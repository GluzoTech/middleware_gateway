// Package skumap translates between Gluzo's SKUs and a vendor's item codes.
//
// Gluzo sells BCPL's products under Gluzo's own codes; BCPL hold them under
// theirs. Neither system knows about the other's, so the correspondence is
// configuration an operator manages, never a rule in code. Mappings are
// scoped to an integration, so two vendors may use the same item code without
// colliding and a mapping can never be used by a pipeline it was not
// configured for.
//
// Both directions matter. Stock arrives under the vendor's code and is
// published under Gluzo's; an order is placed under Gluzo's code and sent
// under the vendor's.
//
// An unmapped SKU is an error, never a skip. Quietly dropping one during a
// stock sweep publishes nothing for it, and a storefront that shows no update
// looks exactly like a storefront that is up to date.
package skumap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Statuses.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// Sentinel errors.
var (
	// ErrUnmapped reports a SKU with no active mapping. Callers must treat
	// it as permanent: no amount of retrying will configure a mapping.
	ErrUnmapped = errors.New("skumap: no active mapping for sku")

	ErrNotFound        = errors.New("skumap: not found")
	ErrAlreadyExists   = errors.New("skumap: mapping already exists")
	ErrInvalidArgument = errors.New("skumap: invalid argument")
)

// Mapping is one configured correspondence.
type Mapping struct {
	ID            uuid.UUID
	IntegrationID uuid.UUID
	// GluzoSKU is the code the storefront and EasyEcom know.
	GluzoSKU string
	// VendorSKU is the code the vendor knows.
	VendorSKU string
	// SafetyBuffer is a quantity withheld from the storefront as protection
	// against oversell: the vendor's stock moves through channels the
	// gateway cannot see, so the figure read is always slightly stale.
	SafetyBuffer int
	Status       string
	CreatedAt    time.Time
}

// Publishable applies the safety buffer to a quantity read from the vendor.
//
// Zero is propagated immediately with no buffer applied. An item the vendor
// has none of is out of stock now, and subtracting a buffer from nothing to
// get nothing would be the same answer reached more slowly. More to the
// point, applying a buffer to a small quantity must never turn "one left"
// into a negative: the result is clamped.
func (m Mapping) Publishable(vendorQuantity int) int {
	if vendorQuantity <= 0 {
		return 0
	}
	n := vendorQuantity - m.SafetyBuffer
	if n < 0 {
		return 0
	}
	return n
}

// Validate reports every invariant the mapping violates.
func (m Mapping) Validate() error {
	var errs []error
	if strings.TrimSpace(m.GluzoSKU) == "" {
		errs = append(errs, errors.New("gluzo sku is required"))
	}
	if strings.TrimSpace(m.VendorSKU) == "" {
		errs = append(errs, errors.New("vendor sku is required"))
	}
	if m.SafetyBuffer < 0 {
		errs = append(errs, errors.New("safety buffer must not be negative"))
	}
	switch m.Status {
	case StatusActive, StatusDisabled, "":
	default:
		errs = append(errs, fmt.Errorf("unknown status %q", m.Status))
	}
	return errors.Join(errs...)
}

// Reader loads mappings. The workflow depends on this, not on the store.
type Reader interface {
	// Active returns every active mapping for an integration.
	//
	// A sweep translates a whole page at once, so it loads the set and
	// indexes it rather than asking per SKU: one query for a catalogue
	// beats one query per line, and the set is small enough to hold.
	Active(ctx context.Context, integrationID uuid.UUID) ([]Mapping, error)
}

// Index is an in-memory lookup over a set of mappings, in both directions.
type Index struct {
	byVendor map[string]Mapping
	byGluzo  map[string]Mapping
}

// NewIndex builds an index. Disabled mappings are excluded: a disabled SKU
// is one an operator chose to stop syncing, and including it would sync it.
//
// Lookups are case-insensitive. Vendors are inconsistent about the case of
// item codes, and a mapping that fails because a code arrived lower-cased
// would read as an unmapped SKU — a configuration error that is not one.
func NewIndex(mappings []Mapping) *Index {
	idx := &Index{
		byVendor: make(map[string]Mapping, len(mappings)),
		byGluzo:  make(map[string]Mapping, len(mappings)),
	}
	for _, m := range mappings {
		if m.Status == StatusDisabled {
			continue
		}
		idx.byVendor[key(m.VendorSKU)] = m
		idx.byGluzo[key(m.GluzoSKU)] = m
	}
	return idx
}

// Len reports how many mappings the index holds.
func (i *Index) Len() int { return len(i.byVendor) }

// ToGluzo translates a vendor item code. The error names the code, because
// the operator's next action is to add exactly that mapping.
func (i *Index) ToGluzo(vendorSKU string) (Mapping, error) {
	m, ok := i.byVendor[key(vendorSKU)]
	if !ok {
		return Mapping{}, fmt.Errorf("%w: vendor sku %q", ErrUnmapped, strings.TrimSpace(vendorSKU))
	}
	return m, nil
}

// ToVendor translates a Gluzo SKU.
func (i *Index) ToVendor(gluzoSKU string) (Mapping, error) {
	m, ok := i.byGluzo[key(gluzoSKU)]
	if !ok {
		return Mapping{}, fmt.Errorf("%w: gluzo sku %q", ErrUnmapped, strings.TrimSpace(gluzoSKU))
	}
	return m, nil
}

func key(sku string) string { return strings.ToLower(strings.TrimSpace(sku)) }
