// Package vendor defines what a fulfilment partner is, in Gluzo's own terms.
//
// A vendor is a party that holds stock Gluzo sells but does not own, accepts
// orders for it, and ships it to the customer directly. BCPL, running
// Vinculum eRetail, is the first. The contracts here describe what such a
// partner can do, not how any particular platform does it: an adapter under
// app/integrations/<platform> implements the roles its platform supports and
// registers once.
//
// The contracts are split by role rather than gathered into one large
// interface. A partner that receives orders but publishes stock through a
// feed rather than an API implements OrderReceiver and not StockProvider,
// and the registry reports that honestly instead of obliging the adapter to
// declare a method it cannot serve.
//
// Direction is fixed by the role, and that is the reason this package
// exists. Because the vendor owns the stock and owns dispatch, stock and
// shipment data flow *from* the vendor *into* Gluzo's OMS. That is the
// opposite of a model where Gluzo owns the stock and pushes it outward, and
// conflating the two produces adapters whose method names lie about which
// way data moves.
//
// Nothing here imports the workflow engine, the queue or any platform
// package. Workflows depend on this; this depends only on the domain.
package vendor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/domain/inventory"
	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/domain/tracking"
	"github.com/gluzo/integration-gateway/app/event"
)

// Route is the resolved configuration for one pipeline: which vendor, and
// which of that vendor's locations. It is a copy of what routing resolved,
// passed by value so that adapters cannot reach back into the workflow
// engine.
type Route struct {
	// IntegrationID and IntegrationName identify the configured pipeline.
	IntegrationID   string
	IntegrationName string
	// OriginPlatform is the OMS the order came from (today: easyecom).
	OriginPlatform string
	// VendorPlatform is the fulfilment partner (today: vinculum).
	VendorPlatform string
	// RouteType and RouteValue are the attribute that selected this route,
	// e.g. warehouse_id = 12345.
	RouteType  string
	RouteValue string
	// VendorReference is the vendor-side identifier for this route: for
	// Vinculum the three-character orderLocation code, for another platform
	// whatever names a location there. Always configuration, never code.
	VendorReference string
	// OriginReference is the origin-side identifier for this route: for
	// EasyEcom the location_key whose JWT scopes stock writes to the
	// vendor's own warehouse. Empty means the process default.
	OriginReference string
}

// Validate reports whether the route can address a vendor.
func (r Route) Validate() error {
	var errs []error
	if strings.TrimSpace(r.VendorPlatform) == "" {
		errs = append(errs, errors.New("route has no vendor platform"))
	}
	if strings.TrimSpace(r.VendorReference) == "" {
		errs = append(errs, errors.New("route has no vendor reference (location)"))
	}
	return errors.Join(errs...)
}

// Vendor is the minimum every fulfilment partner adapter provides. The name
// matches the platforms table, the routing configuration and the execution
// log.
type Vendor interface {
	Platform() string
}

// OrderAck is what a vendor reports after accepting an order.
type OrderAck struct {
	// VendorOrderID is the vendor's identifier for the order.
	VendorOrderID string
	// Created is false when the order already existed and was left as is. A
	// vendor that cannot distinguish the two reports true only when it is
	// certain it created the order.
	Created bool
}

// OrderReceiver is a vendor that accepts orders for fulfilment.
//
// Mapping and submission are separate so that a mapping defect and a vendor
// outage are distinguishable in the execution log and retried differently: a
// bad map will never succeed, an outage usually will.
type OrderReceiver interface {
	Vendor

	// PrepareOrder maps a domain order to the vendor's request document. It
	// must be pure: no network, no database. Failures are permanent.
	PrepareOrder(ctx context.Context, o order.Order, route Route) (json.RawMessage, error)

	// SubmitOrder sends a document produced by PrepareOrder.
	//
	// It must be idempotent. A resumed run, a redelivered webhook or a
	// retried attempt will call this more than once for the same order, and
	// an order that already exists at the vendor is reported with
	// Created=false rather than raised as an error. The usual mechanism is
	// to carry Gluzo's own order identifier as the vendor's order number so
	// that the vendor itself rejects the duplicate.
	SubmitOrder(ctx context.Context, prepared json.RawMessage, o order.Order, route Route) (OrderAck, error)
}

// StockCursor marks how far a stock pull has progressed. Adapters interpret
// the fields their platform supports and ignore the rest; a zero cursor
// means "everything".
type StockCursor struct {
	// Since restricts the pull to entries changed at or after this time,
	// where the platform offers a modified-since filter.
	Since time.Time
	// Page is the next page to request, zero-based. Platforms that page by
	// token instead carry it in Token.
	Page  int
	Token string
}

// StockPage is one page of a vendor's stock position.
type StockPage struct {
	// Levels are already expressed in Gluzo's terms: Available is the
	// sellable quantity the storefront may publish, with the vendor's own
	// bucket filtering and committed-quantity arithmetic already applied.
	Levels []inventory.Level
	// Next is the cursor for the following page; HasMore reports whether one
	// exists.
	Next    StockCursor
	HasMore bool
}

// StockProvider is a vendor that owns the stock for its SKUs.
//
// This direction is what distinguishes a dropship vendor from a warehouse
// Gluzo controls: the gateway reads stock here and writes it into Gluzo's
// own OMS, never the reverse. No part of the gateway may write vendor stock
// back to the vendor.
type StockProvider interface {
	Vendor

	// FetchStock returns one page of current stock for the route's location.
	// Callers follow StockPage.Next while HasMore is true.
	FetchStock(ctx context.Context, route Route, cursor StockCursor) (StockPage, error)
}

// Window bounds a shipment pull.
type Window struct {
	From time.Time
	To   time.Time
	// Page is the next page to request, zero-based.
	Page int
	// OrderIDs restricts the pull to specific orders, for reconciling a
	// known-stuck order rather than sweeping a period.
	OrderIDs []string
}

// ShipmentPage is one page of dispatch information.
type ShipmentPage struct {
	Shipments []tracking.Shipment
	HasMore   bool
	NextPage  int
}

// FulfilmentProvider is a vendor that ships the goods and therefore owns the
// dispatch record: the AWB, the carrier, the invoice and the delivery status.
//
// A vendor may also push these to the gateway where its platform supports
// callbacks. That does not replace this contract: a pushed event lost during
// a deployment leaves an order with no tracking forever, whereas a scheduled
// sweep recovers by itself. Push is a latency improvement layered on top.
type FulfilmentProvider interface {
	Vendor

	// FetchShipments returns dispatch information for orders that changed
	// within the window.
	FetchShipments(ctx context.Context, route Route, w Window) (ShipmentPage, error)
}

// Origin is the platform a customer order originates from: Gluzo's OMS,
// today EasyEcom. It is the customer-facing record of the order and holds
// the replica of vendor stock.
type Origin interface {
	Platform() string

	// FetchOrder returns the full order behind an intake event.
	FetchOrder(ctx context.Context, ev event.Event) (order.Order, error)
}

// StockResult summarises a stock push into the origin platform.
type StockResult struct {
	Updated int
	Failed  int
	// Skipped counts SKUs whose quantity was unchanged since the last push.
	Skipped int
}

// StockSink is an origin platform that accepts vendor stock.
//
// Implementations treat the quantity as authoritative and replace what they
// hold. They must not add, subtract or otherwise reconcile it against their
// own view: the vendor is the source of truth for these SKUs, and an origin
// that applies its own arithmetic on top produces drift no one can trace.
type StockSink interface {
	Platform() string

	// PushStock writes levels for the route's vendor location.
	//
	// Route.OriginReference selects the origin-side location. Where the
	// origin scopes credentials per location, as EasyEcom does, the
	// implementation authenticates for that location so that a defect
	// cannot write quantities into a location the route does not name.
	PushStock(ctx context.Context, route Route, levels []inventory.Level) (StockResult, error)
}

// ShipmentUpdate is what an origin platform is told about a dispatch.
//
// It carries more than the shipment because two things the origin needs are
// not properties of the parcel. The carrier reference is the origin's own
// identifier for the carrier, which only configuration can supply; and
// whether the status advances is a judgement against what was pushed before,
// which needs durable state a platform adapter must not own.
type ShipmentUpdate struct {
	Shipment tracking.Shipment

	// CarrierReference is the origin platform's identifier for the carrier,
	// resolved from configuration. Empty where the origin does not use one.
	CarrierReference string
	// CarrierName is the name to present, where it differs from the
	// vendor's spelling.
	CarrierName string

	// AdvanceStatus is false when only the dispatch details changed. A late
	// arrival may correct a tracking number, and doing so must not roll a
	// delivered order back to shipped.
	AdvanceStatus bool
}

// ShipmentSink is an origin platform that accepts dispatch information for an
// externally shipped order.
type ShipmentSink interface {
	Platform() string

	// PushShipment records the shipment against the origin's order.
	//
	// A shipment's status must never move backwards. A "delivered" notice
	// can arrive before the "shipped" one that preceded it, because the
	// gateway polls on a schedule; the caller decides whether an update
	// advances the status and says so in the update.
	PushShipment(ctx context.Context, route Route, u ShipmentUpdate) error
}
