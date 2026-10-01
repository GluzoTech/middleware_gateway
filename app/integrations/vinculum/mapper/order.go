package mapper

import (
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/integrations/vinculum/dto"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/order"
	"github.com/gluzo/integration-gateway/app/vendor"
)

// Vinculum order types and payment types.
//
// TODO(VERIFY): the specification names orderType and paymentType as header
// fields without enumerating their values. These follow Vinculum's
// conventions and are sent only because omitting a documented field is the
// less safe guess; both are one constant each.
const (
	OrderTypeSale      = "SALE"
	PaymentTypeCOD     = "COD"
	PaymentTypePrepaid = "PREPAID"
	StatusNew          = "NEW"
)

// DefaultCurrency is used when the origin platform carries none. EasyEcom's
// order payload does not include a currency.
const DefaultCurrency = "INR"

// OrderOptions tunes an order mapping.
type OrderOptions struct {
	// VendorName is stamped on each line's `vendor` field where the
	// deployment uses it. Empty omits it.
	VendorName string
}

// ToCreateOrder converts a domain order into Vinculum's create-order
// document.
//
// Pure: no network, no database, no clock. The route supplies the location
// and the order supplies everything else, so the same order and route always
// produce the same document — which is what lets a resumed run submit
// exactly what was mapped rather than remapping against a moved world.
//
// The item SKUs must already be the vendor's. Translating them needs the
// integration's SKU map, which is configuration a pure mapper cannot load;
// the workflow does it before calling.
func ToCreateOrder(o order.Order, route vendor.Route, opts OrderOptions) (dtoorder.CreateOrderRequest, error) {
	req := dtoorder.CreateOrderRequest{
		// Gluzo's own order id becomes Vinculum's order number. Vinculum
		// rejecting a duplicate is what makes submission idempotent, so this
		// must be stable across retries and resumes — it is the order's
		// identity, not a generated reference.
		OrderNo:       strings.TrimSpace(o.ExternalID),
		OrderLocation: strings.TrimSpace(route.VendorReference),
		OrderType:     OrderTypeSale,
		PaymentType:   paymentType(o),
		Status:        StatusNew,
		OrderDate:     FormatOrderDate(o.OrderedAt),
		OrderCurrency: currency(o),
	}

	ship := shippingAddress(o)
	req.ShipTo = firstNonEmpty(ship.Name, o.Customer.Name)
	req.ShipAddress = strings.TrimSpace(ship.Line1)
	req.ShipAddress2 = strings.TrimSpace(ship.Line2)
	req.ShipCity = strings.TrimSpace(ship.City)
	req.ShipState = strings.TrimSpace(ship.State)
	req.ShipCountry = firstNonEmpty(ship.Country, "India")
	req.ShipPincode = strings.TrimSpace(ship.PostalCode)
	req.ShipMobile = firstNonEmpty(ship.Phone, o.Customer.Phone)
	req.ShipEmail = firstNonEmpty(ship.Email, o.Customer.Email)

	req.OrderAmount = make([]dtoorder.Line, 0, len(o.Items))
	for i, item := range o.Items {
		line := dtoorder.Line{
			// Line numbers are one-based and assigned by position. They are
			// persisted with the prepared document, so a shipment response
			// that names a line can be attributed to the item that shipped.
			LineNo:        i + 1,
			SKU:           strings.TrimSpace(item.SKU),
			OrderQty:      item.Quantity,
			UnitPrice:     item.UnitPrice,
			DiscountAmt:   item.Discount,
			TaxPercentage: item.TaxRate,
			Vendor:        strings.TrimSpace(opts.VendorName),
			// The origin's line identifier travels in a user-defined field
			// so a dispatch can be traced back to the EasyEcom suborder
			// without another lookup.
			UDF1: strings.TrimSpace(item.ExternalID),
		}
		// The origin carries no MRP. Sending zero would read as "free" on a
		// customer-facing document, so the unit price stands in.
		line.MRP = item.UnitPrice
		req.OrderAmount = append(req.OrderAmount, line)
	}

	if err := req.Validate(); err != nil {
		return dtoorder.CreateOrderRequest{}, mappingErrorf("order %s: %v", o.ExternalID, err)
	}
	if err := checkShippable(o, req); err != nil {
		return dtoorder.CreateOrderRequest{}, err
	}
	return req, nil
}

// FormatOrderDate renders an order timestamp for Vinculum.
//
// TODO(VERIFY): shares RequestTimeLayout with the read endpoints, which is
// itself unconfirmed (BCPL open item B11). A zero time renders empty rather
// than as the zero date, because a 1-January-0001 order would be accepted and
// then be wrong forever.
func FormatOrderDate(t time.Time) string { return dto.FormatTime(t) }

// requiredShippingFields are the fields an order cannot be dispatched
// without.
//
// This is the gateway's own guard, not a documented Vinculum constraint: the
// specification does not say which address fields are mandatory. Checking
// here turns "shipped to an incomplete address" — which nobody notices until
// a customer complains — into a mapping failure naming the field, which is
// permanent and therefore visible.
func checkShippable(o order.Order, req dtoorder.CreateOrderRequest) error {
	missing := make([]string, 0, 6)
	for _, f := range []struct {
		name  string
		value string
	}{
		{"recipient name", req.ShipTo},
		{"address line 1", req.ShipAddress},
		{"city", req.ShipCity},
		{"state", req.ShipState},
		{"postal code", req.ShipPincode},
		{"phone", req.ShipMobile},
	} {
		if strings.TrimSpace(f.value) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return mappingErrorf("order %s cannot be shipped: missing %s",
		o.ExternalID, strings.Join(missing, ", "))
}

func paymentType(o order.Order) string {
	if o.IsCashOnDelivery() {
		return PaymentTypeCOD
	}
	return PaymentTypePrepaid
}

func currency(o order.Order) string {
	if v := strings.TrimSpace(o.Currency); v != "" {
		return v
	}
	return DefaultCurrency
}

// shippingAddress prefers the shipping address and falls back to billing.
// An order that carries only one address is ordinary: EasyEcom's payload
// carries a single address used for both.
func shippingAddress(o order.Order) order.Address {
	if !o.ShippingAddress.IsEmpty() {
		return o.ShippingAddress
	}
	return o.BillingAddress
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
