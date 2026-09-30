package vinculum_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	"github.com/gluzo/integration-gateway/app/integrations/vinculum"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/inventory"
	dtoshipment "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/shipment"
)

const (
	testOwner = "gluzo-owner"
	testKey   = "gluzo-key"
)

// fakeVinculum serves the two read endpoints. It records the decoded request
// bodies so tests can assert what was actually sent, which is the only way to
// check that optional parameters are omitted rather than sent blank.
type fakeVinculum struct {
	t   *testing.T
	srv *httptest.Server

	stockCalls    atomic.Int32
	shipmentCalls atomic.Int32

	lastStockBody    atomic.Value // map[string]any
	lastShipmentBody atomic.Value // map[string]any

	// stockPages is served one page per pageNumber; hasMore is true while
	// further pages remain.
	stockPages [][]map[string]any

	// delay holds the handler before it answers, for the timeout test.
	delay time.Duration

	// envelopeCode, when non-zero, is returned as an application-level
	// failure with HTTP 200.
	envelopeCode int

	// httpStatus, when non-zero, is returned instead of a body.
	httpStatus int

	// alwaysMore makes the stock endpoint claim another page forever.
	alwaysMore bool
}

func newFakeVinculum(t *testing.T) *fakeVinculum {
	f := &fakeVinculum{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/RestWS/api/eretail/v4/stock/getWhInventory", func(w http.ResponseWriter, r *http.Request) {
		f.stockCalls.Add(1)
		body := f.begin(w, r)
		if body == nil {
			return
		}
		f.lastStockBody.Store(body)

		page := 1
		if v, ok := body["pageNumber"].(float64); ok {
			page = int(v)
		}
		rows := []map[string]any{}
		hasMore := f.alwaysMore
		if idx := page - 1; idx >= 0 && idx < len(f.stockPages) {
			rows = f.stockPages[idx]
			hasMore = hasMore || idx < len(f.stockPages)-1
		}
		f.writeJSON(w, map[string]any{
			"responseCode":    0,
			"responseMessage": "SUCCESS",
			"hasMore":         hasMore,
			"response":        rows,
		})
	})
	mux.HandleFunc("/RestWS/api/eretail/v1/order/shipmentDetail", func(w http.ResponseWriter, r *http.Request) {
		f.shipmentCalls.Add(1)
		body := f.begin(w, r)
		if body == nil {
			return
		}
		f.lastShipmentBody.Store(body)
		f.writeJSON(w, map[string]any{
			"responseCode":    0,
			"responseMessage": "SUCCESS",
			"hasMore":         false,
			"response": []map[string]any{{
				"extOrderNo": "9876543",
				"order_no":   "VIN-1",
				"status":     "Shipped",
				"shipDetail": map[string]any{
					"tracking_number": "AWB1",
					"transporter":     "Delhivery",
					"shipdate":        "2026-09-28 14:05:00",
					"status":          "Shipped",
					"invoiceNo":       "BCPL/2026/00891",
					"sellerGstNo":     "07AABCB1234C1ZQ",
				},
			}},
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// begin applies the shared handler behaviour: credential check, injected
// delay and injected failures. A nil return means the response is written.
func (f *fakeVinculum) begin(w http.ResponseWriter, r *http.Request) map[string]any {
	f.t.Helper()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if r.Header.Get("ApiOwner") != testOwner || r.Header.Get("ApiKey") != testKey {
		w.WriteHeader(http.StatusUnauthorized)
		return nil
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	if f.httpStatus != 0 {
		w.WriteHeader(f.httpStatus)
		return nil
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	if f.envelopeCode != 0 {
		// An application-level failure arrives with HTTP 200, which is the
		// whole reason the envelope is checked separately.
		f.writeJSON(w, map[string]any{
			"responseCode":    f.envelopeCode,
			"responseMessage": "SKU does not belong to this location",
			"response":        []any{},
		})
		return nil
	}
	return body
}

func (f *fakeVinculum) writeJSON(w http.ResponseWriter, v any) {
	f.t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		f.t.Errorf("encode response: %v", err)
	}
}

func (f *fakeVinculum) client(t *testing.T, opts ...vinculum.Option) *vinculum.Client {
	t.Helper()
	cfg := vinculum.Config{
		BaseURL:        f.srv.URL,
		APIOwner:       testOwner,
		APIKey:         testKey,
		Location:       "DEL",
		SellableBucket: "Good",
		Timeout:        2 * time.Second,
	}
	c, err := vinculum.NewClient(cfg, append([]vinculum.Option{noSleep()}, opts...)...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// noSleep removes the retry backoff so a test that exercises retries does not
// wait for it.
func noSleep() vinculum.Option {
	return vinculum.WithHTTPOptions(httpclient.WithSleep(func(context.Context, time.Duration) error { return nil }))
}

func stockRow(sku string, qty int) map[string]any {
	return map[string]any{"skuCode": sku, "location": "DEL", "qty": qty, "committedQty": 0, "bucket": "Good"}
}

func TestConfigValidate(t *testing.T) {
	base := vinculum.Config{BaseURL: "https://erp.example.com", APIOwner: "o", APIKey: "k"}
	tests := []struct {
		name string
		cfg  vinculum.Config
		ok   bool
	}{
		{"complete", base, true},
		{"no base URL", vinculum.Config{APIOwner: "o", APIKey: "k"}, false},
		{"no owner", vinculum.Config{BaseURL: base.BaseURL, APIKey: "k"}, false},
		{"no key", vinculum.Config{BaseURL: base.BaseURL, APIOwner: "o"}, false},
		{"three-character location", withLocation(base, "DEL"), true},
		{"longer location", withLocation(base, "DELHI"), false},
		{"negative timeout", withTimeout(base, -1), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.ok && err != nil {
				t.Errorf("expected valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func withLocation(c vinculum.Config, loc string) vinculum.Config { c.Location = loc; return c }
func withTimeout(c vinculum.Config, d time.Duration) vinculum.Config {
	c.Timeout = d
	return c
}

func TestNewClientRejectsIncompleteConfig(t *testing.T) {
	if _, err := vinculum.NewClient(vinculum.Config{APIOwner: "o"}); err == nil {
		t.Fatal("a client without an API key must not be built")
	}
}

func TestGetWhInventorySuccess(t *testing.T) {
	f := newFakeVinculum(t)
	f.stockPages = [][]map[string]any{{stockRow("BCPL-CHY-500", 120)}}
	c := f.client(t)

	resp, err := c.GetWhInventory(context.Background(), dtoinventory.GetWhInventoryRequest{
		LocCode:  "DEL",
		Buckets:  "Good",
		SKUCodes: []string{"BCPL-CHY-500", "  "},
	})
	if err != nil {
		t.Fatalf("GetWhInventory: %v", err)
	}
	if len(resp.Response) != 1 || resp.Response[0].SKUCode.String() != "BCPL-CHY-500" {
		t.Fatalf("unexpected rows: %+v", resp.Response)
	}
	if int(resp.Response[0].Qty) != 120 {
		t.Errorf("qty = %d, want 120", int(resp.Response[0].Qty))
	}

	body, _ := f.lastStockBody.Load().(map[string]any)
	if got := body["locCode"]; got != "DEL" {
		t.Errorf("locCode = %v, want DEL", got)
	}
	skus, ok := body["skuCodes"].([]any)
	if !ok || len(skus) != 1 || skus[0] != "BCPL-CHY-500" {
		t.Errorf("skuCodes = %v, want the one non-blank code", body["skuCodes"])
	}
	// Unset optional parameters must be absent, not blank: the
	// specification does not say how a blank bound is read.
	for _, key := range []string{"fromDate", "toDate", "reqType"} {
		if _, present := body[key]; present {
			t.Errorf("%s was sent although it was never set", key)
		}
	}
}

func TestGetWhInventoryUsesTheConfiguredLocationWhenTheRequestOmitsIt(t *testing.T) {
	f := newFakeVinculum(t)
	f.stockPages = [][]map[string]any{{stockRow("BCPL-CHY-500", 1)}}
	c := f.client(t)

	if _, err := c.GetWhInventory(context.Background(), dtoinventory.GetWhInventoryRequest{}); err != nil {
		t.Fatalf("GetWhInventory: %v", err)
	}
	body, _ := f.lastStockBody.Load().(map[string]any)
	if got := body["locCode"]; got != "DEL" {
		t.Errorf("locCode = %v, want the configured default DEL", got)
	}
}

func TestGetWhInventoryRejectsARequestWithNoLocation(t *testing.T) {
	f := newFakeVinculum(t)
	cfg := vinculum.Config{BaseURL: f.srv.URL, APIOwner: testOwner, APIKey: testKey, Timeout: time.Second}
	c, err := vinculum.NewClient(cfg, noSleep())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = c.GetWhInventory(context.Background(), dtoinventory.GetWhInventoryRequest{})
	var aerr *apperror.Error
	if !errors.As(err, &aerr) || aerr.Category != apperror.Validation {
		t.Fatalf("want a validation error, got %v", err)
	}
	if f.stockCalls.Load() != 0 {
		t.Error("an invalid request must not reach the vendor")
	}
}

func TestEnvelopeErrorIsNotRetried(t *testing.T) {
	f := newFakeVinculum(t)
	f.envelopeCode = 102
	c := f.client(t)

	_, err := c.GetWhInventory(context.Background(), dtoinventory.GetWhInventoryRequest{LocCode: "DEL"})
	var aerr *apperror.Error
	if !errors.As(err, &aerr) {
		t.Fatalf("want an *apperror.Error, got %v", err)
	}
	if aerr.Category != apperror.ExternalAPI {
		t.Errorf("category = %q, want %q", aerr.Category, apperror.ExternalAPI)
	}
	if aerr.Retryable {
		t.Error("an envelope rejection describes the request, not a transient condition; it must not be retryable")
	}
	if aerr.ExternalCode != "102" {
		t.Errorf("external code = %q, want 102", aerr.ExternalCode)
	}
	if aerr.ExternalMessage == "" {
		t.Error("the vendor's own message must be preserved")
	}
	if got := f.stockCalls.Load(); got != 1 {
		t.Errorf("made %d calls, want exactly 1: an HTTP 200 is not retried", got)
	}
}

func TestServerErrorIsRetriedAndReportedAsRetryable(t *testing.T) {
	f := newFakeVinculum(t)
	f.httpStatus = http.StatusInternalServerError
	c := f.client(t)

	_, err := c.GetWhInventory(context.Background(), dtoinventory.GetWhInventoryRequest{LocCode: "DEL"})
	var aerr *apperror.Error
	if !errors.As(err, &aerr) {
		t.Fatalf("want an *apperror.Error, got %v", err)
	}
	if !aerr.Retryable {
		t.Error("a 5xx must be retryable")
	}
	if got := f.stockCalls.Load(); got < 2 {
		t.Errorf("made %d calls, want more than one: a 5xx is retried", got)
	}
}

func TestBadCredentialsAreNotRetried(t *testing.T) {
	f := newFakeVinculum(t)
	cfg := vinculum.Config{BaseURL: f.srv.URL, APIOwner: "wrong", APIKey: "wrong", Location: "DEL", Timeout: time.Second}
	c, err := vinculum.NewClient(cfg, noSleep())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := c.GetWhInventory(context.Background(), dtoinventory.GetWhInventoryRequest{}); err == nil {
		t.Fatal("expected an error")
	}
	// The credentials are static. Asking again with the same headers would
	// only be told the same thing.
	if got := f.stockCalls.Load(); got != 1 {
		t.Errorf("made %d calls, want exactly 1", got)
	}
}

func TestFetchAllWhInventoryFollowsHasMore(t *testing.T) {
	f := newFakeVinculum(t)
	f.stockPages = [][]map[string]any{
		{stockRow("BCPL-A", 10), stockRow("BCPL-B", 20)},
		{stockRow("BCPL-C", 30)},
		{stockRow("BCPL-D", 40)},
	}
	c := f.client(t)

	rows, err := c.FetchAllWhInventory(context.Background(), dtoinventory.GetWhInventoryRequest{LocCode: "DEL"})
	if err != nil {
		t.Fatalf("FetchAllWhInventory: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("read %d rows across the pages, want 4: %+v", len(rows), rows)
	}
	if got := f.stockCalls.Load(); got != 3 {
		t.Errorf("made %d calls, want one per page (3)", got)
	}
	want := []string{"BCPL-A", "BCPL-B", "BCPL-C", "BCPL-D"}
	for i, w := range want {
		if rows[i].SKUCode.String() != w {
			t.Errorf("row %d = %q, want %q; page order must be preserved", i, rows[i].SKUCode.String(), w)
		}
	}
}

func TestFetchAllWhInventoryStopsAtThePageCeiling(t *testing.T) {
	f := newFakeVinculum(t)
	f.alwaysMore = true
	f.stockPages = [][]map[string]any{{stockRow("BCPL-A", 1)}}
	// Every page repeats the first, so hasMore never goes false.
	for i := 1; i < vinculum.MaxStockPages+2; i++ {
		f.stockPages = append(f.stockPages, []map[string]any{stockRow(fmt.Sprintf("BCPL-%d", i), 1)})
	}
	c := f.client(t)

	_, err := c.FetchAllWhInventory(context.Background(), dtoinventory.GetWhInventoryRequest{LocCode: "DEL"})
	if err == nil {
		t.Fatal("a vendor that never stops paging must be reported, not followed forever")
	}
	if got := int(f.stockCalls.Load()); got != vinculum.MaxStockPages {
		t.Errorf("made %d calls, want the %d-page ceiling", got, vinculum.MaxStockPages)
	}
}

func TestFetchAllWhInventoryStopsOnAnEmptyPage(t *testing.T) {
	f := newFakeVinculum(t)
	// hasMore stays true but the page is empty: following it would loop.
	f.alwaysMore = true
	f.stockPages = [][]map[string]any{{stockRow("BCPL-A", 1)}, {}}
	c := f.client(t)

	rows, err := c.FetchAllWhInventory(context.Background(), dtoinventory.GetWhInventoryRequest{LocCode: "DEL"})
	if err != nil {
		t.Fatalf("FetchAllWhInventory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("read %d rows, want 1", len(rows))
	}
	if got := f.stockCalls.Load(); got != 2 {
		t.Errorf("made %d calls, want 2", got)
	}
}

func TestTimeoutIsReportedAsATimeout(t *testing.T) {
	f := newFakeVinculum(t)
	f.delay = 300 * time.Millisecond
	f.stockPages = [][]map[string]any{{stockRow("BCPL-A", 1)}}

	cfg := vinculum.Config{BaseURL: f.srv.URL, APIOwner: testOwner, APIKey: testKey, Location: "DEL", Timeout: 40 * time.Millisecond}
	c, err := vinculum.NewClient(cfg, noSleep(), vinculum.WithHTTPOptions(httpclient.WithRetryPolicy(httpclient.NoRetry())))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = c.GetWhInventory(context.Background(), dtoinventory.GetWhInventoryRequest{})
	var aerr *apperror.Error
	if !errors.As(err, &aerr) {
		t.Fatalf("want an *apperror.Error, got %v", err)
	}
	if aerr.Category != apperror.Timeout {
		t.Errorf("category = %q, want %q", aerr.Category, apperror.Timeout)
	}
	if !aerr.Retryable {
		t.Error("a timeout must be retryable")
	}
}

func TestShipmentDetailSuccess(t *testing.T) {
	f := newFakeVinculum(t)
	c := f.client(t)

	from := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	resp, err := c.ShipmentDetail(context.Background(), dtoshipment.ShipmentDetailRequest{
		OrderLocation: "DEL",
		DateFrom:      from,
		DateTo:        to,
	})
	if err != nil {
		t.Fatalf("ShipmentDetail: %v", err)
	}
	if len(resp.Response) != 1 {
		t.Fatalf("got %d records, want 1", len(resp.Response))
	}
	rec := resp.Response[0]
	if rec.ExtOrderNo.String() != "9876543" || rec.ShipDetail == nil {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if rec.ShipDetail.SellerGstNo.String() != "07AABCB1234C1ZQ" {
		t.Errorf("seller GSTIN = %q, want the vendor's own registration", rec.ShipDetail.SellerGstNo)
	}

	body, _ := f.lastShipmentBody.Load().(map[string]any)
	if got := body["date_from"]; got != "2026-09-28 00:00:00" {
		t.Errorf("date_from = %v, want the documented request layout", got)
	}
	if got := body["order_location"]; got != "DEL" {
		t.Errorf("order_location = %v, want DEL", got)
	}
	if _, present := body["filterBy"]; present {
		t.Error("filterBy was sent although it was never set")
	}
}

func TestShipmentDetailRejectsAnUnboundedRequest(t *testing.T) {
	f := newFakeVinculum(t)
	c := f.client(t)

	// Neither an order list nor a complete window: this would ask BCPL for
	// their entire dispatch history.
	_, err := c.ShipmentDetail(context.Background(), dtoshipment.ShipmentDetailRequest{OrderLocation: "DEL"})
	var aerr *apperror.Error
	if !errors.As(err, &aerr) || aerr.Category != apperror.Validation {
		t.Fatalf("want a validation error, got %v", err)
	}
	if f.shipmentCalls.Load() != 0 {
		t.Error("an unbounded request must not reach the vendor")
	}
}

func TestShipmentDetailRejectsABackwardsWindow(t *testing.T) {
	f := newFakeVinculum(t)
	c := f.client(t)

	to := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	_, err := c.ShipmentDetail(context.Background(), dtoshipment.ShipmentDetailRequest{
		OrderLocation: "DEL",
		DateFrom:      to.Add(24 * time.Hour),
		DateTo:        to,
	})
	if err == nil {
		t.Fatal("a window that ends before it starts must be rejected")
	}
}

func TestShipmentDetailAcceptsAnOrderListWithoutAWindow(t *testing.T) {
	f := newFakeVinculum(t)
	c := f.client(t)

	if _, err := c.ShipmentDetail(context.Background(), dtoshipment.ShipmentDetailRequest{
		OrderLocation: "DEL",
		OrderNos:      []string{"9876543"},
	}); err != nil {
		t.Fatalf("reconciling a known order needs no window: %v", err)
	}
	body, _ := f.lastShipmentBody.Load().(map[string]any)
	orders, ok := body["order_no"].([]any)
	if !ok || len(orders) != 1 || orders[0] != "9876543" {
		t.Errorf("order_no = %v, want the one order", body["order_no"])
	}
}
