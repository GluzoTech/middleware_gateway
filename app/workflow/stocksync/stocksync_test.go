package stocksync_test

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
	"github.com/gluzo/integration-gateway/app/domain/inventory"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/inventorystate"
	"github.com/gluzo/integration-gateway/app/skumap"
	"github.com/gluzo/integration-gateway/app/vendor"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflow/stocksync"
	"github.com/gluzo/integration-gateway/app/workflowstate"
)

const (
	testVendor = "vinculum"
	testOrigin = "easyecom"
)

// stubProvider serves pages of vendor stock.
type stubProvider struct {
	mu    sync.Mutex
	pages [][]inventory.Level
	calls int
	err   error
}

func (s *stubProvider) Platform() string { return testVendor }

func (s *stubProvider) FetchStock(_ context.Context, _ vendor.Route, cursor vendor.StockCursor) (vendor.StockPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return vendor.StockPage{}, s.err
	}
	if cursor.Page >= len(s.pages) {
		return vendor.StockPage{}, nil
	}
	return vendor.StockPage{
		Levels:  s.pages[cursor.Page],
		Next:    vendor.StockCursor{Page: cursor.Page + 1, Since: cursor.Since},
		HasMore: cursor.Page < len(s.pages)-1,
	}, nil
}

// stubSink records what was pushed.
type stubSink struct {
	mu     sync.Mutex
	pushes [][]inventory.Level
	routes []vendor.Route
	err    error
	failN  int
}

func (s *stubSink) Platform() string { return testOrigin }

func (s *stubSink) PushStock(_ context.Context, route vendor.Route, levels []inventory.Level) (vendor.StockResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushes = append(s.pushes, append([]inventory.Level(nil), levels...))
	s.routes = append(s.routes, route)
	if s.err != nil {
		return vendor.StockResult{Failed: len(levels)}, s.err
	}
	return vendor.StockResult{Updated: len(levels) - s.failN, Failed: s.failN}, nil
}

func (s *stubSink) lastPush() []inventory.Level {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pushes) == 0 {
		return nil
	}
	return s.pushes[len(s.pushes)-1]
}

func (s *stubSink) pushCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pushes)
}

type harness struct {
	t         *testing.T
	wf        workflow.Workflow
	exec      *workflow.Executor
	provider  *stubProvider
	sink      *stubSink
	skus      *skumap.MemoryReader
	pushed    *inventorystate.MemoryStore
	integID   uuid.UUID
	originRef string
}

func newHarness(t *testing.T, mappings []skumap.Mapping, pages ...[]inventory.Level) *harness {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	integID := uuid.New()
	provider := &stubProvider{pages: pages}
	sink := &stubSink{}
	skus := skumap.NewMemoryReader()
	skus.Set(integID, mappings)
	pushed := inventorystate.NewMemoryStore()

	registry := vendor.NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatalf("register vendor: %v", err)
	}

	wf, err := stocksync.New(stocksync.Dependencies{
		Vendors: registry,
		Sinks:   map[string]vendor.StockSink{testOrigin: sink},
		SKUs:    skus,
		Pushed:  pushed,
		Logger:  quiet,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	exec := workflow.NewExecutor(workflowstate.NewMemoryRepository(), workflow.WithLogger(quiet))
	return &harness{
		t: t, wf: wf, exec: exec, provider: provider, sink: sink,
		skus: skus, pushed: pushed, integID: integID, originRef: "bcpl-location",
	}
}

func (h *harness) run(full bool) *workflow.State {
	h.t.Helper()
	sweep := stocksync.Sweep{
		IntegrationID:   h.integID.String(),
		IntegrationName: "easyecom-vinculum",
		OriginPlatform:  testOrigin,
		VendorPlatform:  testVendor,
		VendorReference: "DEL",
		OriginReference: h.originRef,
		Full:            full,
	}
	payload, err := json.Marshal(sweep)
	if err != nil {
		h.t.Fatalf("marshal sweep: %v", err)
	}
	ev := event.Event{
		Platform:       testVendor,
		EventType:      event.StockSyncDue,
		CorrelationID:  "INT-" + uuid.NewString()[:8],
		IntegrationID:  sweep.IntegrationID,
		RoutingKey:     event.RoutingKey{Type: "vendor_location", Value: "DEL"},
		IdempotencyKey: uuid.NewString(),
		Payload:        payload,
	}
	state := workflow.NewState(stocksync.Name, ev, "job-1", nowFixed())
	if err := h.exec.Run(context.Background(), h.wf, state); err != nil {
		h.t.Fatalf("run: %v", err)
	}
	return state
}

func (h *harness) runExpectingFailure() *workflow.State {
	h.t.Helper()
	sweep := stocksync.Sweep{
		IntegrationID:   h.integID.String(),
		IntegrationName: "easyecom-vinculum",
		OriginPlatform:  testOrigin,
		VendorPlatform:  testVendor,
		VendorReference: "DEL",
		OriginReference: h.originRef,
	}
	payload, _ := json.Marshal(sweep)
	ev := event.Event{
		Platform: testVendor, EventType: event.StockSyncDue,
		CorrelationID: "INT-" + uuid.NewString()[:8], IntegrationID: sweep.IntegrationID,
		RoutingKey:     event.RoutingKey{Type: "vendor_location", Value: "DEL"},
		IdempotencyKey: uuid.NewString(), Payload: payload,
	}
	state := workflow.NewState(stocksync.Name, ev, "job-1", nowFixed())
	_ = h.exec.Run(context.Background(), h.wf, state)
	return state
}

func mapping(gluzo, vendorSKU string, buffer int) skumap.Mapping {
	return skumap.Mapping{GluzoSKU: gluzo, VendorSKU: vendorSKU, SafetyBuffer: buffer, Status: skumap.StatusActive}
}

func levels(pairs ...any) []inventory.Level {
	out := make([]inventory.Level, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, inventory.Level{SKU: pairs[i].(string), Available: pairs[i+1].(int)})
	}
	return out
}

// Exit criterion 1: a run with no changes pushes nothing.
func TestARunWithNoChangesPushesNothing(t *testing.T) {
	h := newHarness(t,
		[]skumap.Mapping{mapping("GLZ-A", "BCPL-A", 0), mapping("GLZ-B", "BCPL-B", 0)},
		levels("BCPL-A", 100, "BCPL-B", 50))

	first := h.run(false)
	if got := first.Result(stocksync.ResultPushedSKUs); got != "2" {
		t.Fatalf("first run pushed %s, want 2", got)
	}

	second := h.run(false)
	if got := second.Result(stocksync.ResultChangedSKUs); got != "0" {
		t.Errorf("second run found %s changed, want 0", got)
	}
	if got := second.Result(stocksync.ResultUnchanged); got != "2" {
		t.Errorf("second run counted %s unchanged, want 2", got)
	}
	// The sink must not be called at all. A push of zero SKUs would still
	// spend a request against the platform's rate limit.
	if h.sink.pushCount() != 1 {
		t.Errorf("the sink was called %d times; the second run should not have pushed", h.sink.pushCount())
	}
}

// Exit criterion 2: a run with one change pushes one SKU.
func TestARunWithOneChangePushesOneSKU(t *testing.T) {
	h := newHarness(t,
		[]skumap.Mapping{mapping("GLZ-A", "BCPL-A", 0), mapping("GLZ-B", "BCPL-B", 0)},
		levels("BCPL-A", 100, "BCPL-B", 50))
	h.run(false)

	// One SKU moves.
	h.provider.mu.Lock()
	h.provider.pages = [][]inventory.Level{levels("BCPL-A", 100, "BCPL-B", 42)}
	h.provider.mu.Unlock()

	state := h.run(false)
	if got := state.Result(stocksync.ResultChangedSKUs); got != "1" {
		t.Fatalf("changed = %s, want 1", got)
	}
	if got := state.Result(stocksync.ResultPushedSKUs); got != "1" {
		t.Errorf("pushed = %s, want 1", got)
	}
	last := h.sink.lastPush()
	if len(last) != 1 || last[0].SKU != "GLZ-B" || last[0].Available != 42 {
		t.Errorf("pushed %+v, want only GLZ-B at 42", last)
	}
}

// Exit criterion 3: the nightly sweep pushes everything.
func TestTheFullSweepPushesEverything(t *testing.T) {
	h := newHarness(t,
		[]skumap.Mapping{mapping("GLZ-A", "BCPL-A", 0), mapping("GLZ-B", "BCPL-B", 0)},
		levels("BCPL-A", 100, "BCPL-B", 50))
	h.run(false)

	// Nothing has moved, so an incremental run would push nothing.
	state := h.run(true)
	if got := state.Result(stocksync.ResultFullPush); got != "true" {
		t.Errorf("full push flag = %q", got)
	}
	if got := state.Result(stocksync.ResultPushedSKUs); got != "2" {
		t.Errorf("pushed = %s, want 2: the full sweep ignores what was last recorded", got)
	}
	if len(h.sink.lastPush()) != 2 {
		t.Errorf("full sweep pushed %d SKUs, want 2", len(h.sink.lastPush()))
	}
}

// The full sweep is the repair path for drift, so it has to work in the case
// drift actually arises: the platform was written but the record was not.
func TestTheFullSweepRepairsADivergedRecord(t *testing.T) {
	h := newHarness(t,
		[]skumap.Mapping{mapping("GLZ-A", "BCPL-A", 0)},
		levels("BCPL-A", 100))
	h.run(false)

	// Simulate the record being wrong: it claims a quantity the platform
	// never received.
	key := inventorystate.Key{IntegrationID: h.integID, OriginReference: h.originRef}
	if err := h.pushed.Record(context.Background(), key, []inventorystate.Entry{{GluzoSKU: "GLZ-A", Quantity: 999}}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// An incremental run now disagrees with the record and pushes, which is
	// the benign direction. The full run pushes regardless.
	state := h.run(true)
	if got := state.Result(stocksync.ResultPushedSKUs); got != "1" {
		t.Errorf("pushed = %s, want 1", got)
	}
	if last := h.sink.lastPush(); len(last) != 1 || last[0].Available != 100 {
		t.Errorf("pushed %+v, want the vendor's actual 100", last)
	}
}

func TestSafetyBufferIsAppliedAndZeroPropagates(t *testing.T) {
	h := newHarness(t,
		[]skumap.Mapping{mapping("GLZ-A", "BCPL-A", 5), mapping("GLZ-B", "BCPL-B", 50)},
		levels("BCPL-A", 100, "BCPL-B", 0))

	h.run(false)
	last := h.sink.lastPush()
	got := map[string]int{}
	for _, l := range last {
		got[l.SKU] = l.Available
	}
	if got["GLZ-A"] != 95 {
		t.Errorf("GLZ-A = %d, want 100 less the buffer of 5", got["GLZ-A"])
	}
	// A large buffer must not delay a zero: an item the vendor has none of
	// is out of stock now.
	if q, ok := got["GLZ-B"]; !ok || q != 0 {
		t.Errorf("GLZ-B = %d (present=%v), want 0 published immediately", q, ok)
	}
}

func TestEveryPageIsReadBeforeAnythingIsPushed(t *testing.T) {
	h := newHarness(t,
		[]skumap.Mapping{mapping("GLZ-A", "BCPL-A", 0), mapping("GLZ-B", "BCPL-B", 0), mapping("GLZ-C", "BCPL-C", 0)},
		levels("BCPL-A", 1), levels("BCPL-B", 2), levels("BCPL-C", 3))

	state := h.run(false)
	if got := state.Result(stocksync.ResultVendorRows); got != "3" {
		t.Errorf("read %s rows, want 3 across the pages", got)
	}
	// One push, not one per page: a page-at-a-time push is interruptible
	// halfway and leaves the storefront holding a mixture of two sweeps.
	if h.sink.pushCount() != 1 {
		t.Errorf("the sink was called %d times, want once for the whole sweep", h.sink.pushCount())
	}
	if len(h.sink.lastPush()) != 3 {
		t.Errorf("pushed %d SKUs, want 3", len(h.sink.lastPush()))
	}
}

// An unmapped SKU must fail the action, not be skipped. The error must name
// the SKUs, because adding them is the operator's next action.
func TestAnUnmappedSKUFailsTheRunAndNamesTheSKUs(t *testing.T) {
	h := newHarness(t,
		[]skumap.Mapping{mapping("GLZ-A", "BCPL-A", 0)},
		levels("BCPL-A", 10, "BCPL-MISSING", 5))

	state := h.runExpectingFailure()
	if state.Status != workflow.StatusFailed {
		t.Fatalf("status = %s, want failed", state.Status)
	}
	if state.LastError == nil {
		t.Fatal("no error recorded")
	}
	if state.LastError.Category != apperror.Mapping {
		t.Errorf("category = %s, want %s", state.LastError.Category, apperror.Mapping)
	}
	if !strings.Contains(state.LastError.Message, "BCPL-MISSING") {
		t.Errorf("error %q does not name the unmapped sku", state.LastError.Message)
	}
	if h.sink.pushCount() != 0 {
		t.Error("a run with an unmapped SKU pushed anyway")
	}
}

// A mapping failure is permanent: retrying will not add the missing row. The
// MAP_STOCK policy must therefore be a single attempt.
func TestMappingIsNeverRetried(t *testing.T) {
	h := newHarness(t,
		[]skumap.Mapping{},
		levels("BCPL-ONLY", 1))

	state := h.runExpectingFailure()
	var mapRecord *workflow.ActionRecord
	for i := range state.Actions {
		if state.Actions[i].Name == stocksync.ActionMapStock {
			mapRecord = &state.Actions[i]
		}
	}
	if mapRecord == nil {
		t.Fatal("MAP_STOCK did not run")
	}
	if mapRecord.Attempt != 1 {
		t.Errorf("MAP_STOCK ran %d attempts, want 1", mapRecord.Attempt)
	}
}

func TestAFailedPushDoesNotRecordTheQuantities(t *testing.T) {
	h := newHarness(t,
		[]skumap.Mapping{mapping("GLZ-A", "BCPL-A", 0)},
		levels("BCPL-A", 100))
	h.sink.err = errors.New("easyecom unavailable")

	_ = h.runExpectingFailure()

	// Nothing recorded, so the next run pushes again rather than believing
	// the quantity already landed.
	last, err := h.pushed.Load(context.Background(), inventorystate.Key{IntegrationID: h.integID, OriginReference: h.originRef})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(last) != 0 {
		t.Errorf("a failed push recorded %+v", last)
	}
}

func TestPerOriginLocationRecordsAreIndependent(t *testing.T) {
	h := newHarness(t,
		[]skumap.Mapping{mapping("GLZ-A", "BCPL-A", 0)},
		levels("BCPL-A", 100))
	h.run(false)

	// The same SKU pushed to a second origin location has its own record,
	// so it must push rather than be treated as unchanged.
	h.originRef = "other-location"
	state := h.run(false)
	if got := state.Result(stocksync.ResultPushedSKUs); got != "1" {
		t.Errorf("pushed = %s to the second location, want 1", got)
	}
}

func TestSweepRouteReachesTheSink(t *testing.T) {
	h := newHarness(t,
		[]skumap.Mapping{mapping("GLZ-A", "BCPL-A", 0)},
		levels("BCPL-A", 1))
	h.run(false)

	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	if len(h.sink.routes) != 1 {
		t.Fatalf("got %d routes", len(h.sink.routes))
	}
	r := h.sink.routes[0]
	if r.OriginReference != "bcpl-location" || r.VendorReference != "DEL" {
		t.Errorf("route = %+v; the sink must be told which location to authenticate for", r)
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	base := func() stocksync.Dependencies {
		reg := vendor.NewRegistry()
		_ = reg.Register(&stubProvider{})
		return stocksync.Dependencies{
			Vendors: reg,
			Sinks:   map[string]vendor.StockSink{testOrigin: &stubSink{}},
			SKUs:    skumap.NewMemoryReader(),
			Pushed:  inventorystate.NewMemoryStore(),
		}
	}
	tests := []struct {
		name  string
		mutue func(*stocksync.Dependencies)
	}{
		{"no registry", func(d *stocksync.Dependencies) { d.Vendors = nil }},
		{"no sink", func(d *stocksync.Dependencies) { d.Sinks = nil }},
		{"no sku reader", func(d *stocksync.Dependencies) { d.SKUs = nil }},
		{"no inventory state", func(d *stocksync.Dependencies) { d.Pushed = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps := base()
			tc.mutue(&deps)
			if _, err := stocksync.New(deps); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// nowFixed is the clock every state in this file is created with; the
// workflow itself reads no clock, so a fixed one keeps runs comparable.
func nowFixed() time.Time {
	return time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
}
