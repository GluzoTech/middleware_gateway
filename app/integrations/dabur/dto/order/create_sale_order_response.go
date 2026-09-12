package order

import "github.com/gluzo/integration-gateway/app/integrations/dabur/dto"

// CreateSaleOrderResponse is the response of POST /services/rest/v1/oms/saleOrder/create.
type CreateSaleOrderResponse struct {
	dto.Response
	SaleOrderDetailDTO *SaleOrderDetail `json:"saleOrderDetailDTO"`
}

// SaleOrderDetail describes the created order.
type SaleOrderDetail struct {
	Code                 string             `json:"code"`
	DisplayOrderCode     string             `json:"displayOrderCode"`
	Channel              string             `json:"channel"`
	DisplayOrderDateTime any                `json:"displayOrderDateTime"`
	Status               string             `json:"status"`
	Created              any                `json:"created"`
	Updated              any                `json:"updated"`
	NotificationEmail    string             `json:"notificationEmail"`
	NotificationMobile   string             `json:"notificationMobile"`
	CustomerGSTIN        string             `json:"customerGSTIN"`
	COD                  bool               `json:"cod"`
	Priority             int                `json:"priority"`
	CurrencyCode         string             `json:"currencyCode"`
	CustomerCode         string             `json:"customerCode"`
	BillingAddress       *Address           `json:"billingAddress"`
	Addresses            []Address          `json:"addresses"`
	CustomFieldValues    []CustomFieldValue `json:"customFieldValues"`
}
