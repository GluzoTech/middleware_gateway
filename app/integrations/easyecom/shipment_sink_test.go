package easyecom_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/domain/tracking"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom"
	"github.com/gluzo/integration-gateway/app/vendor"
)

// fakeShipmentEasyEcom records the two dispatch write calls.
type fakeShipmentEasyEcom struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	assigns  []map[string]any
	statuses []map[string]any
	tokens   map[string]string
}

func newFakeShipmentEasyEcom(t *testing.T) *fakeShipmentEasyEcom {
	f := &fakeShipmentEasyEcom{t: t, tokens: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/access/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		location := body["location_key"]
		token := "jwt-for-" + location
		f.mu.Lock()
		f.tokens[token] = location
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"token":{"jwt_token":"` + token + `"}}}`))
	})
	record := func(dst *[]map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			// The location the token was issued for travels with the call,
			// so a test can prove a dispatch was written under the route's
			// own credentials.
			auth := r.Header.Get("Authorization")
			f.mu.Lock()
			body["_location"] = f.tokens[auth[len("Bearer "):]]
			*dst = append(*dst, body)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"code":200,"message":"ok"}`))
		}
	}
	mux.HandleFunc("/AssignShipmentDetails", record(&f.assigns))
	mux.HandleFunc("/updateTrackingStatus", record(&f.statuses))

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeShipmentEasyEcom) assigned() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.assigns...)
}

func (f *fakeShipmentEasyEcom) statusUpdates() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.statuses...)
}

func (f *fakeShipmentEasyEcom) sink(t *testing.T, ids easyecom.ShipmentStatusIDs) *easyecom.Sink {
	t.Helper()
	c, err := easyecom.NewClient(easyecom.Config{
		BaseURL: f.srv.URL, APIKey: "key-123",
		Email: "ops@gluzo.com", Password: "pw", LocationKey: "default-location",
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return easyecom.NewSink(c, slog.New(slog.NewTextHandler(io.Discard, nil)),
		easyecom.WithShipmentStatusIDs(ids))
}

func shipmentUpdate(status tracking.Status, advance bool) vendor.ShipmentUpdate {
	return vendor.ShipmentUpdate{
		Shipment: tracking.Shipment{
			OrderExternalID: "9876543",
			Status:          status,
			TrackingNumber:  "AWB1",
			Carrier:         "Delhivery",
			UpdatedAt:       time.Date(2026, 9, 30, 13, 0, 0, 0, time.UTC),
		},
		CarrierReference: "77",
		CarrierName:      "Delhivery Express",
		AdvanceStatus:    advance,
	}
}

func TestPushShipmentAssignsDetailsAndStatus(t *testing.T) {
	ctx := context.Background()
	f := newFakeShipmentEasyEcom(t)
	sink := f.sink(t, easyecom.ShipmentStatusIDs{tracking.StatusShipped: "3"})

	if err := sink.PushShipment(ctx, route("bcpl-location"), shipmentUpdate(tracking.StatusShipped, true)); err != nil {
		t.Fatalf("PushShipment: %v", err)
	}

	assigns := f.assigned()
	if len(assigns) != 1 {
		t.Fatalf("got %d assignments, want 1", len(assigns))
	}
	if got := assigns[0]["awbNum"]; got != "AWB1" {
		t.Errorf("awbNum = %v", got)
	}
	if got := assigns[0]["companyCarrierId"]; got != "77" {
		t.Errorf("companyCarrierId = %v, want the resolved carrier reference", got)
	}
	if got := assigns[0]["courier"]; got != "Delhivery Express" {
		t.Errorf("courier = %v, want the presented name", got)
	}
	// Written under the route's own location, as a stock push is.
	if got := assigns[0]["_location"]; got != "bcpl-location" {
		t.Errorf("assignment authenticated for %v, want bcpl-location", got)
	}

	statuses := f.statusUpdates()
	if len(statuses) != 1 {
		t.Fatalf("got %d status updates, want 1", len(statuses))
	}
	if got := statuses[0]["current_shipment_status_id"]; got != "3" {
		t.Errorf("status id = %v, want the configured 3", got)
	}
}

// Without the enumeration the gateway will not guess a status id: a wrong one
// puts an order into a state nobody asked for, with no error to notice. The
// dispatch details still land, so the customer gets a tracking number.
func TestPushShipmentAssignsDetailsEvenWithNoStatusEnumeration(t *testing.T) {
	ctx := context.Background()
	f := newFakeShipmentEasyEcom(t)
	sink := f.sink(t, nil)

	if err := sink.PushShipment(ctx, route("bcpl-location"), shipmentUpdate(tracking.StatusShipped, true)); err != nil {
		t.Fatalf("PushShipment: %v", err)
	}
	if len(f.assigned()) != 1 {
		t.Error("the dispatch details should still have been assigned")
	}
	if got := len(f.statusUpdates()); got != 0 {
		t.Errorf("made %d status updates with no configured enumeration, want 0", got)
	}
}

// A details-only correction must not send a status at all, or a corrected
// tracking number would roll a delivered order back.
func TestPushShipmentSkipsTheStatusWhenItDoesNotAdvance(t *testing.T) {
	ctx := context.Background()
	f := newFakeShipmentEasyEcom(t)
	sink := f.sink(t, easyecom.ShipmentStatusIDs{tracking.StatusShipped: "3"})

	if err := sink.PushShipment(ctx, route("bcpl-location"), shipmentUpdate(tracking.StatusShipped, false)); err != nil {
		t.Fatalf("PushShipment: %v", err)
	}
	if len(f.assigned()) != 1 {
		t.Error("the corrected details should have been assigned")
	}
	if got := len(f.statusUpdates()); got != 0 {
		t.Errorf("made %d status updates for a details-only push, want 0", got)
	}
}

func TestPushShipmentCarriesTheDeliveryDate(t *testing.T) {
	ctx := context.Background()
	f := newFakeShipmentEasyEcom(t)
	sink := f.sink(t, easyecom.ShipmentStatusIDs{tracking.StatusDelivered: "7"})

	delivered := time.Date(2026, 9, 29, 11, 40, 0, 0, time.UTC)
	u := shipmentUpdate(tracking.StatusDelivered, true)
	u.Shipment.DeliveredAt = &delivered

	if err := sink.PushShipment(ctx, route("bcpl-location"), u); err != nil {
		t.Fatalf("PushShipment: %v", err)
	}
	statuses := f.statusUpdates()
	if len(statuses) != 1 {
		t.Fatalf("got %d status updates", len(statuses))
	}
	if got := statuses[0]["delivery_date"]; got != "2026-09-29 11:40:00" {
		t.Errorf("delivery_date = %v", got)
	}
}

// The status update is addressed by waybill. Without one there is nothing to
// address it to, and sending it would be a guess about which parcel moved.
func TestPushShipmentSkipsTheStatusWithNoWaybill(t *testing.T) {
	ctx := context.Background()
	f := newFakeShipmentEasyEcom(t)
	sink := f.sink(t, easyecom.ShipmentStatusIDs{tracking.StatusShipped: "3"})

	u := shipmentUpdate(tracking.StatusShipped, true)
	u.Shipment.TrackingNumber = ""

	if err := sink.PushShipment(ctx, route("bcpl-location"), u); err != nil {
		t.Fatalf("PushShipment: %v", err)
	}
	if len(f.assigned()) != 1 {
		t.Error("the carrier should still have been assigned")
	}
	if got := len(f.statusUpdates()); got != 0 {
		t.Errorf("made %d status updates with no waybill, want 0", got)
	}
}

func TestPushShipmentRejectsAnIncompleteShipment(t *testing.T) {
	ctx := context.Background()
	f := newFakeShipmentEasyEcom(t)
	sink := f.sink(t, nil)

	// Neither a waybill nor a carrier: nothing a customer could use, and
	// sending it would overwrite a record that may have had both.
	u := shipmentUpdate(tracking.StatusShipped, true)
	u.Shipment.TrackingNumber, u.Shipment.Carrier, u.CarrierName = "", "", ""

	if err := sink.PushShipment(ctx, route("bcpl-location"), u); err == nil {
		t.Fatal("an empty dispatch must not be pushed")
	}
	if len(f.assigned()) != 0 {
		t.Error("an empty dispatch reached the platform")
	}
}
