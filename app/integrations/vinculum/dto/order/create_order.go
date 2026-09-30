// Package order holds the Vinculum order contracts.
//
// Source: the live Vinculum eRetail specification for
// POST /RestWS/api/eretail/v4/order/create, read 29 September 2026. The
// header and line field names below are taken from it.
//
// TODO(VERIFY): the specification names "the ship* address block" without
// enumerating its members. The nine ship fields below follow the documented
// prefix and Vinculum's conventions elsewhere; they are NOT confirmed and are
// listed in docs/blockers.md. A wrong address field name is the one mistake
// here that fails quietly — Vinculum would accept the order and ship it with
// a field missing — so these must be confirmed before any live order.
package order

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gluzo/integration-gateway/app/integrations/vinculum/dto"
)

// Field widths the specification states.
const (
	MaxOrderNoLength       = 50
	MaxOrderLocationLength = 3
)

// CreateOrderRequest is one order offered to the vendor for fulfilment.
type CreateOrderRequest struct {
	// OrderNo carries Gluzo's own order identifier — the EasyEcom order id.
	//
	// This is what makes submission idempotent: Vinculum rejects a second
	// order under the same number, so a resumed or redelivered run cannot
	// create a duplicate even if the gateway believes it must try again.
	OrderNo string `json:"orderNo"`
	// OrderLocation is the vendor-side warehouse, three characters.
	OrderLocation string `json:"orderLocation"`

	OrderType     string `json:"orderType,omitempty"`
	PaymentType   string `json:"paymentType,omitempty"`
	Status        string `json:"status,omitempty"`
	OrderDate     string `json:"orderDate,omitempty"`
	OrderCurrency string `json:"orderCurrency,omitempty"`

	// AWBNo exists in the contract and stays empty: under dropship BCPL
	// books the courier, so the gateway has no waybill to offer and must not
	// invent one.
	AWBNo string `json:"awbNo,omitempty"`

	ShipTo       string `json:"shipTo,omitempty"`
	ShipAddress  string `json:"shipAddress1,omitempty"`
	ShipAddress2 string `json:"shipAddress2,omitempty"`
	ShipCity     string `json:"shipCity,omitempty"`
	ShipState    string `json:"shipState,omitempty"`
	ShipCountry  string `json:"shipCountry,omitempty"`
	ShipPincode  string `json:"shipPincode,omitempty"`
	ShipMobile   string `json:"shipMobile,omitempty"`
	ShipEmail    string `json:"shipEmail,omitempty"`

	OrderAmount []Line `json:"orderAmount"`
}

// Line is one order line.
type Line struct {
	// LineNo identifies the line within the order and must be persisted:
	// Vinculum's shipment response is line-level, so without it a dispatch
	// cannot be attributed to the item it shipped.
	LineNo int `json:"lineno"`
	// SKU is the vendor's item code, not Gluzo's. Translation happens in the
	// workflow, against the integration's SKU map.
	SKU       string  `json:"sku"`
	OrderQty  int     `json:"orderQty"`
	UnitPrice float64 `json:"unitPrice"`
	// MRP is the printed maximum retail price. Where the origin does not
	// carry one, the unit price stands in: a zero MRP reads as "free" on a
	// customer-facing document.
	MRP           float64 `json:"mrp,omitempty"`
	DiscountAmt   float64 `json:"discountAmt,omitempty"`
	TaxPercentage float64 `json:"taxPercentage,omitempty"`
	Vendor        string  `json:"vendor,omitempty"`

	UDF1 string `json:"udf1,omitempty"`
	UDF2 string `json:"udf2,omitempty"`
	UDF3 string `json:"udf3,omitempty"`
	UDF4 string `json:"udf4,omitempty"`
}

// Validate reports every invariant the request violates.
//
// The checks that mirror Vinculum's documented widths are done here rather
// than left to the vendor, so an over-long identifier is a mapping failure
// naming the field instead of an opaque rejection at submission time.
func (r CreateOrderRequest) Validate() error {
	var errs []error
	orderNo := strings.TrimSpace(r.OrderNo)
	switch {
	case orderNo == "":
		errs = append(errs, errors.New("orderNo is required"))
	case len(orderNo) > MaxOrderNoLength:
		errs = append(errs, fmt.Errorf("orderNo %q exceeds %d characters", orderNo, MaxOrderNoLength))
	}

	location := strings.TrimSpace(r.OrderLocation)
	switch {
	case location == "":
		errs = append(errs, errors.New("orderLocation is required"))
	case len(location) > MaxOrderLocationLength:
		errs = append(errs, fmt.Errorf("orderLocation %q exceeds %d characters", location, MaxOrderLocationLength))
	}

	if len(r.OrderAmount) == 0 {
		errs = append(errs, errors.New("at least one order line is required"))
	}
	seen := make(map[int]bool, len(r.OrderAmount))
	for i, line := range r.OrderAmount {
		if strings.TrimSpace(line.SKU) == "" {
			errs = append(errs, fmt.Errorf("line %d has no sku", i))
		}
		if line.OrderQty <= 0 {
			errs = append(errs, fmt.Errorf("line %d (%s) has quantity %d", i, line.SKU, line.OrderQty))
		}
		if line.LineNo <= 0 {
			errs = append(errs, fmt.Errorf("line %d (%s) has no line number", i, line.SKU))
		}
		if seen[line.LineNo] {
			// Duplicated line numbers would make a line-level shipment
			// response ambiguous, which is the one thing lineno exists for.
			errs = append(errs, fmt.Errorf("line number %d appears more than once", line.LineNo))
		}
		seen[line.LineNo] = true
	}
	return errors.Join(errs...)
}

// CreateOrderResponse is the envelope returned by order creation.
//
// TODO(VERIFY): the specification documents the envelope but not what an
// accepted order returns beyond it. OrderNo is decoded where present; the
// adapter falls back to the number it sent, which it knows.
type CreateOrderResponse struct {
	dto.Envelope

	OrderNo dto.FlexString `json:"orderNo"`
	// Response carries per-order detail on some Vinculum endpoints.
	Response []CreateOrderResult `json:"response"`
}

// CreateOrderResult is one order's outcome where Vinculum reports a list.
type CreateOrderResult struct {
	OrderNo dto.FlexString `json:"orderNo"`
	Status  dto.FlexString `json:"status"`
	Message string         `json:"message"`
}

// VendorOrderNo returns the identifier Vinculum reported, if any.
func (r CreateOrderResponse) VendorOrderNo() string {
	if v := strings.TrimSpace(r.OrderNo.String()); v != "" {
		return v
	}
	for _, res := range r.Response {
		if v := strings.TrimSpace(res.OrderNo.String()); v != "" {
			return v
		}
	}
	return ""
}
