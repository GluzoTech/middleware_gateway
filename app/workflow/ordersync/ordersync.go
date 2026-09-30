// Package ordersync defines the ORDER_SYNC workflow: an order event from the
// origin platform is routed, fetched, mapped and submitted to the fulfilment
// vendor.
//
// The workflow speaks the domain model only. Platform adapters implement the
// roles in app/vendor; nothing here knows about EasyEcom or Vinculum
// payloads, which is what lets a second vendor be added by writing an
// adapter and a route.
//
// Stock and dispatch are deliberately not part of this workflow. A dropship
// vendor owns both, so they flow from the vendor into the origin platform on
// their own schedule (STOCK_SYNC, SHIPMENT_SYNC) rather than once per order.
// Coupling them to an order would mean a customer order's success depended
// on a stock sweep succeeding, and would leave stock stale for any SKU that
// happened not to sell.
package ordersync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/routing"
	"github.com/gluzo/integration-gateway/app/vendor"
	"github.com/gluzo/integration-gateway/app/workflow"
)

// Name is the workflow's registry name.
const Name = "ORDER_SYNC"

// Action names, in execution order.
const (
	ActionResolveIntegration = "RESOLVE_INTEGRATION"
	ActionFetchOrder         = "FETCH_ORDER"
	ActionMapOrder           = "MAP_ORDER"
	ActionSubmitVendorOrder  = "SUBMIT_VENDOR_ORDER"
)

// State keys.
const (
	PayloadVendorOrderRequest = "vendor_order_request"
	ResultVendorOrderCreated  = "vendor_order_created"
)

// Policies lets deployments tune retry behaviour per action.
type Policies struct {
	Resolve workflow.Policy
	Fetch   workflow.Policy
	Map     workflow.Policy
	Submit  workflow.Policy
}

// DefaultPolicies retries transient failures on remote calls and never
// retries pure mapping.
func DefaultPolicies() Policies {
	return Policies{
		Resolve: workflow.Policy{MaxAttempts: 3, Timeout: 10 * time.Second, BaseDelay: time.Second, MaxDelay: 5 * time.Second},
		Fetch:   workflow.DefaultPolicy(),
		Map:     workflow.NoRetry(),
		Submit:  workflow.Policy{MaxAttempts: 5, Timeout: 45 * time.Second, BaseDelay: 2 * time.Second, MaxDelay: 30 * time.Second},
	}
}

// Dependencies wires the workflow.
type Dependencies struct {
	Resolver routing.Resolver
	// Origins holds the OMS adapters, by platform name.
	Origins map[string]vendor.Origin
	// Vendors holds the fulfilment partner adapters. ORDER_SYNC needs the
	// OrderReceiver role; a vendor registered without it is rejected during
	// routing rather than at submission.
	Vendors  *vendor.Registry
	Policies *Policies
	Logger   *slog.Logger
}

// New builds the ORDER_SYNC workflow definition.
func New(deps Dependencies) (*workflow.Definition, error) {
	if deps.Resolver == nil {
		return nil, errors.New("ordersync: resolver is required")
	}
	if len(deps.Origins) == 0 {
		return nil, errors.New("ordersync: at least one origin adapter is required")
	}
	if deps.Vendors == nil {
		return nil, errors.New("ordersync: vendor registry is required")
	}
	if len(deps.Vendors.Platforms()) == 0 {
		return nil, errors.New("ordersync: at least one vendor adapter must be registered")
	}
	policies := DefaultPolicies()
	if deps.Policies != nil {
		policies = *deps.Policies
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	w := &orderSync{deps: deps}

	return workflow.NewDefinition(Name,
		workflow.Step{Action: workflow.NewAction(ActionResolveIntegration, w.resolveIntegration), Policy: policies.Resolve},
		workflow.Step{Action: workflow.NewAction(ActionFetchOrder, w.fetchOrder), Policy: policies.Fetch},
		workflow.Step{Action: workflow.NewAction(ActionMapOrder, w.mapOrder), Policy: policies.Map},
		workflow.Step{Action: workflow.NewAction(ActionSubmitVendorOrder, w.submitVendorOrder), Policy: policies.Submit},
	), nil
}

type orderSync struct {
	deps Dependencies
}

func (w *orderSync) origin(state *workflow.State) (vendor.Origin, error) {
	src, ok := w.deps.Origins[state.Platform]
	if !ok {
		return nil, apperror.New(apperror.Workflow, fmt.Sprintf("no origin adapter for platform %q", state.Platform))
	}
	return src, nil
}

// route converts the persisted routing outcome into the value adapters see.
// workflow.RouteInfo belongs to the engine and is serialised into workflow
// state; vendor.Route is the adapter-facing contract. Converting here keeps
// app/vendor free of any dependency on the engine.
func (w *orderSync) route(state *workflow.State) (vendor.OrderReceiver, vendor.Route, error) {
	if state.Route == nil {
		return nil, vendor.Route{}, apperror.New(apperror.Workflow, "route has not been resolved")
	}
	r := vendor.Route{
		IntegrationID:   state.Route.IntegrationID,
		IntegrationName: state.Route.IntegrationName,
		OriginPlatform:  state.Route.SourcePlatform,
		VendorPlatform:  state.Route.DestinationPlatform,
		RouteType:       state.Route.RouteType,
		RouteValue:      state.Route.RouteValue,
		VendorReference: state.Route.DestinationReference,
		OriginReference: state.Route.OriginReference,
	}
	if err := r.Validate(); err != nil {
		return nil, vendor.Route{}, apperror.Wrap(apperror.Workflow, "resolved route is incomplete", err)
	}
	receiver, err := w.deps.Vendors.OrderReceiver(r.VendorPlatform)
	if err != nil {
		return nil, vendor.Route{}, apperror.Wrap(apperror.Workflow, "vendor cannot receive orders", err)
	}
	return receiver, r, nil
}

// resolveIntegration looks up the route for the event's routing key within
// the authenticated integration. No route means the event is not configured
// for synchronisation and the workflow ends as SKIPPED.
func (w *orderSync) resolveIntegration(ctx context.Context, state *workflow.State) error {
	integrationID, err := uuid.Parse(state.IntegrationID)
	if err != nil {
		return apperror.Wrap(apperror.Workflow, "event carries an invalid integration id", err)
	}
	key := routing.Key{Type: state.Event.RoutingKey.Type, Value: state.Event.RoutingKey.Value}
	res, err := w.deps.Resolver.Resolve(ctx, integrationID, key)
	if errors.Is(err, routing.ErrNoRoute) {
		return workflow.Skip(fmt.Sprintf("no active route for %s in integration %s", key, state.IntegrationID))
	}
	if errors.Is(err, routing.ErrInvalidArgument) {
		return apperror.Wrap(apperror.Validation, "invalid routing key", err)
	}
	if err != nil {
		return apperror.Wrap(apperror.Network, "resolve route", err)
	}
	if _, rerr := w.deps.Vendors.OrderReceiver(res.DestinationPlatform); rerr != nil {
		return apperror.Wrap(apperror.Workflow, "route resolved to a vendor that cannot receive orders", rerr)
	}
	state.Route = &workflow.RouteInfo{
		IntegrationID:        res.IntegrationID.String(),
		IntegrationName:      res.IntegrationName,
		SourcePlatform:       res.SourcePlatform,
		DestinationPlatform:  res.DestinationPlatform,
		RouteType:            res.Route.Type,
		RouteValue:           res.Route.Value,
		DestinationReference: res.Route.DestinationReference,
		OriginReference:      res.Route.OriginReference,
	}
	return nil
}

func (w *orderSync) fetchOrder(ctx context.Context, state *workflow.State) error {
	src, err := w.origin(state)
	if err != nil {
		return err
	}
	o, err := src.FetchOrder(ctx, state.Event)
	if err != nil {
		return err
	}
	if err := o.Validate(); err != nil {
		return apperror.Wrap(apperror.Validation, "fetched order is invalid", err)
	}
	state.Order = &o
	return nil
}

func (w *orderSync) mapOrder(ctx context.Context, state *workflow.State) error {
	if state.Order == nil {
		return apperror.New(apperror.Workflow, "order has not been fetched")
	}
	receiver, route, err := w.route(state)
	if err != nil {
		return err
	}
	doc, err := receiver.PrepareOrder(ctx, *state.Order, route)
	if err != nil {
		return err
	}
	if len(doc) == 0 {
		return apperror.New(apperror.Mapping, "vendor produced an empty order document")
	}
	state.SetPayload(PayloadVendorOrderRequest, doc)
	return nil
}

func (w *orderSync) submitVendorOrder(ctx context.Context, state *workflow.State) error {
	if state.Order == nil {
		return apperror.New(apperror.Workflow, "order has not been fetched")
	}
	receiver, route, err := w.route(state)
	if err != nil {
		return err
	}
	prepared := state.Payload(PayloadVendorOrderRequest)
	if len(prepared) == 0 {
		return apperror.New(apperror.Workflow, "order has not been mapped")
	}
	ack, err := receiver.SubmitOrder(ctx, prepared, *state.Order, route)
	if err != nil {
		return err
	}
	if ack.VendorOrderID == "" {
		return apperror.New(apperror.ExternalAPI, "vendor accepted the order without returning an identifier")
	}
	state.SetResult(workflow.ResultDestinationOrderID, ack.VendorOrderID)
	state.SetResult(ResultVendorOrderCreated, fmt.Sprint(ack.Created))
	if !ack.Created {
		w.deps.Logger.InfoContext(ctx, "vendor already held this order; no duplicate created",
			slog.String("external_order_id", state.Event.ExternalOrderID),
			slog.String("vendor_order_id", ack.VendorOrderID),
			slog.String("correlation_id", state.CorrelationID),
		)
	}
	return nil
}
