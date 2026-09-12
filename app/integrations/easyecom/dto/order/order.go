// Package order holds the EasyEcom order contracts.
//
// Field names follow EasyEcom's published webhook and Get Order Details
// documentation (support.easyecom.io, "Webhook configuration"). Fields marked
// VERIFY are commonly present in EasyEcom payloads but could not be confirmed
// against the API reference, which is not machine-readable.
package order

import "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto"

// Order is one order as EasyEcom represents it in webhooks (V2) and in the
// Get Order Details response. Identifiers and amounts use Flex types because
// EasyEcom emits them as numbers in some payloads and strings in others.
type Order struct {
	InvoiceID     dto.FlexString `json:"invoice_id"`
	OrderID       dto.FlexString `json:"order_id"`
	ReferenceCode dto.FlexString `json:"reference_code"`
	CompanyName   string         `json:"company_name"`
	WarehouseID   dto.FlexString `json:"warehouse_id"`
	OrderStatus   string         `json:"order_status"`
	OrderStatusID dto.FlexString `json:"order_status_id"`
	OrderDate     string         `json:"order_date"`  // VERIFY
	Marketplace   string         `json:"marketplace"` // VERIFY
	MarketplaceID dto.FlexString `json:"marketplace_id"`

	CustomerName string         `json:"customer_name"`
	ContactNum   dto.FlexString `json:"contact_num"`
	Email        string         `json:"email"` // VERIFY
	AddressLine1 string         `json:"address_line_1"`
	AddressLine2 string         `json:"address_line_2"` // VERIFY
	City         string         `json:"city"`
	State        string         `json:"state"`
	PinCode      dto.FlexString `json:"pin_code"`
	Country      string         `json:"country"` // VERIFY

	TotalAmount dto.FlexFloat `json:"total_amount"`
	PaymentMode string        `json:"payment_mode"`

	// EasyEcom names the line items "order_items" in some payload versions
	// and "suborders" in others; Items returns whichever is present.
	OrderItems []Item `json:"order_items"`
	Suborders  []Item `json:"suborders"`
}

// Items returns the order lines regardless of which key carried them.
func (o Order) Items() []Item {
	if len(o.OrderItems) > 0 {
		return o.OrderItems
	}
	return o.Suborders
}

// Item is one order line.
type Item struct {
	SuborderID   dto.FlexString `json:"suborder_id"`
	SKU          string         `json:"sku"`
	ProductID    dto.FlexString `json:"product_id"`
	ProductName  string         `json:"product_name"` // VERIFY
	Quantity     dto.FlexInt    `json:"suborder_quantity"`
	TaxRate      dto.FlexFloat  `json:"tax_rate"`
	TaxType      string         `json:"tax_type"`
	SellingPrice dto.FlexFloat  `json:"selling_price"`
	ShipmentType string         `json:"shipment_type"`
}
