// Package tracking defines Gluzo's internal representation of shipment
// tracking information.
package tracking

import (
	"errors"
	"strings"
	"time"
)

// Status is the normalised delivery state of a shipment.
type Status string

// Shipment statuses.
const (
	StatusPending        Status = "PENDING"
	StatusShipped        Status = "SHIPPED"
	StatusInTransit      Status = "IN_TRANSIT"
	StatusOutForDelivery Status = "OUT_FOR_DELIVERY"
	StatusDelivered      Status = "DELIVERED"
	StatusReturned       Status = "RETURNED"
	StatusCancelled      Status = "CANCELLED"
	StatusUnknown        Status = "UNKNOWN"
)

// Shipment is the tracking view of one package belonging to an order.
type Shipment struct {
	// OrderExternalID is the source platform's identifier of the order the
	// package belongs to.
	OrderExternalID string
	// PackageCode identifies the package when an order ships in several.
	PackageCode    string
	Carrier        string
	TrackingNumber string
	TrackingURL    string
	Status         Status
	SourceStatus   string
	ShippedAt      *time.Time
	// DeliveredAt is when the carrier recorded delivery. Separate from
	// UpdatedAt, which is when the gateway observed the record.
	DeliveredAt *time.Time
	UpdatedAt   time.Time
	// InvoiceNumber is the invoice the vendor raised for this package. Under
	// dropship the vendor invoices the customer under its own registration,
	// so the number originates outside Gluzo.
	InvoiceNumber string
	// SellerGSTIN is the tax registration the package was invoiced under.
	// It is the vendor's, not Gluzo's, and is carried so the origin platform
	// can present the correct seller on the customer's invoice.
	SellerGSTIN string
}

// Validate reports every invariant the shipment violates.
func (s Shipment) Validate() error {
	var errs []error
	if strings.TrimSpace(s.OrderExternalID) == "" {
		errs = append(errs, errors.New("order external id is required"))
	}
	if strings.TrimSpace(s.TrackingNumber) == "" && strings.TrimSpace(s.Carrier) == "" {
		errs = append(errs, errors.New("tracking number or carrier is required"))
	}
	return errors.Join(errs...)
}

// Progress orders the statuses by how far a dispatch has got.
//
// It exists so that "a shipment's status never moves backwards" can be
// stated once and enforced everywhere. Out-of-order arrival is ordinary
// under dropship: the gateway polls the vendor on a schedule and a sweep can
// easily read "delivered" before it ever read "shipped".
//
// Returned and cancelled rank above delivered deliberately. An RTO or a
// cancellation reported after a delivery notice is the vendor correcting
// itself, and the vendor owns dispatch — a parcel that came back is later
// news than a parcel that was said to arrive.
//
// Unknown ranks lowest so that an unrecognised label never overwrites a
// status the gateway did understand.
func (s Status) Progress() int {
	switch s {
	case StatusPending:
		return 10
	case StatusShipped:
		return 20
	case StatusInTransit:
		return 30
	case StatusOutForDelivery:
		return 40
	case StatusDelivered:
		return 50
	case StatusReturned, StatusCancelled:
		return 60
	default:
		return 0
	}
}

// IsAdvanceOver reports whether s is later in the dispatch than previous.
//
// Equal statuses are not an advance: a redelivered event carrying the status
// already held is a no-op, which is what makes a sweep safe to repeat.
func (s Status) IsAdvanceOver(previous Status) bool {
	return s.Progress() > previous.Progress()
}
