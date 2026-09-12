package order

import (
	"errors"
	"strings"
)

// Item is one line of an order.
type Item struct {
	// ExternalID is the source platform's line identifier.
	ExternalID string
	SKU        string
	ProductID  string
	Name       string
	Quantity   int
	UnitPrice  float64
	Discount   float64
	// TaxRate is a percentage, e.g. 18 for 18%.
	TaxRate float64
	TaxType string
	// Total is the line total after discount, including tax.
	Total float64
}

// Validate reports every invariant the item violates.
func (i Item) Validate() error {
	var errs []error
	if strings.TrimSpace(i.SKU) == "" {
		errs = append(errs, errors.New("sku is required"))
	}
	if i.Quantity <= 0 {
		errs = append(errs, errors.New("quantity must be positive"))
	}
	if i.UnitPrice < 0 {
		errs = append(errs, errors.New("unit price must not be negative"))
	}
	return errors.Join(errs...)
}
