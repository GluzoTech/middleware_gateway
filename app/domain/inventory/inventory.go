// Package inventory defines Gluzo's internal representation of stock levels.
package inventory

import (
	"errors"
	"strings"
	"time"
)

// Level is the stock position of one SKU in one warehouse.
type Level struct {
	SKU string
	// WarehouseID identifies the warehouse in the source platform's terms.
	WarehouseID string
	// Available is the sellable quantity.
	Available int
	// Reserved is the quantity committed to open orders, when the platform
	// reports it.
	Reserved  int
	UpdatedAt time.Time
}

// Validate reports every invariant the level violates.
func (l Level) Validate() error {
	var errs []error
	if strings.TrimSpace(l.SKU) == "" {
		errs = append(errs, errors.New("sku is required"))
	}
	if l.Available < 0 {
		errs = append(errs, errors.New("available quantity must not be negative"))
	}
	if l.Reserved < 0 {
		errs = append(errs, errors.New("reserved quantity must not be negative"))
	}
	return errors.Join(errs...)
}
