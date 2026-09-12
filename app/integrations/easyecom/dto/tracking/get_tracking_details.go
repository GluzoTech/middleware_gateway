// Package tracking holds the EasyEcom shipment tracking contracts.
//
// TODO(VERIFY): the tracking contract could not be confirmed against the
// EasyEcom API reference, which is not machine-readable. The request
// parameters and response field names below follow EasyEcom's public naming
// conventions and MUST be checked against api-docs.easyecom.io before the
// tracking action is relied upon in production.
package tracking

import (
	"errors"
	"net/url"
	"strings"

	"github.com/gluzo/integration-gateway/app/integrations/easyecom/dto"
)

// GetTrackingDetailsRequest selects the shipment to query by any one of the
// supported identifiers.
type GetTrackingDetailsRequest struct {
	InvoiceID     string
	ReferenceCode string
	AWBNumber     string
}

// Validate reports whether the request identifies a shipment.
func (r GetTrackingDetailsRequest) Validate() error {
	if strings.TrimSpace(r.InvoiceID) == "" && strings.TrimSpace(r.ReferenceCode) == "" && strings.TrimSpace(r.AWBNumber) == "" {
		return errors.New("invoice id, reference code or awb number is required")
	}
	return nil
}

// Query renders the request as URL parameters.
func (r GetTrackingDetailsRequest) Query() url.Values {
	q := url.Values{}
	if v := strings.TrimSpace(r.InvoiceID); v != "" {
		q.Set("invoice_id", v)
	}
	if v := strings.TrimSpace(r.ReferenceCode); v != "" {
		q.Set("reference_code", v)
	}
	if v := strings.TrimSpace(r.AWBNumber); v != "" {
		q.Set("awb_number", v)
	}
	return q
}

// GetTrackingDetailsResponse is the envelope returned by the tracking API.
type GetTrackingDetailsResponse struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    []TrackingDetail `json:"data"`
}

// TrackingDetail is the tracking view of one shipment.
type TrackingDetail struct {
	InvoiceID     dto.FlexString `json:"invoice_id"`
	OrderID       dto.FlexString `json:"order_id"`
	ReferenceCode dto.FlexString `json:"reference_code"`
	AWBNumber     dto.FlexString `json:"awb_number"`
	Carrier       string         `json:"carrier"`
	TrackingURL   string         `json:"tracking_url"`
	Status        string         `json:"status"`
	ShippedAt     string         `json:"shipped_at"`
	UpdatedAt     string         `json:"updated_at"`
}
