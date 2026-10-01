package shipmentsync_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/couriermap"
	"github.com/gluzo/integration-gateway/app/domain/tracking"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/shipmentstate"
	"github.com/gluzo/integration-gateway/app/vendor"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflow/shipmentsync"
	"github.com/gluzo/integration-gateway/app/workflowstate"
)

const (
	testVendor = "vinculum"
	testOrigin = "easyecom"
)

var observed = time.Date(2026, 9, 30, 13, 0, 0, 0, time.UTC)

// stubProvider serves pages of dispatch records.
type stubProvider struct {
	mu    sync.Mutex
	pages [][]tracking.Shipment
	calls int
	err   error
}

func (s *stubProvider) Platform() string { return testVendor }

func (s *stubProvider) FetchShipments(_ context.Context, _ vendor.Route, w vendor.Window) (vendor.ShipmentPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return vendor.ShipmentPage{}, s.err
	}
	if w.Page >= len(s.pages) {
		return vendor.ShipmentPage{}, nil
	}
	return vendor.ShipmentPage{
		Shipments: s.pages[w.Page],
		HasMore:   w.Page < len(s.pages)-1,
		NextPage:  w.Page + 1,
	}, nil
}

func (s *stubProvider) setPage(shipments []tracking.Shipment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages = [][]tracking.Shipment{shipments}
}

// stubSink records the updates it was given.
type stubSink struct {
	mu      sync.Mutex
	updates []vendor.ShipmentUpdate
	err     error
}

func (s *stubSink) Platform() string { return testOrigin }

func (s *stubSink) PushShipment(_ context.Context, _ vendor.Route, u vendor.ShipmentUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.updates = append(s.updates, u)
	return nil
}

func (s *stubSink) all() []vendor.ShipmentUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]vendor.ShipmentUpdate, len(s.updates))
	copy(out, s.updates)
	return out
}

func (s *stubSink) count() int { return len(s.all()) }

type harness struct {
	t        *testing.T
	wf       workflow.Workflow
	exec     *workflow.Executor
	provider *stubProvider
	sink     *stubSink
	pushed   *shipmentstate.MemoryStore
	integID  uuid.UUID
}

func newHarness(t *testing.T, couriers []couriermap.Courier, pages ...[]tracking.Shipment) *harness {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	integID := uuid.New()
	provider := &stubProvider{pages: pages}
	sink := &stubSink{}
	carriers := couriermap.NewMemoryReader()
	carriers.Set(integID, couriers)
	pushed := shipmentstate.NewMemoryStore()

	registry := vendor.NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatalf("register vendor: %v", err)
	}

	wf, err := shipmentsync.New(shipmentsync.Dependencies{
		Vendors:  registry,
		Sinks:    map[string]vendor.ShipmentSink{testOrigin: sink},
		Couriers: carriers,
		Pushed:   pushed,
		Logger:   quiet,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return &harness{
		t: t, wf: wf,
		exec:     workflow.NewExecutor(workflowstate.NewMemoryRepository(), workflow.WithLogger(quiet)),
		provider: provider, sink: sink, pushed: pushed, integID: integID,
	}
}

func (h *harness) run() *workflow.State {
	h.t.Helper()
	state := h.newState()
	if err := h.exec.Run(context.Background(), h.wf, state); err != nil {
		h.t.Fatalf("run: %v", err)
	}
	return state
}

func (h *harness) runExpectingFailure() *workflow.State {
	h.t.Helper()
	state := h.newState()
	_ = h.exec.Run(context.Background(), h.wf, state)
	return state
}

func (h *harness) newState() *workflow.State {
	sweep := shipmentsync.Sweep{
		IntegrationID:   h.integID.String(),
		IntegrationName: "easyecom-vinculum",
		OriginPlatform:  testOrigin,
		VendorPlatform:  testVendor,
		VendorReference: "DEL",
		OriginReference: "bcpl-location",
		From:            observed.Add(-time.Hour),
		To:              observed,
	}
	payload, err := json.Marshal(sweep)
	if err != nil {
		h.t.Fatalf("marshal sweep: %v", err)
	}
	ev := event.Event{
		Platform: testVendor, EventType: event.ShipmentSyncDue,
		CorrelationID: "INT-" + uuid.NewString()[:8], IntegrationID: sweep.IntegrationID,
		RoutingKey:     event.RoutingKey{Type: "vendor_location", Value: "DEL"},
		IdempotencyKey: uuid.NewString(), Payload: payload,
	}
	return workflow.NewState(shipmentsync.Name, ev, "job-1", observed)
}

func shipment(orderID string, status tracking.Status, awb, carrier string) tracking.Shipment {
	return tracking.Shipment{
		OrderExternalID: orderID,
		Status:          status,
		SourceStatus:    string(status),
		TrackingNumber:  awb,
		Carrier:         carrier,
		UpdatedAt:       observed,
	}
}

func courier(transporter, id string) couriermap.Courier {
	return couriermap.Courier{Transporter: transporter, CompanyCarrierID: id, Status: couriermap.StatusActive}
}

// Exit criterion: a delivered-before-shipped sequence leaves the order
// delivered.
//
// This is the ordinary case under dropship, not an edge case: the gateway
// polls on a schedule, so a sweep can easily read "delivered" before it ever
// reads "shipped".
func TestDeliveredBeforeShippedLeavesTheOrderDelivered(t *testing.T) {
	h := newHarness(t, []couriermap.Courier{courier("Delhivery", "77")},
		[]tracking.Shipment{shipment("9876543", tracking.StatusDelivered, "AWB1", "Delhivery")})

	first := h.run()
	if got := first.Result(shipmentsync.ResultPushedShipments); got != "1" {
		t.Fatalf("first run pushed %s, want 1", got)
	}

	// The late "shipped" notice arrives.
	h.provider.setPage([]tracking.Shipment{shipment("9876543", tracking.StatusShipped, "AWB1", "Delhivery")})
	second := h.run()

	if got := second.Result(shipmentsync.ResultPushedShipments); got != "0" {
		t.Errorf("the late notice pushed %s updates, want 0", got)
	}
	if h.sink.count() != 1 {
		t.Errorf("the sink was called %d times; a status must never move backwards", h.sink.count())
	}

	rec, found, err := h.pushed.Get(context.Background(), shipmentstate.Key{
		IntegrationID: h.integID, OrderExternalID: "9876543",
	})
	if err != nil || !found {
		t.Fatalf("Get: %v (found=%v)", err, found)
	}
	if rec.Status != tracking.StatusDelivered {
		t.Errorf("status = %q, want it left at delivered", rec.Status)
	}
}

// Exit criterion: a redelivered shipment event is a no-op.
func TestARedeliveredEventIsANoOp(t *testing.T) {
	h := newHarness(t, []couriermap.Courier{courier("Delhivery", "77")},
		[]tracking.Shipment{shipment("9876543", tracking.StatusShipped, "AWB1", "Delhivery")})

	h.run()
	if h.sink.count() != 1 {
		t.Fatalf("first run pushed %d updates, want 1", h.sink.count())
	}

	// The same sweep again: identical record, nothing to say.
	second := h.run()
	if got := second.Result(shipmentsync.ResultSkippedNoChange); got != "1" {
		t.Errorf("unchanged = %s, want 1", got)
	}
	if h.sink.count() != 1 {
		t.Errorf("the sink was called %d times; a repeat must not reach the platform", h.sink.count())
	}
}

func TestAForwardStatusChangeIsPushed(t *testing.T) {
	h := newHarness(t, []couriermap.Courier{courier("Delhivery", "77")},
		[]tracking.Shipment{shipment("9876543", tracking.StatusShipped, "AWB1", "Delhivery")})
	h.run()

	for _, next := range []tracking.Status{tracking.StatusInTransit, tracking.StatusOutForDelivery, tracking.StatusDelivered} {
		h.provider.setPage([]tracking.Shipment{shipment("9876543", next, "AWB1", "Delhivery")})
		state := h.run()
		if got := state.Result(shipmentsync.ResultPushedShipments); got != "1" {
			t.Errorf("advancing to %s pushed %s, want 1", next, got)
		}
	}
	if h.sink.count() != 4 {
		t.Errorf("the sink was called %d times, want one per advance plus the first", h.sink.count())
	}
}

// A late arrival may correct the tracking number, but doing so must not roll
// a delivered order back to shipped.
func TestACorrectedTrackingNumberDoesNotRegressTheStatus(t *testing.T) {
	h := newHarness(t, []couriermap.Courier{courier("Delhivery", "77")},
		[]tracking.Shipment{shipment("9876543", tracking.StatusDelivered, "AWB1", "Delhivery")})
	h.run()

	h.provider.setPage([]tracking.Shipment{shipment("9876543", tracking.StatusShipped, "AWB-CORRECTED", "Delhivery")})
	state := h.run()

	if got := state.Result(shipmentsync.ResultPushedShipments); got != "1" {
		t.Fatalf("the correction pushed %s updates, want 1", got)
	}
	updates := h.sink.all()
	last := updates[len(updates)-1]
	if last.AdvanceStatus {
		t.Error("a details-only correction was marked as a status advance")
	}
	if last.Shipment.TrackingNumber != "AWB-CORRECTED" {
		t.Errorf("the corrected waybill did not reach the sink: %+v", last.Shipment)
	}

	rec, _, err := h.pushed.Get(context.Background(), shipmentstate.Key{
		IntegrationID: h.integID, OrderExternalID: "9876543",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != tracking.StatusDelivered {
		t.Errorf("status = %q, want it left at delivered", rec.Status)
	}
	if rec.TrackingNumber != "AWB-CORRECTED" {
		t.Errorf("the corrected waybill was not recorded: %q", rec.TrackingNumber)
	}
}

// A sweep that returns a shipment without its tracking number must not erase
// the one already sent.
func TestABlankTrackingNumberDoesNotEraseTheRecordedOne(t *testing.T) {
	h := newHarness(t, []couriermap.Courier{courier("Delhivery", "77")},
		[]tracking.Shipment{shipment("9876543", tracking.StatusShipped, "AWB1", "Delhivery")})
	h.run()

	h.provider.setPage([]tracking.Shipment{shipment("9876543", tracking.StatusInTransit, "", "Delhivery")})
	h.run()

	rec, _, err := h.pushed.Get(context.Background(), shipmentstate.Key{
		IntegrationID: h.integID, OrderExternalID: "9876543",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.TrackingNumber != "AWB1" {
		t.Errorf("tracking number = %q, want the recorded AWB1 kept", rec.TrackingNumber)
	}
}

func TestTheCarrierIsTranslatedForTheOrigin(t *testing.T) {
	h := newHarness(t,
		[]couriermap.Courier{{Transporter: "Blue Dart", CompanyCarrierID: "42", Name: "BlueDart Express", Status: couriermap.StatusActive}},
		[]tracking.Shipment{shipment("9876543", tracking.StatusShipped, "AWB1", "bluedart")})

	h.run()
	updates := h.sink.all()
	if len(updates) != 1 {
		t.Fatalf("got %d updates", len(updates))
	}
	// Matched despite the case and spacing difference: carriers arrive
	// spelled inconsistently and that is not a configuration error.
	if updates[0].CarrierReference != "42" {
		t.Errorf("carrier reference = %q, want 42", updates[0].CarrierReference)
	}
	if updates[0].CarrierName != "BlueDart Express" {
		t.Errorf("carrier name = %q, want the presented name", updates[0].CarrierName)
	}
}

func TestAnUnmappedCarrierFailsTheRunAndNamesIt(t *testing.T) {
	h := newHarness(t, []couriermap.Courier{courier("Delhivery", "77")},
		[]tracking.Shipment{shipment("9876543", tracking.StatusShipped, "AWB1", "Xpressbees")})

	state := h.runExpectingFailure()
	if state.Status != workflow.StatusFailed {
		t.Fatalf("status = %s, want failed", state.Status)
	}
	if state.LastError == nil || state.LastError.Category != apperror.Mapping {
		t.Fatalf("error = %+v, want a mapping error", state.LastError)
	}
	if !strings.Contains(state.LastError.Message, "Xpressbees") {
		t.Errorf("error %q does not name the carrier", state.LastError.Message)
	}
	if h.sink.count() != 0 {
		t.Error("a run with an unmapped carrier pushed anyway")
	}
}

func TestEveryPageIsRead(t *testing.T) {
	h := newHarness(t, []couriermap.Courier{courier("Delhivery", "77")},
		[]tracking.Shipment{shipment("A", tracking.StatusShipped, "AWB-A", "Delhivery")},
		[]tracking.Shipment{shipment("B", tracking.StatusShipped, "AWB-B", "Delhivery")},
		[]tracking.Shipment{shipment("C", tracking.StatusShipped, "AWB-C", "Delhivery")})

	state := h.run()
	if got := state.Result(shipmentsync.ResultVendorRecords); got != "3" {
		t.Errorf("read %s records, want 3 across the pages", got)
	}
	if h.sink.count() != 3 {
		t.Errorf("pushed %d, want 3", h.sink.count())
	}
}

// One order's failure must not cost every other customer their tracking, but
// the run must still be visibly unclean so it is retried.
func TestOneFailedPushIsCountedAndFailsTheRun(t *testing.T) {
	h := newHarness(t, []couriermap.Courier{courier("Delhivery", "77")},
		[]tracking.Shipment{
			shipment("A", tracking.StatusShipped, "AWB-A", "Delhivery"),
			shipment("B", tracking.StatusShipped, "AWB-B", "Delhivery"),
		})
	h.sink.err = errors.New("easyecom unavailable")

	state := h.runExpectingFailure()
	if state.Status != workflow.StatusFailed {
		t.Errorf("status = %s, want failed", state.Status)
	}
	if got := state.Result(shipmentsync.ResultFailedShipments); got != "2" {
		t.Errorf("failed = %s, want 2", got)
	}

	// Nothing recorded, so the next sweep pushes both again rather than
	// believing they landed.
	_, found, err := h.pushed.Get(context.Background(), shipmentstate.Key{
		IntegrationID: h.integID, OrderExternalID: "A",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Error("a failed push was recorded as sent")
	}
}

func TestNothingToPushIsSuccess(t *testing.T) {
	h := newHarness(t, []couriermap.Courier{courier("Delhivery", "77")}, []tracking.Shipment{})

	state := h.run()
	if state.Status != workflow.StatusCompleted {
		t.Errorf("status = %s, want completed: a quiet period is the ordinary case", state.Status)
	}
	if h.sink.count() != 0 {
		t.Error("an empty sweep reached the platform")
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	base := func() shipmentsync.Dependencies {
		reg := vendor.NewRegistry()
		_ = reg.Register(&stubProvider{})
		return shipmentsync.Dependencies{
			Vendors:  reg,
			Sinks:    map[string]vendor.ShipmentSink{testOrigin: &stubSink{}},
			Couriers: couriermap.NewMemoryReader(),
			Pushed:   shipmentstate.NewMemoryStore(),
		}
	}
	tests := []struct {
		name   string
		break_ func(*shipmentsync.Dependencies)
	}{
		{"no registry", func(d *shipmentsync.Dependencies) { d.Vendors = nil }},
		{"no sink", func(d *shipmentsync.Dependencies) { d.Sinks = nil }},
		{"no courier reader", func(d *shipmentsync.Dependencies) { d.Couriers = nil }},
		{"no shipment state", func(d *shipmentsync.Dependencies) { d.Pushed = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps := base()
			tc.break_(&deps)
			if _, err := shipmentsync.New(deps); err == nil {
				t.Error("expected an error")
			}
		})
	}
}
