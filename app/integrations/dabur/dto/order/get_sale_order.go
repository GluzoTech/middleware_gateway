package order

import "github.com/gluzo/integration-gateway/app/integrations/dabur/dto"

// GetSaleOrderRequest is the body of POST /services/rest/v1/oms/saleorder/get.
type GetSaleOrderRequest struct {
	Code                  string   `json:"code"`
	FacilityCodes         []string `json:"facilityCodes,omitempty"`
	PaymentDetailRequired bool     `json:"paymentDetailRequired,omitempty"`
}

// GetSaleOrderResponse is the response of POST /services/rest/v1/oms/saleorder/get.
type GetSaleOrderResponse struct {
	dto.Response
	SaleOrderDTO *SaleOrderDTO `json:"saleOrderDTO"`
}

// SaleOrderDTO is Uniware's view of an existing order.
type SaleOrderDTO struct {
	Code             string             `json:"code"`
	DisplayOrderCode string             `json:"displayOrderCode"`
	Channel          string             `json:"channel"`
	Status           string             `json:"status"`
	Created          any                `json:"created"`
	Updated          any                `json:"updated"`
	SaleOrderItems   []SaleOrderItemDTO `json:"saleOrderItems"`
	ShippingPackages []ShippingPackage  `json:"shippingPackages"`
	PaymentDetail    *PaymentDetail     `json:"paymentDetail"`
}

// SaleOrderItemDTO is one unit of the existing order.
type SaleOrderItemDTO struct {
	ID                  string `json:"id"`
	ItemSku             string `json:"itemSku"`
	FacilityCode        string `json:"facilityCode"`
	ShippingPackageCode string `json:"shippingPackageCode"`
	StatusCode          string `json:"statusCode"`
}

// ShippingPackage is a shipment of the order with its tracking details.
type ShippingPackage struct {
	Code             string `json:"code"`
	Status           string `json:"status"`
	TrackingNumber   string `json:"trackingNumber"`
	ShippingProvider string `json:"shippingProvider"`
	TrackingStatus   string `json:"trackingStatus"`
}

// PaymentDetail is returned when paymentDetailRequired was set.
type PaymentDetail struct {
	PaymentMode   string  `json:"paymentMode"`
	TransactionID string  `json:"transactionId"`
	AmountPaid    float64 `json:"amountPaid"`
}
