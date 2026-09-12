package dabur_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/inventory"
	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/httpclient"
	"github.com/gluzo/integration-gateway/app/integrations/dabur"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/dabur/dto/order"
	"github.com/gluzo/integration-gateway/app/workflow"
)

// fakeUniware simulates the OAuth, sale order and inventory endpoints.
type fakeUniware struct {
	t             *testing.T
	srv           *httptest.Server
	tokenGrants   atomic.Int32
	refreshGrants atomic.Int32
	createCalls   atomic.Int32
	token         string
	revokeOnce    atomic.Bool
	created       map[string]json.RawMessage
	lastFacility  string
}

func newFakeUniware(t *testing.T) *fakeUniware {
	f := &fakeUniware{t: t, token: "tok-1", created: map[string]json.RawMessage{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("grant_type") {
		case "password":
			f.tokenGrants.Add(1)
			if q.Get("client_id") != "my-trusted-client" || q.Get("username") != "api-user" || q.Get("password") != "pw" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Bad credentials"}`))
				return
			}
		case "refresh_token":
			f.refreshGrants.Add(1)
			if q.Get("refresh_token") != "refresh-1" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"` + f.token + `","token_type":"bearer","refresh_token":"refresh-1","expires_in":41621,"scope":"read trust write"}`))
	})
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "bearer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		if f.revokeOnce.CompareAndSwap(true, false) {
			f.token = "tok-2"
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/services/rest/v1/oms/saleOrder/create", func(w http.ResponseWriter, r *http.Request) {
		f.createCalls.Add(1)
		if !authed(w, r) {
			return
		}
		f.lastFacility = r.Header.Get("Facility")
		var body struct {
			SaleOrder struct {
				Code string `json:"code"`
			} `json:"saleOrder"`
		}
		raw, _ := json.Marshal(nil)
		_ = raw
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&body); err != nil || body.SaleOrder.Code == "" {
			_, _ = w.Write([]byte(`{"successful":false,"message":"","errors":[{"code":10001,"fieldName":"code","description":"Invalid request","message":"code is required"}]}`))
			return
		}
		if body.SaleOrder.Code == "SERVER-ERR" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if _, dup := f.created[body.SaleOrder.Code]; dup {
			_, _ = w.Write([]byte(`{"successful":false,"errors":[{"code":10010,"description":"Sale order with code ` + body.SaleOrder.Code + ` already exists","message":""}]}`))
			return
		}
		f.created[body.SaleOrder.Code] = json.RawMessage(`{}`)
		_, _ = w.Write([]byte(`{"successful":true,"message":"","errors":[],"warnings":[],"saleOrderDetailDTO":{"code":"` + body.SaleOrder.Code + `","status":"CREATED"}}`))
	})
	mux.HandleFunc("/services/rest/v1/oms/saleorder/get", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		var body dtoorder.GetSaleOrderRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := f.created[body.Code]; !ok {
			_, _ = w.Write([]byte(`{"successful":false,"errors":[{"code":10011,"description":"Sale order not found"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"successful":true,"saleOrderDTO":{"code":"` + body.Code + `","status":"PROCESSING","shippingPackages":[{"code":"PKG1","status":"SHIPPED","trackingNumber":"AWB-9","shippingProvider":"Delhivery","trackingStatus":"IN_TRANSIT"}]}}`))
	})
	mux.HandleFunc("/services/rest/v1/inventory/adjust/bulk", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		f.lastFacility = r.Header.Get("Facility")
		var body struct {
			InventoryAdjustments []struct {
				ItemSKU string `json:"itemSKU"`
			} `json:"inventoryAdjustments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var parts []string
		for _, a := range body.InventoryAdjustments {
			ok := "true"
			if a.ItemSKU == "BAD" {
				ok = "false"
			}
			parts = append(parts, `{"facilityInventoryAdjustment":{"itemSKU":"`+a.ItemSKU+`"},"successful":`+ok+`,"errors":[]}`)
		}
		_, _ = w.Write([]byte(`{"successful":true,"inventoryAdjustmentResponses":[` + strings.Join(parts, ",") + `]}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUniware) config() dabur.Config {
	return dabur.Config{BaseURL: f.srv.URL, Username: "api-user", Password: "pw", DefaultFacility: "DABUR-DEL", Channel: "GLUZO", Timeout: 2 * time.Second}
}

func noBackoff() dabur.Option {
	return dabur.WithHTTPOptions(httpclient.WithSleep(func(context.Context, time.Duration) error { return nil }))
}

func sampleOrder(code string) order.Order {
	addr := order.Address{Name: "Asha Verma", Line1: "12 MG Road", City: "Pune", State: "Maharashtra", Phone: "9876501234"}
	return order.Order{
		ExternalID: code, ReferenceCode: "AMZ-1", PaymentMode: order.PaymentPrepaid, TotalAmount: 100,
		Customer: order.Customer{Name: "Asha Verma", Phone: "9876501234"}, ShippingAddress: addr, BillingAddress: addr,
		Items: []order.Item{{ExternalID: "L1", SKU: "DAB-1", Quantity: 1, UnitPrice: 100}},
	}
}

func TestNewClientValidatesConfig(t *testing.T) {
	if _, err := dabur.NewClient(dabur.Config{BaseURL: "https://x"}); err == nil {
		t.Fatal("missing credentials accepted")
	}
	if _, err := dabur.NewClient(dabur.Config{Username: "u", Password: "p"}); err == nil {
		t.Fatal("missing base URL accepted")
	}
	c, err := dabur.NewClient(dabur.Config{BaseURL: "https://x", Username: "u", Password: "p"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if cfg := c.Config(); cfg.ClientID != dabur.DefaultClientID || cfg.ShelfCode != dabur.DefaultShelfCode {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestDestinationCreatesOrderWithFacilityAndToken(t *testing.T) {
	f := newFakeUniware(t)
	c, err := dabur.NewClient(f.config(), noBackoff())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	dst := dabur.NewDestination(c, nil)
	if dst.Platform() != "dabur" {
		t.Fatalf("platform = %q", dst.Platform())
	}
	route := workflow.RouteInfo{RouteType: "warehouse_id", RouteValue: "12345", DestinationReference: "DABUR-MUM"}

	prepared, err := dst.PrepareOrder(context.Background(), sampleOrder("9876543"), route)
	if err != nil {
		t.Fatalf("PrepareOrder: %v", err)
	}
	if !strings.Contains(string(prepared), `"facilityCode":"DABUR-MUM"`) || !strings.Contains(string(prepared), `"channel":"GLUZO"`) {
		t.Fatalf("prepared = %s", prepared)
	}

	res, err := dst.SubmitOrder(context.Background(), prepared, sampleOrder("9876543"), route)
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if res.DestinationOrderID != "9876543" || !res.Created {
		t.Fatalf("result = %+v", res)
	}
	if f.lastFacility != "DABUR-MUM" {
		t.Fatalf("Facility header = %q", f.lastFacility)
	}
	if f.tokenGrants.Load() != 1 {
		t.Fatalf("password grants = %d, want 1", f.tokenGrants.Load())
	}
}

func TestSubmitOrderIsIdempotent(t *testing.T) {
	f := newFakeUniware(t)
	c, _ := dabur.NewClient(f.config(), noBackoff())
	dst := dabur.NewDestination(c, nil)
	route := workflow.RouteInfo{}
	o := sampleOrder("DUP-1")
	prepared, _ := dst.PrepareOrder(context.Background(), o, route)

	first, err := dst.SubmitOrder(context.Background(), prepared, o, route)
	if err != nil || !first.Created {
		t.Fatalf("first submit: %+v %v", first, err)
	}
	second, err := dst.SubmitOrder(context.Background(), prepared, o, route)
	if err != nil {
		t.Fatalf("second submit should succeed idempotently: %v", err)
	}
	if second.Created || second.DestinationOrderID != "DUP-1" {
		t.Fatalf("second result = %+v", second)
	}
	if f.lastFacility != "DABUR-DEL" {
		t.Fatalf("default facility not used: %q", f.lastFacility)
	}
}

func TestRevokedTokenIsReacquired(t *testing.T) {
	f := newFakeUniware(t)
	c, _ := dabur.NewClient(f.config(), noBackoff())
	dst := dabur.NewDestination(c, nil)
	o := sampleOrder("R-1")
	prepared, _ := dst.PrepareOrder(context.Background(), o, workflow.RouteInfo{})
	if _, err := dst.SubmitOrder(context.Background(), prepared, o, workflow.RouteInfo{}); err != nil {
		t.Fatalf("warm-up: %v", err)
	}

	f.revokeOnce.Store(true)
	o2 := sampleOrder("R-2")
	prepared2, _ := dst.PrepareOrder(context.Background(), o2, workflow.RouteInfo{})
	if _, err := dst.SubmitOrder(context.Background(), prepared2, o2, workflow.RouteInfo{}); err != nil {
		t.Fatalf("call after revocation should recover: %v", err)
	}
	if f.tokenGrants.Load()+f.refreshGrants.Load() < 2 {
		t.Fatalf("token was not re-acquired: password=%d refresh=%d", f.tokenGrants.Load(), f.refreshGrants.Load())
	}
}

func TestBadCredentialsAreAuthenticationErrors(t *testing.T) {
	f := newFakeUniware(t)
	cfg := f.config()
	cfg.Password = "wrong"
	c, _ := dabur.NewClient(cfg, noBackoff())
	dst := dabur.NewDestination(c, nil)
	o := sampleOrder("A-1")
	prepared, _ := dst.PrepareOrder(context.Background(), o, workflow.RouteInfo{})
	_, err := dst.SubmitOrder(context.Background(), prepared, o, workflow.RouteInfo{})
	if apperror.CategoryOf(err) != apperror.Authentication || apperror.IsRetryable(err) {
		t.Fatalf("expected authentication error, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("password leaked into error: %v", err)
	}
	if f.createCalls.Load() != 0 {
		t.Fatal("create must not be called without a token")
	}
}

func TestApplicationErrorsAreNotRetried(t *testing.T) {
	f := newFakeUniware(t)
	c, _ := dabur.NewClient(f.config(), noBackoff())
	_, err := c.CreateSaleOrder(context.Background(), "F", json.RawMessage(`{"saleOrder":{}}`))
	if err == nil || apperror.IsRetryable(err) || apperror.CategoryOf(err) != apperror.ExternalAPI {
		t.Fatalf("expected non-retryable external error, got %v", err)
	}
	if !strings.Contains(err.Error(), "code 10001") || !strings.Contains(err.Error(), "code is required") {
		t.Fatalf("error lacks uniware detail: %v", err)
	}
	if f.createCalls.Load() != 1 {
		t.Fatalf("create calls = %d", f.createCalls.Load())
	}
}

func TestServerErrorsAreRetried(t *testing.T) {
	f := newFakeUniware(t)
	c, _ := dabur.NewClient(f.config(), noBackoff())
	_, err := c.CreateSaleOrder(context.Background(), "F", json.RawMessage(`{"saleOrder":{"code":"SERVER-ERR"}}`))
	if err == nil || !apperror.IsRetryable(err) {
		t.Fatalf("expected retryable error, got %v", err)
	}
	if f.createCalls.Load() != int32(httpclient.DefaultMaxAttempts) {
		t.Fatalf("create calls = %d, want %d", f.createCalls.Load(), httpclient.DefaultMaxAttempts)
	}
}

func TestMissingFacilityIsAMappingError(t *testing.T) {
	f := newFakeUniware(t)
	cfg := f.config()
	cfg.DefaultFacility = ""
	c, _ := dabur.NewClient(cfg, noBackoff())
	dst := dabur.NewDestination(c, nil)
	_, err := dst.PrepareOrder(context.Background(), sampleOrder("X"), workflow.RouteInfo{RouteType: "warehouse_id", RouteValue: "1"})
	if apperror.CategoryOf(err) != apperror.Mapping || !strings.Contains(err.Error(), "DABUR_DEFAULT_FACILITY") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateInventoryCountsOutcomes(t *testing.T) {
	f := newFakeUniware(t)
	c, _ := dabur.NewClient(f.config(), noBackoff())
	dst := dabur.NewDestination(c, nil)
	res, err := dst.UpdateInventory(context.Background(), []inventory.Level{{SKU: "A", Available: 5}, {SKU: "BAD", Available: 1}, {SKU: "C", Available: 0}}, workflow.RouteInfo{DestinationReference: "F9"})
	if err != nil {
		t.Fatalf("UpdateInventory: %v", err)
	}
	if res.Updated != 2 || res.Failed != 1 || f.lastFacility != "F9" {
		t.Fatalf("result = %+v facility %q", res, f.lastFacility)
	}
	empty, err := dst.UpdateInventory(context.Background(), nil, workflow.RouteInfo{})
	if err != nil || empty.Updated != 0 {
		t.Fatalf("empty levels: %+v %v", empty, err)
	}
}

func TestGetSaleOrder(t *testing.T) {
	f := newFakeUniware(t)
	c, _ := dabur.NewClient(f.config(), noBackoff())
	if _, err := c.CreateSaleOrder(context.Background(), "F", json.RawMessage(`{"saleOrder":{"code":"G-1"}}`)); err != nil {
		t.Fatalf("create: %v", err)
	}
	resp, err := c.GetSaleOrder(context.Background(), dtoorder.GetSaleOrderRequest{Code: "G-1"})
	if err != nil {
		t.Fatalf("GetSaleOrder: %v", err)
	}
	if resp.SaleOrderDTO == nil || len(resp.SaleOrderDTO.ShippingPackages) != 1 || resp.SaleOrderDTO.ShippingPackages[0].TrackingNumber != "AWB-9" {
		t.Fatalf("response = %+v", resp.SaleOrderDTO)
	}
	if _, err := c.GetSaleOrder(context.Background(), dtoorder.GetSaleOrderRequest{Code: "missing"}); err == nil || apperror.IsRetryable(err) {
		t.Fatalf("missing order: %v", err)
	}
	if _, err := c.GetSaleOrder(context.Background(), dtoorder.GetSaleOrderRequest{}); apperror.CategoryOf(err) != apperror.Validation {
		t.Fatalf("empty code: %v", err)
	}
}
