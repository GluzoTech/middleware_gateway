package order

import (
	"errors"
	"net/url"
	"strings"
)

// GetOrderDetailsRequest selects the order to fetch. Exactly one identifier
// is needed; when both are given the invoice ID wins because it is the more
// specific EasyEcom identifier.
//
// VERIFY: query parameter names invoice_id and reference_code follow the
// published Get Order Details (V2) documentation.
type GetOrderDetailsRequest struct {
	InvoiceID     string
	ReferenceCode string
}

// Validate reports whether the request identifies an order.
func (r GetOrderDetailsRequest) Validate() error {
	if strings.TrimSpace(r.InvoiceID) == "" && strings.TrimSpace(r.ReferenceCode) == "" {
		return errors.New("invoice id or reference code is required")
	}
	return nil
}

// Query renders the request as URL parameters.
func (r GetOrderDetailsRequest) Query() url.Values {
	q := url.Values{}
	switch {
	case strings.TrimSpace(r.InvoiceID) != "":
		q.Set("invoice_id", strings.TrimSpace(r.InvoiceID))
	case strings.TrimSpace(r.ReferenceCode) != "":
		q.Set("reference_code", strings.TrimSpace(r.ReferenceCode))
	}
	return q
}
