package tracking

import (
	"errors"
	"strings"
)

// AssignShipmentDetailsRequest records an externally booked dispatch against
// an order.
//
// Built for exactly the dropship case: the courier was booked by someone
// else, and the origin platform is being told what happened rather than
// asked to arrange it.
//
// TODO(VERIFY): the EasyEcom Postman collection read on 29 September 2026
// records this operation and its body but not its path or its response
// shape. Both are listed in docs/blockers.md.
type AssignShipmentDetailsRequest struct {
	// InvoiceID identifies the order at EasyEcom.
	InvoiceID string `json:"invoiceId"`
	// Courier is the carrier's name as it should appear.
	Courier string `json:"courier"`
	// AWBNum is the waybill the vendor's carrier issued.
	AWBNum string `json:"awbNum"`
	// CompanyCarrierID is EasyEcom's own identifier for the carrier,
	// obtained once the carrier is registered on the account. It comes from
	// the courier map, never from code.
	CompanyCarrierID string `json:"companyCarrierId,omitempty"`

	ShippingLabelURL string `json:"shippingLabelUrl,omitempty"`
	InvoiceURL       string `json:"invoiceUrl,omitempty"`
	OriginCode       string `json:"origin_code,omitempty"`
	DestinationCode  string `json:"destination_code,omitempty"`
}

// Validate reports whether the request identifies an order and a dispatch.
func (r AssignShipmentDetailsRequest) Validate() error {
	var errs []error
	if strings.TrimSpace(r.InvoiceID) == "" {
		errs = append(errs, errors.New("invoice id is required"))
	}
	// A dispatch with neither a waybill nor a carrier carries no
	// information a customer could use, and sending it would overwrite a
	// record that might have had both.
	if strings.TrimSpace(r.AWBNum) == "" && strings.TrimSpace(r.Courier) == "" {
		errs = append(errs, errors.New("awb number or courier is required"))
	}
	return errors.Join(errs...)
}

// AssignShipmentDetailsResponse is the envelope returned by the assignment.
type AssignShipmentDetailsResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// UpdateTrackingStatusRequest moves a shipment's delivery status on.
//
// TODO(VERIFY): current_shipment_status_id is a numeric enumeration EasyEcom
// assigns, and its values are not published (EasyEcom open item 7). The
// gateway will not guess one: without a configured mapping the status update
// is skipped and the dispatch details are still assigned, so the customer
// gets a tracking number even while the status cannot be set.
type UpdateTrackingStatusRequest struct {
	CurrentShipmentStatusID string `json:"current_shipment_status_id"`
	AWB                     string `json:"awb"`

	HistoryScans          []HistoryScan `json:"history_scans,omitempty"`
	EstimatedDeliveryDate string        `json:"estimated_delivery_date,omitempty"`
	DeliveryDate          string        `json:"delivery_date,omitempty"`
}

// HistoryScan is one carrier scan in a shipment's history.
type HistoryScan struct {
	Status   string `json:"status,omitempty"`
	Location string `json:"location,omitempty"`
	Time     string `json:"time,omitempty"`
	Remark   string `json:"remark,omitempty"`
}

// Validate reports whether the request identifies a shipment and a status.
func (r UpdateTrackingStatusRequest) Validate() error {
	var errs []error
	if strings.TrimSpace(r.AWB) == "" {
		errs = append(errs, errors.New("awb is required"))
	}
	if strings.TrimSpace(r.CurrentShipmentStatusID) == "" {
		errs = append(errs, errors.New("current shipment status id is required"))
	}
	return errors.Join(errs...)
}

// UpdateTrackingStatusResponse is the envelope returned by the update.
type UpdateTrackingStatusResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
