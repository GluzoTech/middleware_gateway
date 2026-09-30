package vinculum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/order"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/inventory"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/order"
	"github.com/gluzo/integration-gateway/app/integrations/vinculum/mapper"
	"github.com/gluzo/integration-gateway/app/vendor"
)

// Vendor is BCPL as a fulfilment partner: the adapter that implements the
// roles in app/vendor on top of the Vinculum client.
//
// StockProvider and OrderReceiver are implemented. The registry discovers
// each by type assertion, so asking this vendor for dispatch records reports
// a missing role rather than an unknown vendor — a different mistake,
// diagnosed differently. FulfilmentProvider arrives in Phase 6.
type Vendor struct {
	client *Client
	logger *slog.Logger
	now    func() time.Time
	// vendorName is stamped on order lines where the deployment uses it.
	vendorName string
}

// WithVendorName sets the value stamped on each order line's `vendor` field.
func WithVendorName(name string) VendorOption {
	return func(v *Vendor) { v.vendorName = strings.TrimSpace(name) }
}

// VendorOption configures a Vendor.
type VendorOption func(*Vendor)

// WithClock replaces the clock used to stamp stock levels; intended for tests.
func WithClock(fn func() time.Time) VendorOption {
	return func(v *Vendor) {
		if fn != nil {
			v.now = fn
		}
	}
}

// NewVendor wraps a client.
//
// A configured sellable bucket is required. Reading every bucket is harmless
// and is what the read-only phase did; *publishing* every bucket would put
// damaged, quarantined and in-transit stock on the storefront as sellable.
// The distinction is invisible in the data — every bucket looks like a
// quantity — so it is enforced here rather than left to be noticed.
//
// TODO(VERIFY): which value is sellable is BCPL open item 2. Failing at
// start-up is the point: an unset bucket stops the deployment instead of
// overselling quietly.
func NewVendor(client *Client, logger *slog.Logger, opts ...VendorOption) (*Vendor, error) {
	if client == nil {
		return nil, errors.New("vinculum: client is required")
	}
	if client.SellableBucket() == "" {
		return nil, errors.New("vinculum: VINCULUM_SELLABLE_BUCKET is required before stock may be published; " +
			"an unset bucket would publish every bucket, including damaged and in-transit stock, as sellable")
	}
	if logger == nil {
		logger = slog.Default()
	}
	v := &Vendor{client: client, logger: logger, now: time.Now}
	for _, opt := range opts {
		opt(v)
	}
	return v, nil
}

// Platform implements vendor.Vendor.
func (v *Vendor) Platform() string { return PlatformName }

// FetchStock implements vendor.StockProvider.
//
// The page returned is already in Gluzo's terms, as the role contract
// requires: the sellable bucket is filtered and the committed-quantity rule
// applied before it leaves this package. Buckets and committed quantities are
// Vinculum's concepts, and a workflow that had to understand them would have
// to be rewritten for the next vendor.
//
// SKU codes are still the vendor's. Translating them needs the integration's
// SKU map, which is configuration this adapter has no business loading.
func (v *Vendor) FetchStock(ctx context.Context, route vendor.Route, cursor vendor.StockCursor) (vendor.StockPage, error) {
	if err := route.Validate(); err != nil {
		return vendor.StockPage{}, apperror.Wrap(apperror.Validation, "incomplete route", err)
	}

	// vendor.StockCursor pages from zero; Vinculum's pageNumber starts at
	// one. Converting here keeps the off-by-one in one place.
	page := cursor.Page + 1

	resp, err := v.client.GetWhInventory(ctx, dtoinventory.GetWhInventoryRequest{
		LocCode:    route.VendorReference,
		Buckets:    v.client.SellableBucket(),
		PageNumber: page,
		FromDate:   cursor.Since,
	})
	if err != nil {
		return vendor.StockPage{}, err
	}

	levels, summary := mapper.ToDomainStock(resp.Response, mapper.StockOptions{
		SellableBucket: v.client.SellableBucket(),
		ObservedAt:     v.now().UTC(),
	})
	if summary.Skipped > 0 {
		// Filtered rows are ordinary — another bucket. Skipped rows are
		// malformed, and a silent malformed row is a SKU that quietly stops
		// updating.
		v.logger.WarnContext(ctx, "vinculum stock rows could not be mapped",
			slog.String("location", route.VendorReference),
			slog.Int("skipped", summary.Skipped),
			slog.Int("page", page))
	}

	return vendor.StockPage{
		Levels: levels,
		Next: vendor.StockCursor{
			Since: cursor.Since,
			Page:  cursor.Page + 1,
		},
		// An empty page ends the sweep whatever hasMore claims: following a
		// hasMore that never goes false would loop forever.
		HasMore: resp.HasMore.Bool() && len(resp.Response) > 0,
	}, nil
}

// PrepareOrder implements vendor.OrderReceiver.
//
// Pure, as the role contract requires: no network, no database, no clock. Its
// failures are permanent, because the same order will map the same way next
// time.
//
// The item SKUs must already be the vendor's; the workflow translates them
// against the integration's SKU map before calling, because loading that map
// is a database read a pure mapper cannot do.
func (v *Vendor) PrepareOrder(_ context.Context, o order.Order, route vendor.Route) (json.RawMessage, error) {
	if err := route.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.Validation, "incomplete route", err)
	}
	req, err := mapper.ToCreateOrder(o, route, mapper.OrderOptions{VendorName: v.vendorName})
	if err != nil {
		return nil, err
	}
	doc, err := json.Marshal(req)
	if err != nil {
		return nil, apperror.Wrap(apperror.Mapping, "encode vinculum order", err)
	}
	return doc, nil
}

// SubmitOrder implements vendor.OrderReceiver.
//
// Idempotent by construction rather than by bookkeeping: the order number
// sent is Gluzo's own order id, so Vinculum rejects a second submission of
// the same order and that rejection is reported as Created=false. A resumed
// run, a redelivered webhook and a retried attempt therefore all converge on
// one order at the vendor.
//
// The prepared document is sent exactly as it was mapped. Re-mapping here
// would let a resumed run submit something different from what the log says
// was prepared.
func (v *Vendor) SubmitOrder(ctx context.Context, prepared json.RawMessage, o order.Order, route vendor.Route) (vendor.OrderAck, error) {
	if err := route.Validate(); err != nil {
		return vendor.OrderAck{}, apperror.Wrap(apperror.Validation, "incomplete route", err)
	}
	var req dtoorder.CreateOrderRequest
	if err := json.Unmarshal(prepared, &req); err != nil {
		return vendor.OrderAck{}, apperror.Wrap(apperror.Mapping, "prepared vinculum order is not readable", err)
	}

	resp, err := v.client.CreateOrder(ctx, req)
	if err != nil {
		if ack, ok := v.recogniseDuplicate(ctx, err, req); ok {
			return ack, nil
		}
		return vendor.OrderAck{}, err
	}

	id := resp.VendorOrderNo()
	if id == "" {
		// Vinculum reported success without echoing an identifier. The
		// order is known by the number we sent, which is Gluzo's own order
		// id, so that is the honest answer rather than an error.
		id = req.OrderNo
	}
	return vendor.OrderAck{VendorOrderID: id, Created: true}, nil
}

// recogniseDuplicate turns Vinculum's rejection of an order it already holds
// into a successful acknowledgement.
//
// Only an envelope rejection is considered. A transport failure or a 5xx says
// nothing about whether the order exists, and treating one as a duplicate
// would report an order as accepted that the vendor never saw.
func (v *Vendor) recogniseDuplicate(ctx context.Context, err error, req dtoorder.CreateOrderRequest) (vendor.OrderAck, bool) {
	var aerr *apperror.Error
	if !errors.As(err, &aerr) || aerr.Operation != "CreateOrder" || aerr.HTTPStatus != 0 {
		return vendor.OrderAck{}, false
	}
	if !v.client.IsDuplicateRejection(aerr.ExternalCode, aerr.ExternalMessage) {
		return vendor.OrderAck{}, false
	}
	v.logger.InfoContext(ctx, "vinculum already holds this order; no duplicate created",
		slog.String("order_no", req.OrderNo),
		slog.String("location", req.OrderLocation),
		slog.String("vendor_message", aerr.ExternalMessage))
	return vendor.OrderAck{VendorOrderID: req.OrderNo, Created: false}, true
}

// compile-time proof of the roles this adapter is registered under.
var (
	_ vendor.StockProvider = (*Vendor)(nil)
	_ vendor.OrderReceiver = (*Vendor)(nil)
)

// Describe reports the roles implemented, for start-up logging.
func (v *Vendor) Describe() string {
	return fmt.Sprintf("%s (orders and stock, sellable bucket %q)", PlatformName, v.client.SellableBucket())
}
