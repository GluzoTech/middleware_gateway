package mapper

import (
	"strings"

	"github.com/gluzo/integration-gateway/app/domain/inventory"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/inventory"
)

// ToDomainInventory converts EasyEcom stock rows into domain levels. Rows
// without a SKU are skipped rather than failing the whole batch, because a
// partial inventory view is still useful; the count of skipped rows is
// returned so callers can log it.
func ToDomainInventory(src []dtoinventory.InventoryItem) ([]inventory.Level, int) {
	out := make([]inventory.Level, 0, len(src))
	skipped := 0
	for _, item := range src {
		sku := strings.TrimSpace(item.SKU)
		if sku == "" {
			skipped++
			continue
		}
		updatedAt, err := ParseTimestamp(item.UpdatedAt)
		if err != nil {
			skipped++
			continue
		}
		level := inventory.Level{
			SKU:         sku,
			WarehouseID: strings.TrimSpace(item.WarehouseID.String()),
			Available:   int(item.AvailableInventory),
			Reserved:    int(item.ReservedInventory),
			UpdatedAt:   updatedAt,
		}
		if level.Validate() != nil {
			skipped++
			continue
		}
		out = append(out, level)
	}
	return out, skipped
}
