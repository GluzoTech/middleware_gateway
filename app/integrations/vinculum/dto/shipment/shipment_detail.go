// Package shipment holds the Vinculum dispatch contracts.
//
// Source: the live Vinculum eRetail specification for
// POST /RestWS/api/eretail/v1/order/shipmentDetail, read 29 September 2026.
// Field names below are taken from it, including its mixed casing
// (order_no beside orderLocation, invoiceNo beside tracking_number); they
// are reproduced as documented rather than tidied, because a tidied name
// that does not match the wire is a silent empty field.
package shipment

import (
	"errors"
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/integrations/vinculum/dto"
)

// ShipmentDetailRequest selects which dispatch records to read.
//
// Either a window (DateFrom/DateTo) or an explicit OrderNos list must be
// given: a request that bounds nothing would ask BCPL for their entire
// dispatch history. The scheduled sweep uses the window; reconciling a
// known-stuck order uses the list.
type ShipmentDetailRequest struct {
	OrderNos []string

	DateFrom time.Time
	DateTo   time.Time

	OrderLocation string

	// Status filters by Vinculum's own dispatch states. The specification
	// names the parameter without enumerating the values, so they are
	// carried through unchanged.
	Status []string

	PageNumber int

	// FulfillmentLocation and FilterBy are documented parameters whose
	// accepted values are not published. Both are omitted when unset.
	FulfillmentLocation string
	FilterBy            string
}

// Validate reports whether the request is bounded.
func (r ShipmentDetailRequest) Validate() error {
	var errs []error
	if strings.TrimSpace(r.OrderLocation) == "" {
		errs = append(errs, errors.New("order location is required"))
	}
	if len(trimAll(r.OrderNos)) == 0 && (r.DateFrom.IsZero() || r.DateTo.IsZero()) {
		errs = append(errs, errors.New("either order numbers or a complete date window is required"))
	}
	if !r.DateFrom.IsZero() && !r.DateTo.IsZero() && r.DateTo.Before(r.DateFrom) {
		errs = append(errs, errors.New("date window ends before it starts"))
	}
	return errors.Join(errs...)
}

// Body renders the request as the JSON document Vinculum expects.
func (r ShipmentDetailRequest) Body() map[string]any {
	body := map[string]any{
		"order_location": strings.TrimSpace(r.OrderLocation),
	}
	if orders := trimAll(r.OrderNos); len(orders) > 0 {
		body["order_no"] = orders
	}
	if v := dto.FormatTime(r.DateFrom); v != "" {
		body["date_from"] = v
	}
	if v := dto.FormatTime(r.DateTo); v != "" {
		body["date_to"] = v
	}
	if status := trimAll(r.Status); len(status) > 0 {
		body["status"] = status
	}
	if r.PageNumber > 0 {
		body["pageNumber"] = r.PageNumber
	}
	if v := strings.TrimSpace(r.FulfillmentLocation); v != "" {
		body["fulfillmentLocation"] = v
	}
	if v := strings.TrimSpace(r.FilterBy); v != "" {
		body["filterBy"] = v
	}
	return body
}

// ShipmentDetailResponse is one page of dispatch records.
type ShipmentDetailResponse struct {
	dto.Envelope

	HasMore  dto.FlexBool    `json:"hasMore"`
	Response []OrderShipment `json:"response"`
}

// OrderShipment is one order's dispatch record.
type OrderShipment struct {
	// ExtOrderNo is the order number the gateway sent, which is EasyEcom's
	// order id. OrderNo is Vinculum's own. The first is what identifies the
	// order everywhere else in the gateway.
	ExtOrderNo dto.FlexString `json:"extOrderNo"`
	OrderNo    dto.FlexString `json:"order_no"`

	Status dto.FlexString `json:"status"`

	// ShipDetail is absent until the order ships, which is ordinary and not
	// an error.
	ShipDetail *ShipDetail `json:"shipDetail"`
}

// ShipDetail is the dispatch information for one package.
type ShipDetail struct {
	TrackingNumber dto.FlexString `json:"tracking_number"`
	Transporter    dto.FlexString `json:"transporter"`
	TrackingURL    dto.FlexString `json:"tracking_url"`
	ShipDate       dto.FlexString `json:"shipdate"`
	Status         dto.FlexString `json:"status"`

	// InvoiceNo and SellerGstNo are the vendor's, not Gluzo's: under
	// dropship BCPL invoices the customer under its own registration.
	InvoiceNo   dto.FlexString `json:"invoiceNo"`
	SellerGstNo dto.FlexString `json:"sellerGstNo"`

	// DeliveredDate, DeliveryNumber and EwbNo are carried through to the
	// workflow state rather than the domain model; no consumer needs them
	// yet and inventing domain fields for them would be speculative.
	DeliveredDate  dto.FlexString `json:"delivereddate"`
	DeliveryNumber dto.FlexString `json:"deliveryNumber"`
	EwbNo          dto.FlexString `json:"ewbNo"`
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
