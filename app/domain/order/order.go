// Package order defines Gluzo's internal representation of a customer order.
//
// The domain knows nothing about EasyEcom, Vinculum or any other platform:
// mappers in the integration packages translate to and from these types.
// Fields carry no external JSON names; serialisation for workflow state uses
// Go field names.
package order

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// PaymentMode is how the customer paid.
type PaymentMode string

// Payment modes.
const (
	PaymentPrepaid        PaymentMode = "PREPAID"
	PaymentCashOnDelivery PaymentMode = "COD"
	PaymentUnknown        PaymentMode = "UNKNOWN"
)

// Status is the normalised lifecycle state of an order.
type Status string

// Order statuses.
const (
	StatusPending   Status = "PENDING"
	StatusConfirmed Status = "CONFIRMED"
	StatusShipped   Status = "SHIPPED"
	StatusDelivered Status = "DELIVERED"
	StatusCancelled Status = "CANCELLED"
	StatusReturned  Status = "RETURNED"
	StatusUnknown   Status = "UNKNOWN"
)

// Order is a customer order as Gluzo understands it.
type Order struct {
	// ExternalID is the source platform's primary identifier for the order.
	ExternalID string
	// InvoiceNumber is the source platform's invoice identifier, if any.
	InvoiceNumber string
	// ReferenceCode is the customer-facing or marketplace order reference.
	ReferenceCode string
	// Channel is the marketplace or sales channel the order came from.
	Channel string
	// WarehouseID identifies the source warehouse; it is the routing key.
	WarehouseID string

	Status       Status
	SourceStatus string // the platform's own status label, kept for traceability
	OrderedAt    time.Time

	Customer        Customer
	ShippingAddress Address
	BillingAddress  Address

	PaymentMode     PaymentMode
	Currency        string
	ShippingCharges float64
	Discount        float64
	TotalAmount     float64

	Items []Item
}

// Validate reports every invariant the order violates.
func (o Order) Validate() error {
	var errs []error
	if strings.TrimSpace(o.ExternalID) == "" {
		errs = append(errs, errors.New("external id is required"))
	}
	if len(o.Items) == 0 {
		errs = append(errs, errors.New("at least one item is required"))
	}
	for i, item := range o.Items {
		if err := item.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("item %d: %w", i, err))
		}
	}
	if o.TotalAmount < 0 {
		errs = append(errs, errors.New("total amount must not be negative"))
	}
	return errors.Join(errs...)
}

// TotalQuantity is the number of units across all items.
func (o Order) TotalQuantity() int {
	var n int
	for _, item := range o.Items {
		n += item.Quantity
	}
	return n
}

// IsCashOnDelivery reports whether payment is collected on delivery.
func (o Order) IsCashOnDelivery() bool {
	return o.PaymentMode == PaymentCashOnDelivery
}
