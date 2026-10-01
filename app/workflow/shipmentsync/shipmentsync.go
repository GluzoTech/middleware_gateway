// Package shipmentsync defines the SHIPMENT_SYNC workflow: dispatch records
// are read from the vendor, translated, and pushed into the origin platform
// so the customer sees tracking.
//
// It runs on a schedule and in that direction because under dropship the
// vendor books the courier. The origin platform holds no tracking until this
// workflow puts it there, which is also why reading it back would tell us
// nothing we did not already know.
//
// Out-of-order arrival is normal, not an error. The gateway polls, so a sweep
// can read "delivered" before it has ever read "shipped", and a redelivered
// event is expected rather than exceptional.
package shipmentsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/couriermap"
	"github.com/gluzo/integration-gateway/app/domain/tracking"
	"github.com/gluzo/integration-gateway/app/shipmentstate"
	"github.com/gluzo/integration-gateway/app/vendor"
	"github.com/gluzo/integration-gateway/app/workflow"
)

// Name is the workflow's registry name.
const Name = "SHIPMENT_SYNC"

// Action names, in execution order.
const (
	ActionFetchShipments = "FETCH_SHIPMENTS"
	ActionMapShipments   = "MAP_SHIPMENTS"
	ActionPushShipment   = "PUSH_SHIPMENT"
)

// State keys.
const (
	ResultVendorRecords   = "vendor_shipment_records"
	ResultMappedShipments = "mapped_shipments"
	ResultPushedShipments = "pushed_shipments"
	ResultSkippedNoChange = "unchanged_shipments"
	ResultFailedShipments = "failed_shipments"
	PayloadShipments      = "shipments"
)

// MaxPages bounds one sweep, as the stock sweep is bounded: hasMore is the
// vendor's claim, not ours.
const MaxPages = 200

// Sweep is the job payload: which location to read, over which period.
type Sweep struct {
	IntegrationID   string `json:"integration_id"`
	IntegrationName string `json:"integration_name"`
	OriginPlatform  string `json:"origin_platform"`
	VendorPlatform  string `json:"vendor_platform"`
	VendorReference string `json:"vendor_reference"`
	OriginReference string `json:"origin_reference"`

	// From and To bound the read. Unlike a stock sweep, which reads a
	// current position, a dispatch sweep reads a period: a missed run is a
	// real gap, and the watermark is what closes it.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`

	// OrderIDs restricts the sweep to specific orders, for reconciling a
	// known-stuck order rather than sweeping a period.
	OrderIDs []string `json:"order_ids,omitempty"`
}

// Validate reports whether the sweep addresses a location and a period.
func (s Sweep) Validate() error {
	var errs []error
	if s.IntegrationID == "" {
		errs = append(errs, errors.New("integration id is required"))
	}
	if s.VendorPlatform == "" {
		errs = append(errs, errors.New("vendor platform is required"))
	}
	if s.VendorReference == "" {
		errs = append(errs, errors.New("vendor reference is required"))
	}
	if s.OriginPlatform == "" {
		errs = append(errs, errors.New("origin platform is required"))
	}
	if len(s.OrderIDs) == 0 && (s.From.IsZero() || s.To.IsZero()) {
		errs = append(errs, errors.New("either order ids or a complete period is required"))
	}
	return errors.Join(errs...)
}

// Route renders the sweep as the value adapters see.
func (s Sweep) Route() vendor.Route {
	return vendor.Route{
		IntegrationID:   s.IntegrationID,
		IntegrationName: s.IntegrationName,
		OriginPlatform:  s.OriginPlatform,
		VendorPlatform:  s.VendorPlatform,
		VendorReference: s.VendorReference,
		OriginReference: s.OriginReference,
	}
}

// Policies lets deployments tune retry behaviour per action.
type Policies struct {
	Fetch workflow.Policy
	Map   workflow.Policy
	Push  workflow.Policy
}

// DefaultPolicies retries remote calls and gives mapping a small budget
// because it reads the courier map.
func DefaultPolicies() Policies {
	return Policies{
		Fetch: workflow.Policy{MaxAttempts: 3, Timeout: 2 * time.Minute, BaseDelay: 5 * time.Second, MaxDelay: 30 * time.Second},
		Map:   workflow.Policy{MaxAttempts: 3, Timeout: 15 * time.Second, BaseDelay: time.Second, MaxDelay: 5 * time.Second},
		Push:  workflow.Policy{MaxAttempts: 3, Timeout: 2 * time.Minute, BaseDelay: 5 * time.Second, MaxDelay: 30 * time.Second},
	}
}

// Dependencies wires the workflow.
type Dependencies struct {
	Vendors *vendor.Registry
	// Sinks holds the origin adapters that accept dispatch information.
	Sinks map[string]vendor.ShipmentSink
	// Couriers translates the vendor's carrier names into the origin's own
	// carrier identifiers.
	Couriers couriermap.Reader
	// Pushed remembers the furthest state pushed per package, which is what
	// makes "never move a status backwards" decidable.
	Pushed   shipmentstate.Store
	Policies *Policies
	Logger   *slog.Logger
}

// New builds the SHIPMENT_SYNC workflow definition.
func New(deps Dependencies) (*workflow.Definition, error) {
	switch {
	case deps.Vendors == nil:
		return nil, errors.New("shipmentsync: vendor registry is required")
	case len(deps.Sinks) == 0:
		return nil, errors.New("shipmentsync: at least one shipment sink is required")
	case deps.Couriers == nil:
		return nil, errors.New("shipmentsync: a courier map reader is required")
	case deps.Pushed == nil:
		return nil, errors.New("shipmentsync: a shipment state store is required")
	}
	policies := DefaultPolicies()
	if deps.Policies != nil {
		policies = *deps.Policies
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	w := &shipmentSync{deps: deps}

	return workflow.NewDefinition(Name,
		workflow.Step{Action: workflow.NewAction(ActionFetchShipments, w.fetchShipments), Policy: policies.Fetch},
		workflow.Step{Action: workflow.NewAction(ActionMapShipments, w.mapShipments), Policy: policies.Map},
		workflow.Step{Action: workflow.NewAction(ActionPushShipment, w.pushShipments), Policy: policies.Push},
	), nil
}

type shipmentSync struct {
	deps Dependencies
}

func (w *shipmentSync) sweep(state *workflow.State) (Sweep, error) {
	var s Sweep
	if err := json.Unmarshal(state.Event.Payload, &s); err != nil {
		return Sweep{}, apperror.Wrap(apperror.Validation, "shipment sweep payload is not readable", err)
	}
	if err := s.Validate(); err != nil {
		return Sweep{}, apperror.Wrap(apperror.Validation, "shipment sweep payload is incomplete", err)
	}
	return s, nil
}

// fetchShipments reads every page of dispatch records for the period.
func (w *shipmentSync) fetchShipments(ctx context.Context, state *workflow.State) error {
	sweep, err := w.sweep(state)
	if err != nil {
		return err
	}
	route := sweep.Route()

	state.Route = &workflow.RouteInfo{
		IntegrationID:        sweep.IntegrationID,
		IntegrationName:      sweep.IntegrationName,
		SourcePlatform:       sweep.OriginPlatform,
		DestinationPlatform:  sweep.VendorPlatform,
		DestinationReference: sweep.VendorReference,
		OriginReference:      sweep.OriginReference,
	}

	provider, err := w.deps.Vendors.FulfilmentProvider(route.VendorPlatform)
	if err != nil {
		return apperror.Wrap(apperror.Workflow, "vendor cannot provide dispatch records", err)
	}

	var shipments []tracking.Shipment
	window := vendor.Window{From: sweep.From, To: sweep.To, OrderIDs: sweep.OrderIDs}
	for page := 0; page < MaxPages; page++ {
		window.Page = page
		result, err := provider.FetchShipments(ctx, route, window)
		if err != nil {
			return err
		}
		shipments = append(shipments, result.Shipments...)
		if !result.HasMore {
			return w.storeShipments(state, shipments)
		}
	}
	return apperror.New(apperror.ExternalAPI,
		fmt.Sprintf("vendor %s reported more than %d pages of dispatch records for location %s",
			route.VendorPlatform, MaxPages, route.VendorReference))
}

func (w *shipmentSync) storeShipments(state *workflow.State, shipments []tracking.Shipment) error {
	encoded, err := json.Marshal(shipments)
	if err != nil {
		return apperror.Wrap(apperror.Internal, "encode shipments", err)
	}
	state.SetPayload(PayloadShipments, encoded)
	state.SetResult(ResultVendorRecords, fmt.Sprint(len(shipments)))
	return nil
}

func (w *shipmentSync) loadShipments(state *workflow.State) ([]tracking.Shipment, error) {
	raw := state.Payload(PayloadShipments)
	if len(raw) == 0 {
		return nil, nil
	}
	var shipments []tracking.Shipment
	if err := json.Unmarshal(raw, &shipments); err != nil {
		return nil, apperror.Wrap(apperror.Internal, "decode shipments", err)
	}
	return shipments, nil
}

// update pairs a shipment with everything the sink needs to apply it.
type update struct {
	Shipment         tracking.Shipment `json:"shipment"`
	CarrierReference string            `json:"carrier_reference"`
	CarrierName      string            `json:"carrier_name"`
	AdvanceStatus    bool              `json:"advance_status"`
	PackageCode      string            `json:"package_code"`
}

// mapShipments resolves carriers and decides, per package, whether anything
// should be pushed at all.
//
// The decision is made here rather than in the sink because it needs durable
// state, and a platform adapter must not own that. It is also what makes a
// redelivered sweep a no-op: a record identical to the one already pushed
// produces no call.
func (w *shipmentSync) mapShipments(ctx context.Context, state *workflow.State) error {
	sweep, err := w.sweep(state)
	if err != nil {
		return err
	}
	integrationID, err := uuid.Parse(sweep.IntegrationID)
	if err != nil {
		return apperror.Wrap(apperror.Validation, "sweep carries an invalid integration id", err)
	}
	shipments, err := w.loadShipments(state)
	if err != nil {
		return err
	}

	couriers, err := w.deps.Couriers.Active(ctx, integrationID)
	if err != nil {
		return apperror.Wrap(apperror.Internal, "load courier map", err)
	}
	index := couriermap.NewIndex(couriers)

	updates := make([]update, 0, len(shipments))
	var unmapped []string
	unchanged := 0

	for _, sh := range shipments {
		key := shipmentstate.Key{
			IntegrationID:   integrationID,
			OrderExternalID: sh.OrderExternalID,
			PackageCode:     sh.PackageCode,
		}
		previous, found, err := w.deps.Pushed.Get(ctx, key)
		if err != nil {
			return apperror.Wrap(apperror.Internal, "load shipment state", err)
		}

		decision := shipmentstate.Decide(previous, found, sh)
		if !decision.Push {
			unchanged++
			continue
		}

		u := update{Shipment: sh, AdvanceStatus: decision.AdvanceStatus, PackageCode: sh.PackageCode}
		if carrier := sh.Carrier; carrier != "" {
			courier, err := index.Lookup(carrier)
			if err != nil {
				unmapped = append(unmapped, carrier)
				continue
			}
			u.CarrierReference = courier.CompanyCarrierID
			u.CarrierName = courier.PresentedName()
		}
		updates = append(updates, u)
	}

	if len(unmapped) > 0 {
		// Never a skip, for the same reason an unmapped SKU is not: a
		// dispatch quietly dropped leaves a customer with no tracking and
		// nothing in the log saying why.
		e := apperror.New(apperror.Mapping, fmt.Sprintf(
			"%d carrier(s) have no active mapping for integration %s: %s",
			len(unmapped), sweep.IntegrationName, distinct(unmapped)))
		e.Integration = sweep.VendorPlatform
		e.Operation = ActionMapShipments
		return e
	}

	encoded, err := json.Marshal(updates)
	if err != nil {
		return apperror.Wrap(apperror.Internal, "encode shipment updates", err)
	}
	state.SetPayload(PayloadShipments, encoded)
	state.SetResult(ResultMappedShipments, fmt.Sprint(len(updates)))
	state.SetResult(ResultSkippedNoChange, fmt.Sprint(unchanged))
	return nil
}

// pushShipments writes each update into the origin platform and records what
// was sent.
//
// One shipment's failure does not abandon the rest: a sweep covers many
// orders, and one order whose carrier the platform rejects must not cost
// every other customer their tracking. Failures are counted and the action
// fails at the end, so the run is visibly not clean and is retried.
func (w *shipmentSync) pushShipments(ctx context.Context, state *workflow.State) error {
	sweep, err := w.sweep(state)
	if err != nil {
		return err
	}
	integrationID, err := uuid.Parse(sweep.IntegrationID)
	if err != nil {
		return apperror.Wrap(apperror.Validation, "sweep carries an invalid integration id", err)
	}

	var updates []update
	if raw := state.Payload(PayloadShipments); len(raw) > 0 {
		if err := json.Unmarshal(raw, &updates); err != nil {
			return apperror.Wrap(apperror.Internal, "decode shipment updates", err)
		}
	}
	if len(updates) == 0 {
		state.SetResult(ResultPushedShipments, "0")
		return nil
	}

	sink, ok := w.deps.Sinks[sweep.OriginPlatform]
	if !ok {
		return apperror.New(apperror.Workflow,
			fmt.Sprintf("no shipment sink for origin platform %q", sweep.OriginPlatform))
	}
	route := sweep.Route()

	pushed, failed := 0, 0
	var lastErr error
	for _, u := range updates {
		err := sink.PushShipment(ctx, route, vendor.ShipmentUpdate{
			Shipment:         u.Shipment,
			CarrierReference: u.CarrierReference,
			CarrierName:      u.CarrierName,
			AdvanceStatus:    u.AdvanceStatus,
		})
		if err != nil {
			failed++
			lastErr = err
			w.deps.Logger.WarnContext(ctx, "shipment push failed",
				slog.String("order", u.Shipment.OrderExternalID),
				slog.String("error", err.Error()))
			continue
		}
		pushed++

		key := shipmentstate.Key{
			IntegrationID:   integrationID,
			OrderExternalID: u.Shipment.OrderExternalID,
			PackageCode:     u.PackageCode,
		}
		previous, _, err := w.deps.Pushed.Get(ctx, key)
		if err != nil {
			return apperror.Wrap(apperror.Internal, "reload shipment state", err)
		}
		record := shipmentstate.FromShipment(previous, u.Shipment, u.AdvanceStatus, u.Shipment.UpdatedAt)
		if err := w.deps.Pushed.Record(ctx, key, record); err != nil {
			// The platform has the dispatch; only our record failed. Left
			// unrecorded it would be pushed again next sweep, which is
			// harmless, so this is reported rather than swallowed.
			return apperror.Wrap(apperror.Internal, "record shipment state", err)
		}
	}

	state.SetResult(ResultPushedShipments, fmt.Sprint(pushed))
	state.SetResult(ResultFailedShipments, fmt.Sprint(failed))
	if failed > 0 {
		return lastErr
	}
	return nil
}

// distinct lists carrier names once each, so an operator reading the error
// sees the set to register rather than one entry per affected order.
func distinct(values []string) string {
	seen := make(map[string]bool, len(values))
	out := ""
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		if out != "" {
			out += ", "
		}
		out += v
	}
	return out
}
