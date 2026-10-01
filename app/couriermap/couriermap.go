// Package couriermap translates a vendor's carrier name into the origin
// platform's carrier identifier.
//
// Vinculum names a carrier with a free-text `transporter` string. EasyEcom
// identifies one with a numeric id it assigns when the carrier is registered
// on the account. Neither knows the other's, so the correspondence is
// operator configuration, never a table in code — carriers are added and
// dropped commercially, and a code change per carrier would guarantee the
// mapping is out of date.
//
// An unmapped carrier is an error, not a skip, for the same reason an
// unmapped SKU is: a dispatch quietly dropped leaves a customer with no
// tracking and leaves nothing in the log saying why.
package couriermap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	// ErrUnmapped reports a carrier with no active mapping. Permanent: no
	// amount of retrying will register the carrier.
	ErrUnmapped = errors.New("couriermap: no active mapping for carrier")

	ErrNotFound        = errors.New("couriermap: not found")
	ErrAlreadyExists   = errors.New("couriermap: mapping already exists")
	ErrInvalidArgument = errors.New("couriermap: invalid argument")
)

// Courier is one configured correspondence.
type Courier struct {
	ID            uuid.UUID
	IntegrationID uuid.UUID
	// Transporter is the carrier name as the vendor reports it.
	Transporter string
	// CompanyCarrierID is the origin platform's own identifier, obtained
	// once the carrier is registered on the account.
	CompanyCarrierID string
	// Name is what to present to the origin platform where it differs from
	// the vendor's spelling. Empty sends the vendor's.
	Name      string
	Status    string
	CreatedAt time.Time
}

// PresentedName returns the carrier name to send onward.
func (c Courier) PresentedName() string {
	if v := strings.TrimSpace(c.Name); v != "" {
		return v
	}
	return strings.TrimSpace(c.Transporter)
}

// Validate reports every invariant the mapping violates.
func (c Courier) Validate() error {
	var errs []error
	if strings.TrimSpace(c.Transporter) == "" {
		errs = append(errs, errors.New("transporter is required"))
	}
	if strings.TrimSpace(c.CompanyCarrierID) == "" {
		errs = append(errs, errors.New("company carrier id is required"))
	}
	switch c.Status {
	case StatusActive, StatusDisabled, "":
	default:
		errs = append(errs, fmt.Errorf("unknown status %q", c.Status))
	}
	return errors.Join(errs...)
}

// Reader loads mappings. The workflow depends on this, not on the store.
type Reader interface {
	Active(ctx context.Context, integrationID uuid.UUID) ([]Courier, error)
}

// Index is an in-memory lookup over a set of mappings.
type Index struct {
	byTransporter map[string]Courier
}

// NewIndex builds an index, excluding disabled mappings.
//
// Lookups are case-insensitive and ignore surrounding space: carriers arrive
// spelled inconsistently — "Blue Dart", "BLUEDART", "bluedart" — and a
// mapping that failed on case would report a configuration error that is not
// one.
func NewIndex(couriers []Courier) *Index {
	idx := &Index{byTransporter: make(map[string]Courier, len(couriers))}
	for _, c := range couriers {
		if c.Status == StatusDisabled {
			continue
		}
		idx.byTransporter[key(c.Transporter)] = c
	}
	return idx
}

// Len reports how many mappings the index holds.
func (i *Index) Len() int { return len(i.byTransporter) }

// Lookup translates a vendor carrier name. The error names the carrier,
// because registering exactly that one is the operator's next action.
func (i *Index) Lookup(transporter string) (Courier, error) {
	c, ok := i.byTransporter[key(transporter)]
	if !ok {
		return Courier{}, fmt.Errorf("%w: %q", ErrUnmapped, strings.TrimSpace(transporter))
	}
	return c, nil
}

func key(name string) string {
	// Spaces are removed as well as folded, so "Blue Dart" and "BlueDart"
	// are the same carrier. They always are.
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), " ", "")
}

// MemoryReader is an in-memory Reader for tests.
type MemoryReader struct {
	mu       sync.RWMutex
	couriers map[uuid.UUID][]Courier
}

// NewMemoryReader builds an empty reader.
func NewMemoryReader() *MemoryReader {
	return &MemoryReader{couriers: make(map[uuid.UUID][]Courier)}
}

// Set replaces the mappings for an integration.
func (m *MemoryReader) Set(integrationID uuid.UUID, couriers []Courier) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.couriers[integrationID] = append([]Courier(nil), couriers...)
}

// Active implements Reader.
func (m *MemoryReader) Active(_ context.Context, integrationID uuid.UUID) ([]Courier, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Courier
	for _, c := range m.couriers[integrationID] {
		if c.Status == StatusDisabled {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}
