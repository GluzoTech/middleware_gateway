package vinculum

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/inventory"
	"github.com/gluzo/integration-gateway/app/integrations/vinculum/mapper"
	"github.com/gluzo/integration-gateway/app/vendor"
)

// Vendor is BCPL as a fulfilment partner: the adapter that implements the
// roles in app/vendor on top of the Vinculum client.
//
// Only StockProvider is implemented so far. The registry discovers that by
// type assertion, so asking this vendor to receive an order reports a missing
// role rather than an unknown vendor — a different mistake, diagnosed
// differently. OrderReceiver arrives in Phase 5 and FulfilmentProvider in
// Phase 6.
type Vendor struct {
	client *Client
	logger *slog.Logger
	now    func() time.Time
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

// compile-time proof of the role this adapter is registered under.
var _ vendor.StockProvider = (*Vendor)(nil)

// Describe reports the roles implemented, for start-up logging.
func (v *Vendor) Describe() string {
	return fmt.Sprintf("%s (stock provider, sellable bucket %q)", PlatformName, v.client.SellableBucket())
}
