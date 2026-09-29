package ordersync_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/intlog"
	"github.com/gluzo/integration-gateway/app/routing"
	"github.com/gluzo/integration-gateway/app/vendor"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflow/ordersync"
	"github.com/gluzo/integration-gateway/app/workflowstate"
)

// fakeOrigin and fakeVendor let tests script every adapter call.
type fakeOrigin struct {
	fetchOrderErr   error
	fetchOrderCalls int
}

func (f *fakeOrigin) Platform() string { return "easyecom" }
func (f *fakeOrigin) FetchOrder(_ context.Context, ev event.Event) (order.Order, error) {
	f.fetchOrderCalls++
	if f.fetchOrderErr != nil {
		return order.Order{}, f.fetchOrderErr
	}
	return order.Order{
		ExternalID:  ev.ExternalOrderID,
		WarehouseID: ev.RoutingKey.Value,
		PaymentMode: order.PaymentPrepaid,
		TotalAmount: 10,
		Items:       []order.Item{{SKU: "A", Quantity: 1, UnitPrice: 10}},
	}, nil
}

type fakeVendor struct {
	prepareErr  error
	submitErrs  []error // consumed per call; a nil entry means success
	submitCalls int
	submitted   json.RawMessage
	created     bool
}

func (f *fakeVendor) Platform() string { return "vinculum" }
func (f *fakeVendor) PrepareOrder(_ context.Context, o order.Order, route vendor.Route) (json.RawMessage, error) {
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	return json.Marshal(map[string]string{"orderNo": o.ExternalID, "orderLocation": route.VendorReference})
}
func (f *fakeVendor) SubmitOrder(_ context.Context, prepared json.RawMessage, o order.Order, _ vendor.Route) (vendor.OrderAck, error) {
	f.submitCalls++
	f.submitted = prepared
	if f.submitCalls <= len(f.submitErrs) && f.submitErrs[f.submitCalls-1] != nil {
		return vendor.OrderAck{}, f.submitErrs[f.submitCalls-1]
	}
	return vendor.OrderAck{VendorOrderID: "VC-" + o.ExternalID, Created: f.created}, nil
}

// stockOnlyVendor is registered but cannot receive orders, which is the case
// the registry must distinguish from an unknown vendor.
type stockOnlyVendor struct{}

func (stockOnlyVendor) Platform() string { return "feedpartner" }
func (stockOnlyVendor) FetchStock(context.Context, vendor.Route, vendor.StockCursor) (vendor.StockPage, error) {
	return vendor.StockPage{}, nil
}

type fixture struct {
	integ    uuid.UUID
	resolver *routing.MemoryResolver
	origin   *fakeOrigin
	vnd      *fakeVendor
	vendors  *vendor.Registry
	repo     *workflowstate.MemoryRepository
	recorder *intlog.Memory
	exec     *workflow.Executor
	wf       *workflow.Definition
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		integ:    uuid.New(),
		resolver: routing.NewMemoryResolver(),
		origin:   &fakeOrigin{},
		vnd:      &fakeVendor{created: true},
		vendors:  vendor.NewRegistry(),
		repo:     workflowstate.NewMemoryRepository(),
		recorder: &intlog.Memory{},
	}
	if err := f.vendors.Register(f.vnd); err != nil {
		t.Fatalf("register vendor: %v", err)
	}
	f.resolver.Add(routing.Resolution{
		Route:               routing.Route{ID: uuid.New(), IntegrationID: f.integ, Type: "warehouse_id", Value: "12345", DestinationReference: "BLR"},
		IntegrationID:       f.integ,
		IntegrationName:     "easyecom-vinculum",
		SourcePlatform:      "easyecom",
		DestinationPlatform: "vinculum",
	})
	policies := ordersync.DefaultPolicies()
	for _, p := range []*workflow.Policy{&policies.Resolve, &policies.Fetch, &policies.Map, &policies.Submit} {
		p.BaseDelay, p.MaxDelay = time.Millisecond, time.Millisecond
	}
	wf, err := ordersync.New(ordersync.Dependencies{
		Resolver: f.resolver,
		Origins:  map[string]vendor.Origin{"easyecom": f.origin},
		Vendors:  f.vendors,
		Policies: &policies,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.wf = wf
	f.exec = workflow.NewExecutor(f.repo, workflow.WithRecorder(f.recorder), workflow.WithSleep(func(context.Context, time.Duration) error { return nil }))
	return f
}

func (f *fixture) state(warehouse string) *workflow.State {
	ev := event.Event{
		Platform: "easyecom", EventType: event.OrderCreated, CorrelationID: "INT-" + uuid.NewString()[:8],
		IntegrationID: f.integ.String(), ExternalOrderID: "9876543", ReferenceCode: "AMZ-1",
		RoutingKey: event.RoutingKey{Type: "warehouse_id", Value: warehouse}, IdempotencyKey: "k", Payload: []byte(`{"order_id":9876543}`),
	}
	return workflow.NewState(ordersync.Name, ev, "job-1", time.Now())
}

func TestHappyPath(t *testing.T) {
	f := newFixture(t)
	st := f.state("12345")

	if err := f.exec.Run(context.Background(), f.wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != workflow.StatusCompleted {
		t.Fatalf("status = %s (%+v)", st.Status, st.LastError)
	}
	if st.Route == nil || st.Route.DestinationPlatform != "vinculum" || st.Route.DestinationReference != "BLR" {
		t.Fatalf("route = %+v", st.Route)
	}
	if st.Order == nil || st.Order.ExternalID != "9876543" {
		t.Fatalf("order = %+v", st.Order)
	}
	if got := string(st.Payload(ordersync.PayloadVendorOrderRequest)); !strings.Contains(got, `"orderLocation":"BLR"`) {
		t.Fatalf("prepared payload = %s", got)
	}
	if string(f.vnd.submitted) != string(st.Payload(ordersync.PayloadVendorOrderRequest)) {
		t.Fatal("vendor did not receive the prepared payload")
	}
	if st.Result(workflow.ResultDestinationOrderID) != "VC-9876543" || st.Result(ordersync.ResultVendorOrderCreated) != "true" {
		t.Fatalf("results = %v", st.Results)
	}

	var names []string
	for _, e := range f.recorder.Entries() {
		if e.Status == intlog.StatusSuccess && !strings.HasPrefix(e.Action, "WORKFLOW_") {
			names = append(names, e.Action)
		}
	}
	want := "RESOLVE_INTEGRATION,FETCH_ORDER,MAP_ORDER,SUBMIT_VENDOR_ORDER"
	if strings.Join(names, ",") != want {
		t.Fatalf("recorded actions = %v", names)
	}
	for _, e := range f.recorder.Entries() {
		if e.Action == ordersync.ActionSubmitVendorOrder && (e.Integration != "vinculum" || e.ExternalOrderID != "9876543") {
			t.Fatalf("entry lacks integration context: %+v", e)
		}
	}
}

// A vendor that already holds the order reports Created=false. That is the
// idempotent path, not a failure.
func TestExistingVendorOrderIsNotAFailure(t *testing.T) {
	f := newFixture(t)
	f.vnd.created = false
	st := f.state("12345")

	if err := f.exec.Run(context.Background(), f.wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != workflow.StatusCompleted {
		t.Fatalf("status = %s", st.Status)
	}
	if st.Result(ordersync.ResultVendorOrderCreated) != "false" {
		t.Fatalf("results = %v", st.Results)
	}
	if st.Result(workflow.ResultDestinationOrderID) != "VC-9876543" {
		t.Fatalf("vendor order id must still be recorded, got %v", st.Results)
	}
}

func TestUnroutedWarehouseSkips(t *testing.T) {
	f := newFixture(t)
	st := f.state("99999")
	if err := f.exec.Run(context.Background(), f.wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != workflow.StatusSkipped || !strings.Contains(st.SkipReason, "warehouse_id=99999") {
		t.Fatalf("state = %s %q", st.Status, st.SkipReason)
	}
	if f.origin.fetchOrderCalls != 0 || f.vnd.submitCalls != 0 {
		t.Fatal("adapters must not be called for an unrouted event")
	}
}

func TestVendorOutageRetriesThenSucceeds(t *testing.T) {
	f := newFixture(t)
	f.vnd.submitErrs = []error{apperror.New(apperror.ExternalAPI, "503"), apperror.New(apperror.Timeout, "timeout"), nil}
	f.vnd.submitErrs[0].(*apperror.Error).Retryable = true
	st := f.state("12345")

	if err := f.exec.Run(context.Background(), f.wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.vnd.submitCalls != 3 || st.Actions[3].Attempt != 3 || st.Status != workflow.StatusCompleted {
		t.Fatalf("submit calls = %d, attempt = %d, status = %s", f.vnd.submitCalls, st.Actions[3].Attempt, st.Status)
	}
	if f.origin.fetchOrderCalls != 1 {
		t.Fatalf("fetch order replayed %d times", f.origin.fetchOrderCalls)
	}
}

func TestMappingFailureIsPermanent(t *testing.T) {
	f := newFixture(t)
	f.vnd.prepareErr = apperror.New(apperror.Mapping, "order location missing")
	st := f.state("12345")

	err := f.exec.Run(context.Background(), f.wf, st)
	if err == nil || apperror.CategoryOf(err) != apperror.Mapping {
		t.Fatalf("Run err = %v", err)
	}
	if st.Status != workflow.StatusFailed || st.NextAction != ordersync.ActionMapOrder || st.Actions[2].Attempt != 1 || f.vnd.submitCalls != 0 {
		t.Fatalf("state = %+v", st)
	}
}

func TestFailedRunResumesFromFailedAction(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.vnd.submitErrs = []error{
		apperror.New(apperror.Authentication, "token expired"), // permanent on the first run
	}
	st := f.state("12345")
	if err := f.exec.Run(ctx, f.wf, st); err == nil {
		t.Fatal("first run should fail")
	}
	if st.Status != workflow.StatusFailed || st.LastSuccessfulAction != ordersync.ActionMapOrder || st.NextAction != ordersync.ActionSubmitVendorOrder {
		t.Fatalf("state after failure = %+v", st)
	}

	// Operator fixes credentials and resumes: only the failed action and
	// those after it run.
	loaded, err := f.repo.Get(ctx, st.CorrelationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	loaded.ResetCurrentAttempts()
	if err := f.exec.Run(ctx, f.wf, loaded); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if loaded.Status != workflow.StatusCompleted || f.origin.fetchOrderCalls != 1 || f.vnd.submitCalls != 2 || loaded.ResumeCount != 1 {
		t.Fatalf("after resume: status=%s fetches=%d submits=%d resumes=%d", loaded.Status, f.origin.fetchOrderCalls, f.vnd.submitCalls, loaded.ResumeCount)
	}
}

func TestUnknownVendorFails(t *testing.T) {
	f := newFixture(t)
	f.resolver.Add(routing.Resolution{
		Route:               routing.Route{ID: uuid.New(), IntegrationID: f.integ, Type: "warehouse_id", Value: "777", DestinationReference: "XXX"},
		IntegrationID:       f.integ,
		DestinationPlatform: "unknown-erp",
	})
	st := f.state("777")
	err := f.exec.Run(context.Background(), f.wf, st)
	if err == nil || apperror.CategoryOf(err) != apperror.Workflow || !strings.Contains(err.Error(), "no adapter") {
		t.Fatalf("Run err = %v", err)
	}
}

// A vendor that is registered but implements no order role must fail with a
// different message from an unknown vendor: they are different mistakes.
func TestRegisteredVendorWithoutOrderRoleFails(t *testing.T) {
	f := newFixture(t)
	if err := f.vendors.Register(stockOnlyVendor{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	f.resolver.Add(routing.Resolution{
		Route:               routing.Route{ID: uuid.New(), IntegrationID: f.integ, Type: "warehouse_id", Value: "888", DestinationReference: "FP1"},
		IntegrationID:       f.integ,
		DestinationPlatform: "feedpartner",
	})
	st := f.state("888")
	err := f.exec.Run(context.Background(), f.wf, st)
	if err == nil || !strings.Contains(err.Error(), "does not implement") {
		t.Fatalf("Run err = %v", err)
	}
}

// A route without a vendor reference cannot address a location, and must be
// caught before the vendor is called.
func TestRouteWithoutVendorReferenceFails(t *testing.T) {
	f := newFixture(t)
	f.resolver.Add(routing.Resolution{
		Route:               routing.Route{ID: uuid.New(), IntegrationID: f.integ, Type: "warehouse_id", Value: "555"},
		IntegrationID:       f.integ,
		DestinationPlatform: "vinculum",
	})
	st := f.state("555")
	err := f.exec.Run(context.Background(), f.wf, st)
	if err == nil || apperror.CategoryOf(err) != apperror.Workflow {
		t.Fatalf("Run err = %v", err)
	}
	if f.vnd.submitCalls != 0 {
		t.Fatal("vendor must not be called for an incomplete route")
	}
}

func TestResolverOutageIsRetryable(t *testing.T) {
	f := newFixture(t)
	wf, err := ordersync.New(ordersync.Dependencies{
		Resolver: brokenResolver{},
		Origins:  map[string]vendor.Origin{"easyecom": f.origin},
		Vendors:  f.vendors,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := f.state("12345")
	runErr := f.exec.Run(context.Background(), wf, st)
	if runErr == nil || !apperror.IsRetryable(runErr) || st.Actions[0].Attempt != 3 {
		t.Fatalf("err = %v, attempt = %d", runErr, st.Actions[0].Attempt)
	}
}

type brokenResolver struct{}

func (brokenResolver) Resolve(context.Context, uuid.UUID, routing.Key) (*routing.Resolution, error) {
	return nil, errors.New("postgres down")
}

func TestNewValidatesDependencies(t *testing.T) {
	f := newFixture(t)
	cases := []ordersync.Dependencies{
		{},
		{Resolver: f.resolver},
		{Resolver: f.resolver, Origins: map[string]vendor.Origin{"easyecom": f.origin}},
		{Resolver: f.resolver, Origins: map[string]vendor.Origin{"easyecom": f.origin}, Vendors: vendor.NewRegistry()},
	}
	for i, deps := range cases {
		if _, err := ordersync.New(deps); err == nil {
			t.Errorf("case %d: incomplete dependencies accepted", i)
		}
	}
}
