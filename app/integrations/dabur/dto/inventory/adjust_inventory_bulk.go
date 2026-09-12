// Package inventory holds the Uniware inventory adjustment contracts
// (documentation.unicommerce.com/docs/adjust-inventory-bulk.html).
package inventory

import "github.com/gluzo/integration-gateway/app/integrations/dabur/dto"

// Inventory types.
const (
	InventoryTypeGood       = "GOOD_INVENTORY"
	InventoryTypeBad        = "BAD_INVENTORY"
	InventoryTypeQCRejected = "QC_REJECTED"
	InventoryTypeVirtual    = "VIRTUAL_INVENTORY"
)

// Adjustment types.
const (
	AdjustmentAdd      = "ADD"
	AdjustmentRemove   = "REMOVE"
	AdjustmentReplace  = "REPLACE"
	AdjustmentTransfer = "TRANSFER"
)

// AdjustInventoryBulkRequest is the body of POST /services/rest/v1/inventory/adjust/bulk.
type AdjustInventoryBulkRequest struct {
	InventoryAdjustments []InventoryAdjustment `json:"inventoryAdjustments"`
	ForceAllocate        bool                  `json:"forceAllocate"`
}

// InventoryAdjustment is one SKU adjustment on one shelf of one facility.
type InventoryAdjustment struct {
	ItemSKU             string `json:"itemSKU"`
	Quantity            int    `json:"quantity"`
	ShelfCode           string `json:"shelfCode"`
	InventoryType       string `json:"inventoryType,omitempty"`
	TransferToShelfCode string `json:"transferToShelfCode,omitempty"`
	SLA                 int    `json:"sla,omitempty"`
	AdjustmentType      string `json:"adjustmentType"`
	Remarks             string `json:"remarks,omitempty"`
	FacilityCode        string `json:"facilityCode"`
}

// AdjustInventoryBulkResponse is the response of the bulk adjustment API.
type AdjustInventoryBulkResponse struct {
	dto.Response
	InventoryAdjustmentResponses []InventoryAdjustmentResponse `json:"inventoryAdjustmentResponses"`
}

// InventoryAdjustmentResponse is the per-adjustment outcome.
type InventoryAdjustmentResponse struct {
	FacilityInventoryAdjustment InventoryAdjustment `json:"facilityInventoryAdjustment"`
	Successful                  bool                `json:"successful"`
	Errors                      []dto.Error         `json:"errors"`
}
