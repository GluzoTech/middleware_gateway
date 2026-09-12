// Package inventory holds the EasyEcom inventory contracts.
//
// TODO(VERIFY): the Get Inventory Details V2 contract could not be confirmed
// against the EasyEcom API reference, which is not machine-readable. The
// request parameter and response field names below follow EasyEcom's public
// naming conventions and MUST be checked against api-docs.easyecom.io before
// the inventory actions are enabled in production.
package inventory

import (
	"errors"
	"net/url"
	"strings"

	"github.com/gluzo/integration-gateway/app/integrations/easyecom/dto"
)

// GetInventoryDetailsRequest selects the SKU (and optionally warehouse) to
// query.
type GetInventoryDetailsRequest struct {
	SKU         string
	WarehouseID string
}

// Validate reports whether the request identifies a SKU.
func (r GetInventoryDetailsRequest) Validate() error {
	if strings.TrimSpace(r.SKU) == "" {
		return errors.New("sku is required")
	}
	return nil
}

// Query renders the request as URL parameters.
func (r GetInventoryDetailsRequest) Query() url.Values {
	q := url.Values{}
	q.Set("sku", strings.TrimSpace(r.SKU))
	if w := strings.TrimSpace(r.WarehouseID); w != "" {
		q.Set("warehouse_id", w)
	}
	return q
}

// GetInventoryDetailsResponse is the envelope returned by Get Inventory
// Details V2.
type GetInventoryDetailsResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    []InventoryItem `json:"data"`
}

// InventoryItem is the stock position of one SKU in one warehouse.
type InventoryItem struct {
	SKU                string         `json:"sku"`
	ProductID          dto.FlexString `json:"product_id"`
	WarehouseID        dto.FlexString `json:"warehouse_id"`
	AvailableInventory dto.FlexInt    `json:"available_inventory"`
	ReservedInventory  dto.FlexInt    `json:"reserved_inventory"`
	UpdatedAt          string         `json:"updated_at"`
}
