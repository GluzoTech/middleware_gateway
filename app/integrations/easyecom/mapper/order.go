// Package mapper translates EasyEcom DTOs into Gluzo domain models.
//
// Mappers are pure functions: no HTTP, no database, no clock other than the
// timestamps in the payload. Anything they cannot interpret becomes a
// mapping error rather than a silently wrong domain value.
package mapper

import (
	"fmt"
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/order"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/order"
)

// istZone is applied to EasyEcom timestamps that carry no zone. EasyEcom
// operates in India and reports local time.
var istZone = time.FixedZone("IST", 5*3600+30*60)

// timestampLayouts are tried in order when parsing EasyEcom timestamps.
var timestampLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// ToDomainOrder converts an EasyEcom order into the domain model.
func ToDomainOrder(src dtoorder.Order) (order.Order, error) {
	externalID := strings.TrimSpace(src.OrderID.String())
	if externalID == "" {
		externalID = strings.TrimSpace(src.InvoiceID.String())
	}
	if externalID == "" {
		return order.Order{}, mappingError("order has neither order_id nor invoice_id")
	}

	orderedAt, err := ParseTimestamp(src.OrderDate)
	if err != nil {
		return order.Order{}, mappingError(fmt.Sprintf("order %s: %v", externalID, err))
	}

	address := order.Address{
		Name:       strings.TrimSpace(src.CustomerName),
		Line1:      strings.TrimSpace(src.AddressLine1),
		Line2:      strings.TrimSpace(src.AddressLine2),
		City:       strings.TrimSpace(src.City),
		State:      strings.TrimSpace(src.State),
		Country:    strings.TrimSpace(src.Country),
		PostalCode: strings.TrimSpace(src.PinCode.String()),
		Phone:      strings.TrimSpace(src.ContactNum.String()),
		Email:      strings.TrimSpace(src.Email),
	}

	items := src.Items()
	out := order.Order{
		ExternalID:    externalID,
		InvoiceNumber: strings.TrimSpace(src.InvoiceID.String()),
		ReferenceCode: strings.TrimSpace(src.ReferenceCode.String()),
		Channel:       strings.TrimSpace(src.Marketplace),
		WarehouseID:   strings.TrimSpace(src.WarehouseID.String()),
		Status:        NormaliseStatus(src.OrderStatus),
		SourceStatus:  strings.TrimSpace(src.OrderStatus),
		OrderedAt:     orderedAt,
		Customer: order.Customer{
			Name:  address.Name,
			Email: address.Email,
			Phone: address.Phone,
		},
		ShippingAddress: address,
		BillingAddress:  address, // EasyEcom's order payload carries a single address
		PaymentMode:     NormalisePaymentMode(src.PaymentMode),
		Currency:        "INR",
		TotalAmount:     float64(src.TotalAmount),
		Items:           make([]order.Item, 0, len(items)),
	}
	for i, item := range items {
		mapped, err := toDomainItem(item)
		if err != nil {
			return order.Order{}, mappingError(fmt.Sprintf("order %s item %d: %v", externalID, i, err))
		}
		out.Items = append(out.Items, mapped)
	}

	if err := out.Validate(); err != nil {
		return order.Order{}, mappingError(fmt.Sprintf("order %s: %v", externalID, err))
	}
	return out, nil
}

func toDomainItem(src dtoorder.Item) (order.Item, error) {
	if strings.TrimSpace(src.SKU) == "" {
		return order.Item{}, fmt.Errorf("missing sku")
	}
	qty := int(src.Quantity)
	unit := float64(src.SellingPrice)
	return order.Item{
		ExternalID: strings.TrimSpace(src.SuborderID.String()),
		SKU:        strings.TrimSpace(src.SKU),
		ProductID:  strings.TrimSpace(src.ProductID.String()),
		Name:       strings.TrimSpace(src.ProductName),
		Quantity:   qty,
		UnitPrice:  unit,
		TaxRate:    float64(src.TaxRate),
		TaxType:    strings.TrimSpace(src.TaxType),
		Total:      unit * float64(qty),
	}, nil
}

// ParseTimestamp interprets an EasyEcom timestamp. Values without a zone are
// taken as IST. An empty value yields the zero time, not an error, because
// not every payload version carries order_date.
func ParseTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range timestampLayouts {
		if t, err := time.ParseInLocation(layout, s, istZone); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", s)
}

// NormalisePaymentMode maps EasyEcom's free-text payment mode to the domain
// enumeration.
func NormalisePaymentMode(s string) order.PaymentMode {
	v := strings.ToLower(strings.TrimSpace(s))
	switch {
	case v == "":
		return order.PaymentUnknown
	case v == "cod", strings.Contains(v, "cash on delivery"), strings.Contains(v, "cash_on_delivery"), strings.Contains(v, "pay on delivery"):
		return order.PaymentCashOnDelivery
	case strings.Contains(v, "prepaid"), strings.Contains(v, "online"), strings.Contains(v, "paid"),
		strings.Contains(v, "card"), strings.Contains(v, "upi"), strings.Contains(v, "netbanking"),
		strings.Contains(v, "net banking"), strings.Contains(v, "wallet"):
		return order.PaymentPrepaid
	default:
		return order.PaymentUnknown
	}
}

// NormaliseStatus maps EasyEcom's order status label to the domain
// enumeration. Matching is by keyword so that label variants across
// EasyEcom versions land in the right bucket.
func NormaliseStatus(s string) order.Status {
	v := strings.ToLower(strings.TrimSpace(s))
	switch {
	case v == "":
		return order.StatusUnknown
	case strings.Contains(v, "cancel"):
		return order.StatusCancelled
	case strings.Contains(v, "return"), strings.Contains(v, "rto"):
		return order.StatusReturned
	case strings.Contains(v, "deliver"):
		return order.StatusDelivered
	case strings.Contains(v, "ship"), strings.Contains(v, "manifest"), strings.Contains(v, "dispatch"), strings.Contains(v, "transit"):
		return order.StatusShipped
	case strings.Contains(v, "confirm"), strings.Contains(v, "assigned"), strings.Contains(v, "printed"), strings.Contains(v, "packed"):
		return order.StatusConfirmed
	case strings.Contains(v, "pending"), strings.Contains(v, "new"), strings.Contains(v, "open"), strings.Contains(v, "created"), strings.Contains(v, "unconfirmed"):
		return order.StatusPending
	default:
		return order.StatusUnknown
	}
}

func mappingError(msg string) *apperror.Error {
	e := apperror.New(apperror.Mapping, msg)
	e.Integration = "easyecom"
	return e
}
