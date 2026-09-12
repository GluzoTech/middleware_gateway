package mapper

import (
	"strings"

	"github.com/gluzo/integration-gateway/app/domain/inventory"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/dabur/dto/inventory"
)

// InventoryOptions carries tenant configuration for adjustments.
type InventoryOptions struct {
	FacilityCode string
	ShelfCode    string
	// Remarks is stamped on every adjustment for traceability in Uniware.
	Remarks string
}

// ToInventoryAdjustments maps stock levels to REPLACE adjustments, which set
// the absolute quantity of good inventory on the shelf. Levels without a SKU
// are dropped. VERIFY with Dabur: REPLACE versus ADD/REMOVE deltas, and the
// shelf code that receives synchronised stock.
func ToInventoryAdjustments(levels []inventory.Level, opts InventoryOptions) dtoinventory.AdjustInventoryBulkRequest {
	req := dtoinventory.AdjustInventoryBulkRequest{ForceAllocate: false}
	for _, l := range levels {
		sku := strings.TrimSpace(l.SKU)
		if sku == "" || l.Available < 0 {
			continue
		}
		req.InventoryAdjustments = append(req.InventoryAdjustments, dtoinventory.InventoryAdjustment{
			ItemSKU:        sku,
			Quantity:       l.Available,
			ShelfCode:      opts.ShelfCode,
			InventoryType:  dtoinventory.InventoryTypeGood,
			AdjustmentType: dtoinventory.AdjustmentReplace,
			Remarks:        opts.Remarks,
			FacilityCode:   opts.FacilityCode,
		})
	}
	return req
}
