// Package mapper translates Gluzo domain models into Uniware DTOs.
//
// Mappers are pure functions. Anything Uniware requires that the domain
// order lacks becomes a mapping error naming the field, so an operator can
// see exactly what the source platform failed to provide.
package mapper

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/order"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/dabur/dto/order"
)

// Uniware field limits from the create sale order documentation.
const (
	maxCodeLength         = 45
	maxCustomerNameLength = 100
	maxAddressLine1Length = 500
	maxCityLength         = 100
	maxStateLength        = 45
	maxPhoneLength        = 100
	maxEmailLength        = 100
	maxPincodeLength      = 100
	maxAdditionalInfoLen  = 500
)

// OrderOptions carries tenant configuration that is not part of the order.
type OrderOptions struct {
	// FacilityCode is stamped on every item; the same code goes in the
	// Facility header when the request is sent.
	FacilityCode string
	// Channel is the Uniware channel code for gateway-created orders.
	Channel string
	// VerificationRequired holds the order for manual verification.
	VerificationRequired bool
}

// ToCreateSaleOrderRequest maps a domain order to Uniware's create request.
//
// Uniware models quantity as one sale order item per unit, so a line with
// quantity 3 becomes three items whose codes derive from the line
// identifier. Prices are per unit. The order code is the source platform's
// order identifier, which makes creation idempotent on the Uniware side.
// VERIFY with Dabur: whether they prefer the marketplace reference as code.
func ToCreateSaleOrderRequest(o order.Order, opts OrderOptions) (dtoorder.CreateSaleOrderRequest, error) {
	var errs []error

	code := strings.TrimSpace(o.ExternalID)
	if code == "" {
		errs = append(errs, errors.New("order external id is required"))
	}
	if len(code) > maxCodeLength {
		errs = append(errs, fmt.Errorf("order code %q exceeds %d characters", code, maxCodeLength))
	}

	address, addrErrs := toAddress("1", o.ShippingAddress, o.Customer)
	errs = append(errs, addrErrs...)

	items, itemErrs := toItems(o, opts.FacilityCode)
	errs = append(errs, itemErrs...)

	if len(errs) > 0 {
		return dtoorder.CreateSaleOrderRequest{}, mappingError(fmt.Sprintf("order %s: %v", code, errors.Join(errs...)))
	}

	so := dtoorder.SaleOrder{
		Code:                 code,
		DisplayOrderCode:     firstNonEmpty(strings.TrimSpace(o.ReferenceCode), code),
		CustomerName:         truncate(strings.TrimSpace(o.Customer.Name), maxCustomerNameLength),
		CustomerGSTIN:        strings.TrimSpace(o.Customer.TaxID),
		Channel:              strings.TrimSpace(opts.Channel),
		NotificationEmail:    truncate(strings.TrimSpace(o.Customer.Email), maxEmailLength),
		NotificationMobile:   truncate(strings.TrimSpace(o.Customer.Phone), maxPhoneLength),
		CashOnDelivery:       o.IsCashOnDelivery(),
		CurrencyCode:         firstNonEmpty(strings.TrimSpace(o.Currency), "INR"),
		TaxExempted:          false,
		CformProvided:        false,
		VerificationRequired: opts.VerificationRequired,
		TotalDiscount:        o.Discount,
		TotalShippingCharges: o.ShippingCharges,
		UseVerifiedListings:  true,
		Addresses:            []dtoorder.Address{address},
		BillingAddress:       dtoorder.AddressReference{ReferenceID: "1"},
		ShippingAddress:      dtoorder.AddressReference{ReferenceID: "1"},
		SaleOrderItems:       items,
	}
	if o.InvoiceNumber != "" || o.Channel != "" {
		so.AdditionalInfo = truncate(strings.TrimSpace(strings.Join(nonEmpty("invoice "+o.InvoiceNumber, "channel "+o.Channel), "; ")), maxAdditionalInfoLen)
	}
	if !o.OrderedAt.IsZero() {
		ms := o.OrderedAt.UnixMilli()
		so.DisplayOrderDateTime = &ms
	}
	if o.IsCashOnDelivery() {
		so.PaymentInstrument = dtoorder.PaymentInstrumentCash
		so.TotalCashOnDeliveryCharges = 0
	} else {
		so.TotalPrepaidAmount = o.TotalAmount
	}

	return dtoorder.CreateSaleOrderRequest{SaleOrder: so}, nil
}

func toAddress(id string, a order.Address, c order.Customer) (dtoorder.Address, []error) {
	var errs []error
	name := firstNonEmpty(strings.TrimSpace(a.Name), strings.TrimSpace(c.Name))
	phone := firstNonEmpty(strings.TrimSpace(a.Phone), strings.TrimSpace(c.Phone))
	line1 := strings.TrimSpace(a.Line1)
	city := strings.TrimSpace(a.City)
	state := strings.TrimSpace(a.State)

	if name == "" {
		errs = append(errs, errors.New("address name is required"))
	}
	if line1 == "" {
		errs = append(errs, errors.New("address line 1 is required"))
	}
	if city == "" {
		errs = append(errs, errors.New("address city is required"))
	}
	if state == "" {
		errs = append(errs, errors.New("address state is required"))
	}
	if phone == "" {
		errs = append(errs, errors.New("address phone is required"))
	}
	return dtoorder.Address{
		ID:           id,
		Name:         truncate(name, maxCustomerNameLength),
		AddressLine1: truncate(line1, maxAddressLine1Length),
		AddressLine2: strings.TrimSpace(a.Line2),
		City:         truncate(city, maxCityLength),
		State:        truncate(state, maxStateLength),
		Country:      strings.TrimSpace(a.Country),
		Pincode:      truncate(strings.TrimSpace(a.PostalCode), maxPincodeLength),
		Phone:        truncate(phone, maxPhoneLength),
		Email:        truncate(firstNonEmpty(strings.TrimSpace(a.Email), strings.TrimSpace(c.Email)), maxEmailLength),
	}, errs
}

func toItems(o order.Order, facility string) ([]dtoorder.SaleOrderItem, []error) {
	var errs []error
	var items []dtoorder.SaleOrderItem
	prepaid := !o.IsCashOnDelivery()
	for i, line := range o.Items {
		sku := strings.TrimSpace(line.SKU)
		if sku == "" {
			errs = append(errs, fmt.Errorf("item %d: sku is required", i))
			continue
		}
		if line.Quantity <= 0 {
			errs = append(errs, fmt.Errorf("item %d (%s): quantity must be positive", i, sku))
			continue
		}
		lineID := firstNonEmpty(strings.TrimSpace(line.ExternalID), fmt.Sprintf("L%d", i+1))
		unitDiscount := 0.0
		if line.Quantity > 0 {
			unitDiscount = line.Discount / float64(line.Quantity)
		}
		for unit := 1; unit <= line.Quantity; unit++ {
			itemCode := fmt.Sprintf("%s-%d", lineID, unit)
			if len(itemCode) > maxCodeLength {
				errs = append(errs, fmt.Errorf("item code %q exceeds %d characters", itemCode, maxCodeLength))
				break
			}
			item := dtoorder.SaleOrderItem{
				Code:               itemCode,
				ItemSku:            sku,
				ShippingMethodCode: dtoorder.ShippingMethodStandard,
				GiftWrap:           false,
				FacilityCode:       strings.TrimSpace(facility),
				TotalPrice:         line.UnitPrice,
				SellingPrice:       line.UnitPrice,
				Discount:           unitDiscount,
			}
			if prepaid {
				item.PrepaidAmount = line.UnitPrice
			}
			items = append(items, item)
		}
	}
	if len(items) == 0 && len(errs) == 0 {
		errs = append(errs, errors.New("order has no items"))
	}
	return items, errs
}

func mappingError(msg string) *apperror.Error {
	e := apperror.New(apperror.Mapping, msg)
	e.Integration = "dabur"
	return e
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if !strings.HasSuffix(v, " ") {
			out = append(out, v)
		}
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
