// Package mapper converts Vinculum DTOs into Gluzo's domain models.
//
// Every function here is pure: no HTTP, no database, no clock. Anything
// time-dependent is passed in, so a mapping is reproducible from its input
// and a test needs nothing but a payload.
package mapper

import (
	"fmt"
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/inventory"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/inventory"
)

// SellableQuantity is assumption A1 of the integration plan, in one place.
//
// The conventional reading of Vinculum's stock rows is that qty is the total
// held and committedQty the portion already promised to open orders, so what
// may still be sold is the difference. BCPL have indicated committedQty may
// itself be the net figure, in which case this becomes `return committed`.
//
// It is one expression deliberately. Getting it wrong is expensive in
// production — systematic oversell if too high, a catalogue reading as out
// of stock if too low — so it must be observed rather than assumed, and the
// correction must be a one-line change. The observation is in the plan:
// read both values, place an order for one unit, read again.
//
// TODO(VERIFY): BCPL open item 1. Blocks Phase 4.
func SellableQuantity(qty, committed int) int {
	n := qty - committed
	if n < 0 {
		return 0
	}
	return n
}

// StockOptions tunes a stock mapping.
type StockOptions struct {
	// SellableBucket keeps only rows in that bucket. Empty accepts every
	// bucket, which is right for reading and wrong for pushing; Phase 4
	// requires it to be set.
	SellableBucket string
	// ObservedAt stamps the levels. Passed in rather than read from the
	// clock so the mapper stays pure.
	ObservedAt time.Time
}

// StockSummary reports what a mapping did with rows it did not return.
type StockSummary struct {
	// Filtered counts rows dropped because they are in another bucket. This
	// is ordinary and expected.
	Filtered int
	// Skipped counts rows that could not be mapped: no SKU, or a level that
	// fails its own validation. These are defects worth logging.
	Skipped int
}

// ToDomainStock converts Vinculum stock rows into domain levels.
//
// Rows are dropped rather than failing the batch, because a partial stock
// view is still useful and one malformed row should not cost the sweep. The
// summary makes the drops visible so they can be logged rather than lost.
//
// The SKU codes on the returned levels are BCPL's, not Gluzo's. Translation
// is the SKU map's job (Phase 3) and does not belong in a mapper.
func ToDomainStock(rows []dtoinventory.StockRow, opts StockOptions) ([]inventory.Level, StockSummary) {
	out := make([]inventory.Level, 0, len(rows))
	var sum StockSummary
	want := strings.TrimSpace(opts.SellableBucket)

	for _, row := range rows {
		if want != "" && !strings.EqualFold(strings.TrimSpace(row.Bucket.String()), want) {
			sum.Filtered++
			continue
		}
		sku := strings.TrimSpace(row.SKUCode.String())
		if sku == "" {
			sum.Skipped++
			continue
		}
		level := inventory.Level{
			SKU:         sku,
			WarehouseID: strings.TrimSpace(row.Location.String()),
			Available:   SellableQuantity(int(row.Qty), int(row.CommittedQty)),
			Reserved:    int(row.CommittedQty),
			UpdatedAt:   opts.ObservedAt,
		}
		if level.Reserved < 0 {
			level.Reserved = 0
		}
		if level.Validate() != nil {
			sum.Skipped++
			continue
		}
		out = append(out, level)
	}
	return out, sum
}

// mappingError reports a payload the mapper refused. Mapping failures are
// permanent: the same payload will map the same way next time, so retrying
// only repeats the failure.
func mappingError(msg string) error {
	e := apperror.New(apperror.Mapping, msg)
	e.Integration = "vinculum"
	return e
}

func mappingErrorf(format string, args ...any) error {
	return mappingError(fmt.Sprintf(format, args...))
}
