package easyecom

import (
	"context"
	"log/slog"
	"strings"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/inventory"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/inventory"
	"github.com/gluzo/integration-gateway/app/vendor"
)

// Sink is EasyEcom in its role as the origin platform that receives what a
// vendor owns: stock it does not hold, and dispatch it did not book.
//
// It is a separate type from Source deliberately. Source reads the
// customer-facing order; Sink writes vendor-owned facts inward. They travel
// in opposite directions, they are used by different workflows, and one type
// carrying both would be a type whose method names do not agree about which
// way data moves.
type Sink struct {
	client *Client
	logger *slog.Logger
}

// NewSink wraps a client.
func NewSink(client *Client, logger *slog.Logger) *Sink {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sink{client: client, logger: logger}
}

// Platform implements vendor.StockSink.
func (s *Sink) Platform() string { return PlatformName }

// PushStock implements vendor.StockSink.
//
// The quantities are authoritative and replace what EasyEcom holds. Nothing
// here reconciles them against EasyEcom's own view: the vendor is the source
// of truth for these SKUs, and an origin that applied its own arithmetic on
// top would produce drift nobody could trace back.
//
// Route.OriginReference selects the location the write authenticates for.
// Because EasyEcom scopes its JWT to one location, a defect in the SKU set
// cannot write quantities into a location the route does not name — the
// platform rejects it rather than trusting us to have got it right.
//
// Levels arrive already translated into Gluzo's SKUs; translation is the SKU
// map's job and happens in the workflow, not here.
func (s *Sink) PushStock(ctx context.Context, route vendor.Route, levels []inventory.Level) (vendor.StockResult, error) {
	if err := route.Validate(); err != nil {
		return vendor.StockResult{}, apperror.Wrap(apperror.Validation, "incomplete route", err)
	}
	if len(levels) == 0 {
		return vendor.StockResult{}, nil
	}

	location := strings.TrimSpace(route.OriginReference)
	if location == "" {
		// The process default is the right fallback for a single-location
		// deployment and the wrong one for this pipeline, so it is logged
		// rather than passed over: a stock push landing in the default
		// location is the failure the per-location design exists to stop.
		s.logger.WarnContext(ctx, "stock push has no origin reference; using the process default location",
			slog.String("integration", route.IntegrationName),
			slog.String("vendor", route.VendorPlatform))
	}

	items, skipped := toBulkItems(levels)
	var result vendor.StockResult
	result.Failed += skipped

	for _, batch := range chunk(items, dtoinventory.MaxBulkItems) {
		resp, err := s.client.BulkInventoryUpdate(ctx, location, dtoinventory.BulkInventoryUpdateRequest{Items: batch})
		if err != nil {
			// The batches already sent stand. Quantities are absolute, so a
			// partial push leaves EasyEcom consistent for what it did
			// receive and the rest is corrected by the next run rather than
			// compounding.
			result.Failed += len(batch)
			return result, err
		}
		updated, failed := countOutcomes(ctx, batch, resp.Data, s.logger)
		result.Updated += updated
		result.Failed += failed
	}
	return result, nil
}

// toBulkItems converts domain levels into request items, clamping at
// EasyEcom's maximum.
//
// A level that cannot be sent is counted rather than dropped silently: the
// caller reports it as a failure, because a SKU whose stock never reaches the
// storefront looks exactly like a SKU whose stock did not change.
func toBulkItems(levels []inventory.Level) ([]dtoinventory.BulkInventoryUpdateItem, int) {
	items := make([]dtoinventory.BulkInventoryUpdateItem, 0, len(levels))
	skipped := 0
	for _, l := range levels {
		sku := strings.TrimSpace(l.SKU)
		if sku == "" {
			skipped++
			continue
		}
		qty := l.Available
		if qty < 0 {
			qty = 0
		}
		if qty > dtoinventory.MaxQuantity {
			// EasyEcom clamps silently and reports the clamped figure. Doing
			// it here means the number sent is the number stored, so the
			// response can be read at face value.
			qty = dtoinventory.MaxQuantity
		}
		items = append(items, dtoinventory.BulkInventoryUpdateItem{SKU: sku, Quantity: qty})
	}
	return items, skipped
}

// countOutcomes reads the per-SKU results, where EasyEcom reports them.
func countOutcomes(ctx context.Context, sent []dtoinventory.BulkInventoryUpdateItem, results []dtoinventory.BulkInventoryUpdateResult, logger *slog.Logger) (updated, failed int) {
	if len(results) == 0 {
		// No per-SKU detail and a successful envelope: the whole batch was
		// accepted.
		return len(sent), 0
	}
	bySKU := make(map[string]bool, len(results))
	for _, r := range results {
		bySKU[strings.ToLower(strings.TrimSpace(r.SKU))] = r.Failed()
		if r.Failed() {
			logger.WarnContext(ctx, "easyecom rejected a stock update",
				slog.String("sku", r.SKU),
				slog.String("status", r.Status.String()),
				slog.String("message", r.Message))
		}
	}
	for _, item := range sent {
		if didFail, ok := bySKU[strings.ToLower(item.SKU)]; ok && didFail {
			failed++
			continue
		}
		updated++
	}
	return updated, failed
}

// chunk splits items into batches of at most size.
func chunk(items []dtoinventory.BulkInventoryUpdateItem, size int) [][]dtoinventory.BulkInventoryUpdateItem {
	if size < 1 {
		size = 1
	}
	var out [][]dtoinventory.BulkInventoryUpdateItem
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[start:end])
	}
	return out
}

// compile-time proof that the sink satisfies the role it is registered under.
var _ vendor.StockSink = (*Sink)(nil)
