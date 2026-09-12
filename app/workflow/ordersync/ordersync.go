// Package ordersync defines the ORDER_SYNC workflow: an order event from a
// source platform is routed, fetched, mapped and pushed to the destination
// platform, followed by inventory and tracking synchronisation.
//
// The workflow speaks the domain model only. Platform adapters implement
// Source and Destination; nothing here knows about EasyEcom or Uniware
// payloads, which is what lets a second destination be added by writing an
// adapter and a route.
package ordersync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/inventory"
	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/domain/tracking"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/routing"
	"github.com/gluzo/integration-gateway/app/workflow"
)

// Name is the workflow's registry name.
const Name = "ORDER_SYNC"

// Action names, in execution order.
const (
	ActionResolveIntegration     = "RESOLVE_INTEGRATION"
	ActionFetchOrder             = "FETCH_ORDER"
	ActionMapOrder               = "MAP_ORDER"
	ActionUpdateDestinationOrder = "UPDATE_DESTINATION_ORDER"
	ActionFetchInventory         = "FETCH_INVENTORY"
	ActionUpdateInventory        = "UPDATE_INVENTORY"
	ActionFetchTracking          = "FETCH_TRACKING"
)

// State keys.
const (
	PayloadDestinationOrderRequest = "destination_order_request"
	ResultDestinationOrderCreated  = "destination_order_created"
	ResultInventoryUpdated         = "inventory_updated"
	ResultInventoryFailed          = "inventory_failed"
	ResultTrackingNumber           = "tracking_number"
)

// Source is what a source platform adapter provides.
type Source interface {
	Platform() string
	// FetchOrder returns the full order behind ev as a domain order.
	FetchOrder(ctx context.Context, ev event.Event) (order.Order, error)
	// FetchInventory returns current stock levels for the order's SKUs.
	FetchInventory(ctx context.Context, o order.Order) ([]inventory.Level, error)
	// FetchTracking returns the order's shipment, or nil when none exists yet.
	FetchTracking(ctx context.Context, o order.Order) (*tracking.Shipment, error)
}

// OrderResult is what a destination reports after accepting an order.
type OrderResult struct {
	// DestinationOrderID is the destination's identifier for the order.
	DestinationOrderID string
	// Created is false when the order already existed and was left as is.
	Created bool
}

// InventoryResult summarises an inventory push.
type InventoryResult struct {
	Updated int
	Failed  int
}

// Destination is what a destination platform adapter provides.
type Destination interface {
	Platform() string
	// PrepareOrder maps the domain order to the destination's request
	// document. It is pure: mapping failures are non-retryable.
	PrepareOrder(ctx context.Context, o order.Order, route workflow.RouteInfo) (json.RawMessage, error)
	// SubmitOrder sends a prepared document. It must be idempotent: an order
	// that already exists is reported with Created=false, not as an error.
	SubmitOrder(ctx context.Context, prepared json.RawMessage, o order.Order, route workflow.RouteInfo) (OrderResult, error)
	// UpdateInventory pushes stock levels to the destination.
	UpdateInventory(ctx context.Context, levels []inventory.Level, route workflow.RouteInfo) (InventoryResult, error)
}

// Policies lets deployments tune retry behaviour per action.
type Policies struct {
	Resolve   workflow.Policy
	Fetch     workflow.Policy
	Map       workflow.Policy
	Submit    workflow.Policy
	Inventory workflow.Policy
	Tracking  workflow.Policy
}

// DefaultPolicies retries transient failures on remote calls, never retries
// pure mapping, and treats inventory and tracking as best effort.
func DefaultPolicies() Policies {
	remote := workflow.DefaultPolicy()
	optional := workflow.DefaultPolicy()
	optional.Optional = true
	tracking := workflow.Policy{MaxAttempts: 2, Timeout: 20 * time.Second, BaseDelay: 2 * time.Second, MaxDelay: 10 * time.Second, Optional: true}
	return Policies{
		Resolve:   workflow.Policy{MaxAttempts: 3, Timeout: 10 * time.Second, BaseDelay: time.Second, MaxDelay: 5 * time.Second},
		Fetch:     remote,
		Map:       workflow.NoRetry(),
		Submit:    workflow.Policy{MaxAttempts: 5, Timeout: 45 * time.Second, BaseDelay: 2 * time.Second, MaxDelay: 30 * time.Second},
		Inventory: optional,
		Tracking:  tracking,
	}
}

// Dependencies wires the workflow.
type Dependencies struct {
	Resolver     routing.Resolver
	Sources      map[string]Source      // by source platform name
	Destinations map[string]Destination // by destination platform name
	Policies     *Policies
	Logger       *slog.Logger
}

// New builds the ORDER_SYNC workflow definition.
func New(deps Dependencies) (*workflow.Definition, error) {
	if deps.Resolver == nil {
		return nil, errors.New("ordersync: resolver is required")
	}
	if len(deps.Sources) == 0 {
		return nil, errors.New("ordersync: at least one source adapter is required")
	}
	if len(deps.Destinations) == 0 {
		return nil, errors.New("ordersync: at least one destination adapter is required")
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
		workflow.Step{Action: workflow.NewAction(ActionUpdateDestinationOrder, w.updateDestinationOrder), Policy: policies.Submit},
		workflow.Step{Action: workflow.NewAction(ActionFetchInventory, w.fetchInventory), Policy: policies.Inventory},
		workflow.Step{Action: workflow.NewAction(ActionUpdateInventory, w.updateInventory), Policy: policies.Inventory},
		workflow.Step{Action: workflow.NewAction(ActionFetchTracking, w.fetchTracking), Policy: policies.Tracking},
	), nil
}

type orderSync struct {
	deps Dependencies
}

func (w *orderSync) source(state *workflow.State) (Source, error) {
	src, ok := w.deps.Sources[state.Platform]
	if !ok {
		return nil, apperror.New(apperror.Workflow, fmt.Sprintf("no source adapter for platform %q", state.Platform))
	}
	return src, nil
}

func (w *orderSync) destination(state *workflow.State) (Destination, workflow.RouteInfo, error) {
	if state.Route == nil {
		return nil, workflow.RouteInfo{}, apperror.New(apperror.Workflow, "route has not been resolved")
	}
	dst, ok := w.deps.Destinations[state.Route.DestinationPlatform]
	if !ok {
		return nil, workflow.RouteInfo{}, apperror.New(apperror.Workflow, fmt.Sprintf("no destination adapter for platform %q", state.Route.DestinationPlatform))
	}
	return dst, *state.Route, nil
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
	if _, ok := w.deps.Destinations[res.DestinationPlatform]; !ok {
		return apperror.New(apperror.Workflow, fmt.Sprintf("route resolved to destination %q but no adapter is configured", res.DestinationPlatform))
	}
	state.Route = &workflow.RouteInfo{
		IntegrationID:        res.IntegrationID.String(),
		IntegrationName:      res.IntegrationName,
		SourcePlatform:       res.SourcePlatform,
		DestinationPlatform:  res.DestinationPlatform,
		RouteType:            res.Route.Type,
		RouteValue:           res.Route.Value,
		DestinationReference: res.Route.DestinationReference,
	}
	return nil
}

func (w *orderSync) fetchOrder(ctx context.Context, state *workflow.State) error {
	src, err := w.source(state)
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
	dst, route, err := w.destination(state)
	if err != nil {
		return err
	}
	doc, err := dst.PrepareOrder(ctx, *state.Order, route)
	if err != nil {
		return err
	}
	if len(doc) == 0 {
		return apperror.New(apperror.Mapping, "destination produced an empty order document")
	}
	state.SetPayload(PayloadDestinationOrderRequest, doc)
	return nil
}

func (w *orderSync) updateDestinationOrder(ctx context.Context, state *workflow.State) error {
	if state.Order == nil {
		return apperror.New(apperror.Workflow, "order has not been fetched")
	}
	dst, route, err := w.destination(state)
	if err != nil {
		return err
	}
	prepared := state.Payload(PayloadDestinationOrderRequest)
	if len(prepared) == 0 {
		return apperror.New(apperror.Workflow, "order has not been mapped")
	}
	res, err := dst.SubmitOrder(ctx, prepared, *state.Order, route)
	if err != nil {
		return err
	}
	if res.DestinationOrderID == "" {
		return apperror.New(apperror.ExternalAPI, "destination accepted the order without returning an identifier")
	}
	state.SetResult(workflow.ResultDestinationOrderID, res.DestinationOrderID)
	state.SetResult(ResultDestinationOrderCreated, fmt.Sprint(res.Created))
	return nil
}

func (w *orderSync) fetchInventory(ctx context.Context, state *workflow.State) error {
	if state.Order == nil {
		return apperror.New(apperror.Workflow, "order has not been fetched")
	}
	src, err := w.source(state)
	if err != nil {
		return err
	}
	levels, err := src.FetchInventory(ctx, *state.Order)
	if err != nil {
		return err
	}
	state.Inventory = levels
	return nil
}

func (w *orderSync) updateInventory(ctx context.Context, state *workflow.State) error {
	if len(state.Inventory) == 0 {
		state.SetResult(ResultInventoryUpdated, "0")
		return nil
	}
	dst, route, err := w.destination(state)
	if err != nil {
		return err
	}
	res, err := dst.UpdateInventory(ctx, state.Inventory, route)
	if err != nil {
		return err
	}
	state.SetResult(ResultInventoryUpdated, fmt.Sprint(res.Updated))
	state.SetResult(ResultInventoryFailed, fmt.Sprint(res.Failed))
	return nil
}

func (w *orderSync) fetchTracking(ctx context.Context, state *workflow.State) error {
	if state.Order == nil {
		return apperror.New(apperror.Workflow, "order has not been fetched")
	}
	src, err := w.source(state)
	if err != nil {
		return err
	}
	shipment, err := src.FetchTracking(ctx, *state.Order)
	if err != nil {
		return err
	}
	if shipment == nil {
		w.deps.Logger.InfoContext(ctx, "no shipment yet for order",
			slog.String("external_order_id", state.Event.ExternalOrderID),
			slog.String("correlation_id", state.CorrelationID),
		)
		return nil
	}
	state.Tracking = shipment
	state.SetResult(ResultTrackingNumber, shipment.TrackingNumber)
	return nil
}
