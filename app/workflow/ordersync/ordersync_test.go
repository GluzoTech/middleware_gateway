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
	"github.com/gluzo/integration-gateway/app/domain/inventory"
	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/domain/tracking"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/intlog"
	"github.com/gluzo/integration-gateway/app/routing"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflow/ordersync"
	"github.com/gluzo/integration-gateway/app/workflowstate"
)

// fakeSource and fakeDestination let tests script every adapter call.
type fakeSource struct {
	fetchOrderErr     error
	fetchOrderCalls   int
	inventory         []inventory.Level
	inventoryErr      error
	shipment          *tracking.Shipment
	trackingErr       error
	fetchTrackingCall int
}

func (f *fakeSource) Platform() string { return "easyecom" }
func (f *fakeSource) FetchOrder(_ context.Context, ev event.Event) (order.Order, error) {
	f.fetchOrderCalls++
	if f.fetchOrderErr != nil {
		return order.Order{}, f.fetchOrderErr
	}
	return order.Order{ExternalID: ev.ExternalOrderID, WarehouseID: ev.RoutingKey.Value, PaymentMode: order.PaymentPrepaid, TotalAmount: 10, Items: []order.Item{{SKU: "A", Quantity: 1, UnitPrice: 10}}}, nil
}
func (f *fakeSource) FetchInventory(context.Context, order.Order) ([]inventory.Level, error) {
	return f.inventory, f.inventoryErr
}
func (f *fakeSource) FetchTracking(context.Context, order.Order) (*tracking.Shipment, error) {
	f.fetchTrackingCall++
	return f.shipment, f.trackingErr
}

type fakeDestination struct {
	prepareErr   error
	submitErrs   []error // consumed per call; nil entry means success
	submitCalls  int
	submitted    json.RawMessage
	inventoryRes ordersync.InventoryResult
	inventoryErr error
}

func (f *fakeDestination) Platform() string { return "dabur" }
func (f *fakeDestination) PrepareOrder(_ context.Context, o order.Order, route workflow.RouteInfo) (json.RawMessage, error) {
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	return json.Marshal(map[string]string{"code": o.ExternalID, "facility": route.DestinationReference})
}
func (f *fakeDestination) SubmitOrder(_ context.Context, prepared json.RawMessage, o order.Order, _ workflow.RouteInfo) (ordersync.OrderResult, error) {
	f.submitCalls++
	f.submitted = prepared
	if f.submitCalls <= len(f.submitErrs) && f.submitErrs[f.submitCalls-1] != nil {
		return ordersync.OrderResult{}, f.submitErrs[f.submitCalls-1]
	}
	return ordersync.OrderResult{DestinationOrderID: "SO-" + o.ExternalID, Created: true}, nil
}
func (f *fakeDestination) UpdateInventory(context.Context, []inventory.Level, workflow.RouteInfo) (ordersync.InventoryResult, error) {
	return f.inventoryRes, f.inventoryErr
}

type fixture struct {
	integ    uuid.UUID
	resolver *routing.MemoryResolver
	src      *fakeSource
	dst      *fakeDestination
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
		src:      &fakeSource{},
		dst:      &fakeDestination{},
		repo:     workflowstate.NewMemoryRepository(),
		recorder: &intlog.Memory{},
	}
	f.resolver.Add(routing.Resolution{
		Route:               routing.Route{ID: uuid.New(), IntegrationID: f.integ, Type: "warehouse_id", Value: "12345", DestinationReference: "DABUR-DEL"},
		IntegrationID:       f.integ,
		IntegrationName:     "easyecom-dabur",
		SourcePlatform:      "easyecom",
		DestinationPlatform: "dabur",
	})
	policies := ordersync.DefaultPolicies()
	for _, p := range []*workflow.Policy{&policies.Resolve, &policies.Fetch, &policies.Submit, &policies.Inventory, &policies.Tracking} {
		p.BaseDelay, p.MaxDelay = time.Millisecond, time.Millisecond
	}
	wf, err := ordersync.New(ordersync.Dependencies{
		Resolver:     f.resolver,
		Sources:      map[string]ordersync.Source{"easyecom": f.src},
		Destinations: map[string]ordersync.Destination{"dabur": f.dst},
		Policies:     &policies,
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
	f.src.inventory = []inventory.Level{{SKU: "A", WarehouseID: "12345", Available: 7}}
	f.dst.inventoryRes = ordersync.InventoryResult{Updated: 1}
	f.src.shipment = &tracking.Shipment{OrderExternalID: "9876543", TrackingNumber: "AWB1", Carrier: "Delhivery"}
	st := f.state("12345")

	if err := f.exec.Run(context.Background(), f.wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != workflow.StatusCompleted {
		t.Fatalf("status = %s (%+v)", st.Status, st.LastError)
	}
	if st.Route == nil || st.Route.DestinationPlatform != "dabur" || st.Route.DestinationReference != "DABUR-DEL" {
		t.Fatalf("route = %+v", st.Route)
	}
	if st.Order == nil || st.Order.ExternalID != "9876543" {
		t.Fatalf("order = %+v", st.Order)
	}
	if got := string(st.Payload(ordersync.PayloadDestinationOrderRequest)); !strings.Contains(got, `"facility":"DABUR-DEL"`) {
		t.Fatalf("prepared payload = %s", got)
	}
	if string(f.dst.submitted) != string(st.Payload(ordersync.PayloadDestinationOrderRequest)) {
		t.Fatal("destination did not receive the prepared payload")
	}
	if st.Result(workflow.ResultDestinationOrderID) != "SO-9876543" || st.Result(ordersync.ResultDestinationOrderCreated) != "true" {
		t.Fatalf("results = %v", st.Results)
	}
	if len(st.Inventory) != 1 || st.Result(ordersync.ResultInventoryUpdated) != "1" {
		t.Fatalf("inventory = %+v results %v", st.Inventory, st.Results)
	}
	if st.Tracking == nil || st.Result(ordersync.ResultTrackingNumber) != "AWB1" {
		t.Fatalf("tracking = %+v", st.Tracking)
	}
	names := []string{}
	for _, e := range f.recorder.Entries() {
		if e.Status == intlog.StatusSuccess && !strings.HasPrefix(e.Action, "WORKFLOW_") {
			names = append(names, e.Action)
		}
	}
	want := "RESOLVE_INTEGRATION,FETCH_ORDER,MAP_ORDER,UPDATE_DESTINATION_ORDER,FETCH_INVENTORY,UPDATE_INVENTORY,FETCH_TRACKING"
	if strings.Join(names, ",") != want {
		t.Fatalf("recorded actions = %v", names)
	}
	for _, e := range f.recorder.Entries() {
		if e.Action == "UPDATE_DESTINATION_ORDER" && (e.Integration != "dabur" || e.ExternalOrderID != "9876543") {
			t.Fatalf("entry lacks integration context: %+v", e)
		}
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
	if f.src.fetchOrderCalls != 0 || f.dst.submitCalls != 0 {
		t.Fatal("adapters must not be called for an unrouted event")
	}
}

func TestDestinationOutageRetriesThenSucceeds(t *testing.T) {
	f := newFixture(t)
	f.dst.submitErrs = []error{apperror.New(apperror.ExternalAPI, "503"), apperror.New(apperror.Timeout, "timeout"), nil}
	f.dst.submitErrs[0].(*apperror.Error).Retryable = true
	st := f.state("12345")

	if err := f.exec.Run(context.Background(), f.wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.dst.submitCalls != 3 || st.Actions[3].Attempt != 3 || st.Status != workflow.StatusCompleted {
		t.Fatalf("submit calls = %d, attempt = %d, status = %s", f.dst.submitCalls, st.Actions[3].Attempt, st.Status)
	}
	if f.src.fetchOrderCalls != 1 {
		t.Fatalf("fetch order replayed %d times", f.src.fetchOrderCalls)
	}
}

func TestMappingFailureIsPermanent(t *testing.T) {
	f := newFixture(t)
	f.dst.prepareErr = apperror.New(apperror.Mapping, "facility code missing")
	st := f.state("12345")

	err := f.exec.Run(context.Background(), f.wf, st)
	if err == nil || apperror.CategoryOf(err) != apperror.Mapping {
		t.Fatalf("Run err = %v", err)
	}
	if st.Status != workflow.StatusFailed || st.NextAction != ordersync.ActionMapOrder || st.Actions[2].Attempt != 1 || f.dst.submitCalls != 0 {
		t.Fatalf("state = %+v", st)
	}
}

func TestFailedRunResumesFromFailedAction(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.dst.submitErrs = []error{
		apperror.New(apperror.Authentication, "token expired"), // permanent on first run
	}
	st := f.state("12345")
	if err := f.exec.Run(ctx, f.wf, st); err == nil {
		t.Fatal("first run should fail")
	}
	if st.Status != workflow.StatusFailed || st.LastSuccessfulAction != ordersync.ActionMapOrder || st.NextAction != ordersync.ActionUpdateDestinationOrder {
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
	if loaded.Status != workflow.StatusCompleted || f.src.fetchOrderCalls != 1 || f.dst.submitCalls != 2 || loaded.ResumeCount != 1 {
		t.Fatalf("after resume: status=%s fetches=%d submits=%d resumes=%d", loaded.Status, f.src.fetchOrderCalls, f.dst.submitCalls, loaded.ResumeCount)
	}
}

func TestInventoryAndTrackingAreBestEffort(t *testing.T) {
	f := newFixture(t)
	f.src.inventoryErr = apperror.New(apperror.ExternalAPI, "inventory api down")
	f.src.trackingErr = apperror.New(apperror.ExternalAPI, "no tracking")
	st := f.state("12345")

	if err := f.exec.Run(context.Background(), f.wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != workflow.StatusCompleted {
		t.Fatalf("status = %s", st.Status)
	}
	if st.Actions[4].Status != workflow.ActionSkipped || st.Actions[5].Status != workflow.ActionSucceeded || st.Actions[6].Status != workflow.ActionSkipped {
		t.Fatalf("actions = %+v", st.Actions[4:])
	}
	if st.Result(ordersync.ResultInventoryUpdated) != "0" {
		t.Fatalf("inventory result = %v", st.Results)
	}
}

func TestNoShipmentYetIsNotAFailure(t *testing.T) {
	f := newFixture(t)
	st := f.state("12345")
	if err := f.exec.Run(context.Background(), f.wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Actions[6].Status != workflow.ActionSucceeded || st.Tracking != nil {
		t.Fatalf("tracking action = %+v", st.Actions[6])
	}
}

func TestUnknownDestinationAdapterFails(t *testing.T) {
	f := newFixture(t)
	f.resolver.Add(routing.Resolution{
		Route:               routing.Route{ID: uuid.New(), IntegrationID: f.integ, Type: "warehouse_id", Value: "777"},
		IntegrationID:       f.integ,
		DestinationPlatform: "unknown-erp",
	})
	st := f.state("777")
	err := f.exec.Run(context.Background(), f.wf, st)
	if err == nil || apperror.CategoryOf(err) != apperror.Workflow || !strings.Contains(err.Error(), "no adapter") {
		t.Fatalf("Run err = %v", err)
	}
}

func TestResolverOutageIsRetryable(t *testing.T) {
	f := newFixture(t)
	wf, _ := ordersync.New(ordersync.Dependencies{
		Resolver:     brokenResolver{},
		Sources:      map[string]ordersync.Source{"easyecom": f.src},
		Destinations: map[string]ordersync.Destination{"dabur": f.dst},
	})
	st := f.state("12345")
	err := f.exec.Run(context.Background(), wf, st)
	if err == nil || !apperror.IsRetryable(err) || st.Actions[0].Attempt != 3 {
		t.Fatalf("err = %v, attempt = %d", err, st.Actions[0].Attempt)
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
		{Resolver: f.resolver, Sources: map[string]ordersync.Source{"easyecom": f.src}},
	}
	for i, deps := range cases {
		if _, err := ordersync.New(deps); err == nil {
			t.Errorf("case %d: incomplete dependencies accepted", i)
		}
	}
}
