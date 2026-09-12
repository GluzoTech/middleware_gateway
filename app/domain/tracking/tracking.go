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
	UpdatedAt      time.Time
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
