// Package stocksync defines the STOCK_SYNC workflow: a vendor's stock
// position is read, translated into Gluzo's terms and written into the origin
// platform.
//
// It runs on a schedule rather than per order, and it runs in that direction
// rather than the other, because under dropship the vendor owns the stock.
// Coupling it to an order would leave stock stale for any SKU that happened
// not to sell, and pushing Gluzo's view outward would overwrite the only
// authoritative copy.
//
// The workflow speaks the domain model and the vendor roles only. Buckets,
// committed quantities and item codes are the adapter's and the SKU map's
// business; a workflow that understood them would have to be rewritten for
// the next vendor.
package stocksync

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
	"github.com/gluzo/integration-gateway/app/inventorystate"
	"github.com/gluzo/integration-gateway/app/skumap"
	"github.com/gluzo/integration-gateway/app/vendor"
	"github.com/gluzo/integration-gateway/app/workflow"
)

// Name is the workflow's registry name.
const Name = "STOCK_SYNC"

// Action names, in execution order.
const (
	ActionFetchVendorStock = "FETCH_VENDOR_STOCK"
	ActionMapStock         = "MAP_STOCK"
	ActionPushStock        = "PUSH_STOCK"
)

// State keys.
const (
	ResultVendorRows   = "vendor_stock_rows"
	ResultMappedSKUs   = "mapped_skus"
	ResultChangedSKUs  = "changed_skus"
	ResultPushedSKUs   = "pushed_skus"
	ResultFailedSKUs   = "failed_skus"
	ResultUnchanged    = "unchanged_skus"
	ResultFullPush     = "full_push"
	PayloadSweepIntent = "sweep_intent"
)

// MaxPages bounds one sweep. A vendor that keeps claiming another page would
// otherwise hold the action open until its timeout, once per attempt.
const MaxPages = 500

// Sweep is the job payload: which location to read, and whether to ignore
// what was last pushed.
type Sweep struct {
	IntegrationID   string `json:"integration_id"`
	IntegrationName string `json:"integration_name"`
	OriginPlatform  string `json:"origin_platform"`
	VendorPlatform  string `json:"vendor_platform"`
	VendorReference string `json:"vendor_reference"`
	OriginReference string `json:"origin_reference"`

	// Full ignores inventory_state and pushes every SKU.
	//
	// This is the repair path, and it is why inventory_state may be treated
	// as a cache rather than a source of truth: a run that wrote the
	// platform and failed before recording the write leaves the two
	// disagreeing, and only a push that ignores the record can notice.
	Full bool `json:"full,omitempty"`

	// Since bounds an incremental read where the vendor supports one. Zero
	// reads the whole catalogue.
	Since time.Time `json:"since,omitempty"`
}

// Validate reports whether the sweep addresses a location.
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

// DefaultPolicies retries remote calls and never retries pure mapping.
//
// The fetch timeout is generous because a sweep pages through a catalogue.
// The push timeout is generous for the same reason and because it batches.
func DefaultPolicies() Policies {
	return Policies{
		Fetch: workflow.Policy{MaxAttempts: 3, Timeout: 2 * time.Minute, BaseDelay: 5 * time.Second, MaxDelay: 30 * time.Second},
		Map:   workflow.NoRetry(),
		Push:  workflow.Policy{MaxAttempts: 3, Timeout: 2 * time.Minute, BaseDelay: 5 * time.Second, MaxDelay: 30 * time.Second},
	}
}

// Dependencies wires the workflow.
type Dependencies struct {
	// Vendors supplies the StockProvider role. A vendor registered without
	// it is reported as missing that role rather than as unknown.
	Vendors *vendor.Registry
	// Sinks holds the origin adapters that accept vendor stock, by platform.
	Sinks map[string]vendor.StockSink
	// SKUs translates the vendor's item codes into Gluzo's.
	SKUs skumap.Reader
	// Pushed remembers what was last written, so a sweep sends only changes.
	Pushed   inventorystate.Store
	Policies *Policies
	Logger   *slog.Logger
}

// New builds the STOCK_SYNC workflow definition.
func New(deps Dependencies) (*workflow.Definition, error) {
	switch {
	case deps.Vendors == nil:
		return nil, errors.New("stocksync: vendor registry is required")
	case len(deps.Sinks) == 0:
		return nil, errors.New("stocksync: at least one stock sink is required")
	case deps.SKUs == nil:
		return nil, errors.New("stocksync: a SKU map reader is required")
	case deps.Pushed == nil:
		return nil, errors.New("stocksync: an inventory state store is required")
	}
	policies := DefaultPolicies()
	if deps.Policies != nil {
		policies = *deps.Policies
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	w := &stockSync{deps: deps}

	return workflow.NewDefinition(Name,
		workflow.Step{Action: workflow.NewAction(ActionFetchVendorStock, w.fetchVendorStock), Policy: policies.Fetch},
		workflow.Step{Action: workflow.NewAction(ActionMapStock, w.mapStock), Policy: policies.Map},
		workflow.Step{Action: workflow.NewAction(ActionPushStock, w.pushStock), Policy: policies.Push},
	), nil
}

type stockSync struct {
	deps Dependencies
}

// sweep reads the job payload. It is decoded per action rather than held on
// the struct because actions are resumed independently after a crash, and a
// field set by a previous process is not there when the next one starts.
func (w *stockSync) sweep(state *workflow.State) (Sweep, error) {
	var s Sweep
	if err := json.Unmarshal(state.Event.Payload, &s); err != nil {
		return Sweep{}, apperror.Wrap(apperror.Validation, "stock sweep payload is not readable", err)
	}
	if err := s.Validate(); err != nil {
		return Sweep{}, apperror.Wrap(apperror.Validation, "stock sweep payload is incomplete", err)
	}
	return s, nil
}

// fetchVendorStock reads every page of the vendor's stock for the location.
//
// The whole catalogue is read before anything is pushed. A page-at-a-time
// push would be interruptible halfway, leaving the storefront holding a
// mixture of two sweeps — and with no record of which SKUs belonged to which.
func (w *stockSync) fetchVendorStock(ctx context.Context, state *workflow.State) error {
	sweep, err := w.sweep(state)
	if err != nil {
		return err
	}
	route := sweep.Route()

	// Recorded on the state so the admin viewer can show which pipeline a
	// sweep belonged to, exactly as it does for an order.
	state.Route = &workflow.RouteInfo{
		IntegrationID:        sweep.IntegrationID,
		IntegrationName:      sweep.IntegrationName,
		SourcePlatform:       sweep.OriginPlatform,
		DestinationPlatform:  sweep.VendorPlatform,
		DestinationReference: sweep.VendorReference,
		OriginReference:      sweep.OriginReference,
	}

	provider, err := w.deps.Vendors.StockProvider(route.VendorPlatform)
	if err != nil {
		return apperror.Wrap(apperror.Workflow, "vendor cannot provide stock", err)
	}

	var levels []inventory.Level
	cursor := vendor.StockCursor{Since: sweep.Since}
	for page := 0; page < MaxPages; page++ {
		result, err := provider.FetchStock(ctx, route, cursor)
		if err != nil {
			return err
		}
		levels = append(levels, result.Levels...)
		if !result.HasMore {
			state.Inventory = levels
			state.SetResult(ResultVendorRows, fmt.Sprint(len(levels)))
			return nil
		}
		cursor = result.Next
	}
	return apperror.New(apperror.ExternalAPI,
		fmt.Sprintf("vendor %s reported more than %d pages of stock for location %s",
			route.VendorPlatform, MaxPages, route.VendorReference))
}

// mapStock translates vendor item codes into Gluzo's, applies each mapping's
// safety buffer, and drops SKUs whose quantity has not changed.
//
// Pure apart from two reads of configuration, and never retried: a mapping
// failure is a missing row in the SKU map, and no amount of retrying will add
// it.
func (w *stockSync) mapStock(ctx context.Context, state *workflow.State) error {
	sweep, err := w.sweep(state)
	if err != nil {
		return err
	}
	integrationID, err := uuid.Parse(sweep.IntegrationID)
	if err != nil {
		return apperror.Wrap(apperror.Validation, "sweep carries an invalid integration id", err)
	}

	mappings, err := w.deps.SKUs.Active(ctx, integrationID)
	if err != nil {
		return apperror.Wrap(apperror.Internal, "load sku map", err)
	}
	index := skumap.NewIndex(mappings)

	entries := make([]inventorystate.Entry, 0, len(state.Inventory))
	var unmapped []string
	for _, level := range state.Inventory {
		mapping, err := index.ToGluzo(level.SKU)
		if err != nil {
			// Collected rather than returned at the first one: an operator
			// fixing the SKU map wants the whole list, not one SKU per
			// failed run.
			unmapped = append(unmapped, level.SKU)
			continue
		}
		entries = append(entries, inventorystate.Entry{
			GluzoSKU: mapping.GluzoSKU,
			Quantity: mapping.Publishable(level.Available),
		})
	}

	if len(unmapped) > 0 {
		// Never a skip. A SKU dropped silently publishes nothing, and a
		// storefront showing no update looks exactly like one that is up to
		// date.
		e := apperror.New(apperror.Mapping, fmt.Sprintf(
			"%d vendor sku(s) have no active mapping for integration %s: %s",
			len(unmapped), sweep.IntegrationName, summarise(unmapped)))
		e.Integration = sweep.VendorPlatform
		e.Operation = ActionMapStock
		return e
	}
	state.SetResult(ResultMappedSKUs, fmt.Sprint(len(entries)))

	key := inventorystate.Key{IntegrationID: integrationID, OriginReference: sweep.OriginReference}
	changed := entries
	if sweep.Full {
		state.SetResult(ResultFullPush, "true")
	} else {
		last, err := w.deps.Pushed.Load(ctx, key)
		if err != nil {
			return apperror.Wrap(apperror.Internal, "load pushed inventory state", err)
		}
		changed = inventorystate.Changed(last, entries)
	}
	state.SetResult(ResultChangedSKUs, fmt.Sprint(len(changed)))
	state.SetResult(ResultUnchanged, fmt.Sprint(len(entries)-len(changed)))

	state.Inventory = toLevels(changed)
	return nil
}

// pushStock writes the changed quantities into the origin platform and
// records what was sent.
func (w *stockSync) pushStock(ctx context.Context, state *workflow.State) error {
	sweep, err := w.sweep(state)
	if err != nil {
		return err
	}
	if len(state.Inventory) == 0 {
		// Nothing moved. This is the ordinary outcome of a frequent sweep
		// and is not worth a write.
		state.SetResult(ResultPushedSKUs, "0")
		return nil
	}

	sink, ok := w.deps.Sinks[sweep.OriginPlatform]
	if !ok {
		return apperror.New(apperror.Workflow,
			fmt.Sprintf("no stock sink for origin platform %q", sweep.OriginPlatform))
	}

	result, err := sink.PushStock(ctx, sweep.Route(), state.Inventory)
	state.SetResult(ResultPushedSKUs, fmt.Sprint(result.Updated))
	state.SetResult(ResultFailedSKUs, fmt.Sprint(result.Failed))
	if err != nil {
		return err
	}

	integrationID, err := uuid.Parse(sweep.IntegrationID)
	if err != nil {
		return apperror.Wrap(apperror.Validation, "sweep carries an invalid integration id", err)
	}
	key := inventorystate.Key{IntegrationID: integrationID, OriginReference: sweep.OriginReference}

	if err := w.deps.Pushed.Record(ctx, key, toEntries(state.Inventory)); err != nil {
		// The platform has the quantities; only our record of them failed.
		// Failing the action would re-push on the next attempt, which is
		// harmless because the quantities are absolute, so this is reported
		// rather than swallowed.
		return apperror.Wrap(apperror.Internal, "record pushed inventory state", err)
	}

	if result.Failed > 0 {
		w.deps.Logger.WarnContext(ctx, "some stock updates were rejected",
			slog.String("integration", sweep.IntegrationName),
			slog.Int("updated", result.Updated),
			slog.Int("failed", result.Failed))
	}
	return nil
}

func toLevels(entries []inventorystate.Entry) []inventory.Level {
	out := make([]inventory.Level, 0, len(entries))
	for _, e := range entries {
		out = append(out, inventory.Level{SKU: e.GluzoSKU, Available: e.Quantity})
	}
	return out
}

func toEntries(levels []inventory.Level) []inventorystate.Entry {
	out := make([]inventorystate.Entry, 0, len(levels))
	for _, l := range levels {
		out = append(out, inventorystate.Entry{GluzoSKU: l.SKU, Quantity: l.Available})
	}
	return out
}

// summarise lists the first few SKUs so the error is readable in a log line
// while still naming enough of them to act on.
func summarise(skus []string) string {
	const max = 10
	if len(skus) <= max {
		return join(skus)
	}
	return join(skus[:max]) + fmt.Sprintf(" (and %d more)", len(skus)-max)
}

func join(skus []string) string {
	out := ""
	for i, s := range skus {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
