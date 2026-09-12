// Package order holds the Uniware sale order contracts
// (documentation.unicommerce.com/docs/saleorder-create.html and
// saleorder-get.html).
package order

// ShippingMethodStandard is the shipping method code the create API
// documents for sale order items.
const ShippingMethodStandard = "STD"

// Payment instruments Uniware accepts.
const (
	PaymentInstrumentCash       = "CASH"
	PaymentInstrumentCreditCard = "CREDIT_CARD"
	PaymentInstrumentDebitCard  = "DEBIT_CARD"
	PaymentInstrumentNetBanking = "NET_BANKING"
	PaymentInstrumentWallet     = "WALLET"
)

// CreateSaleOrderRequest is the body of POST /services/rest/v1/oms/saleOrder/create.
type CreateSaleOrderRequest struct {
	SaleOrder SaleOrder `json:"saleOrder"`
}

// SaleOrder is the order to create. Booleans that Uniware defaults to true
// are sent explicitly so the gateway's intent is never left to a default.
type SaleOrder struct {
	Code                  string `json:"code"`
	DisplayOrderCode      string `json:"displayOrderCode,omitempty"`
	DisplayOrderDateTime  *int64 `json:"displayOrderDateTime,omitempty"` // epoch milliseconds; VERIFY format with Dabur
	ChannelProcessingTime *int64 `json:"channelProcessingTime,omitempty"`
	CustomerCode          string `json:"customerCode,omitempty"`
	CustomerName          string `json:"customerName,omitempty"`
	CustomerGSTIN         string `json:"customerGSTIN,omitempty"`
	Channel               string `json:"channel,omitempty"`
	NotificationEmail     string `json:"notificationEmail,omitempty"`
	NotificationMobile    string `json:"notificationMobile,omitempty"`
	CashOnDelivery        bool   `json:"cashOnDelivery"`
	PaymentInstrument     string `json:"paymentInstrument,omitempty"`
	AdditionalInfo        string `json:"additionalInfo,omitempty"`
	ThirdPartyShipping    bool   `json:"thirdPartyShipping"`
	CurrencyCode          string `json:"currencyCode,omitempty"`
	TaxExempted           bool   `json:"taxExempted"`
	CformProvided         bool   `json:"cformProvided"`
	FulfillmentTat        *int64 `json:"fulfillmentTat,omitempty"`
	VerificationRequired  bool   `json:"verificationRequired"`
	Priority              int    `json:"priority,omitempty"`

	TotalDiscount              float64 `json:"totalDiscount"`
	TotalShippingCharges       float64 `json:"totalShippingCharges"`
	TotalCashOnDeliveryCharges float64 `json:"totalCashOnDeliveryCharges"`
	TotalGiftWrapCharges       float64 `json:"totalGiftWrapCharges"`
	TotalStoreCredit           float64 `json:"totalStoreCredit"`
	TotalPrepaidAmount         float64 `json:"totalPrepaidAmount"`
	UseVerifiedListings        bool    `json:"useVerifiedListings"`

	ShippingProviders         []ShippingProvider         `json:"shippingProviders,omitempty"`
	SaleOrderItemCombinations []SaleOrderItemCombination `json:"saleOrderItemCombinations,omitempty"`
	Addresses                 []Address                  `json:"addresses"`
	BillingAddress            AddressReference           `json:"billingAddress"`
	ShippingAddress           AddressReference           `json:"shippingAddress"`
	SaleOrderItems            []SaleOrderItem            `json:"saleOrderItems"`
	CustomFieldValues         []CustomFieldValue         `json:"customFieldValues,omitempty"`
}

// ShippingProvider pre-assigns a carrier to a packet.
type ShippingProvider struct {
	PacketNumber   int    `json:"packetNumber"`
	Code           string `json:"code"`
	TrackingNumber string `json:"trackingNumber,omitempty"`
}

// SaleOrderItemCombination groups items shipped together.
type SaleOrderItemCombination struct {
	CombinationIdentifier  string `json:"combinationIdentifier"`
	CombinationDescription string `json:"combinationDescription"`
}

// Address is a postal address referenced by id from billing/shipping.
type Address struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	AddressLine1 string `json:"addressLine1"`
	AddressLine2 string `json:"addressLine2,omitempty"`
	Latitude     string `json:"latitude,omitempty"`
	Longitude    string `json:"longitude,omitempty"`
	City         string `json:"city"`
	State        string `json:"state"`
	Country      string `json:"country,omitempty"`
	Pincode      string `json:"pincode,omitempty"`
	Phone        string `json:"phone"`
	Email        string `json:"email,omitempty"`
}

// AddressReference points at an entry of Addresses by id.
type AddressReference struct {
	ReferenceID string `json:"referenceId"`
}

// SaleOrderItem is one unit of one SKU. Uniware models quantity as one
// item per unit, each with its own code.
type SaleOrderItem struct {
	Code               string  `json:"code"`
	ItemSku            string  `json:"itemSku"`
	ShippingMethodCode string  `json:"shippingMethodCode"`
	PacketNumber       string  `json:"packetNumber,omitempty"`
	GiftWrap           bool    `json:"giftWrap"`
	GiftMessage        string  `json:"giftMessage,omitempty"`
	FacilityCode       string  `json:"facilityCode,omitempty"`
	TotalPrice         float64 `json:"totalPrice"`
	SellingPrice       float64 `json:"sellingPrice"`
	PrepaidAmount      float64 `json:"prepaidAmount"`
	Discount           float64 `json:"discount"`
	ShippingCharges    float64 `json:"shippingCharges"`
	GiftWrapCharges    float64 `json:"giftWrapCharges"`
	StoreCredit        float64 `json:"storeCredit"`
}

// CustomFieldValue sets a tenant-defined custom field.
type CustomFieldValue struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}
