// Package shipmentstate remembers the furthest dispatch state the gateway
// has pushed for each package.
//
// A shipment's status must never move backwards, and out-of-order arrival is
// ordinary under dropship: the gateway polls the vendor on a schedule, so a
// sweep can read "delivered" before it has ever read "shipped". Deciding
// whether an incoming notice is an advance needs a record of what was sent.
//
// The record is kept here rather than read back from the origin platform.
// The origin's own tracking read is unverified and, under dropship, holds
// only what this gateway put there — asking it would be asking ourselves
// through a slower and less reliable route.
package shipmentstate

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/domain/tracking"
)

// Key identifies one package's dispatch record.
type Key struct {
	IntegrationID uuid.UUID
	// OrderExternalID is the origin platform's order identifier.
	OrderExternalID string
	// PackageCode distinguishes packages when an order ships in several.
	// Empty is legitimate: most orders ship as one.
	PackageCode string
}

// Normalised returns the key with its text fields in comparable form.
func (k Key) Normalised() Key {
	k.OrderExternalID = strings.TrimSpace(k.OrderExternalID)
	k.PackageCode = strings.TrimSpace(k.PackageCode)
	return k
}

// Record is what was last pushed for a package.
type Record struct {
	Status tracking.Status
	// Progress is stored alongside the label so that a comparison does not
	// depend on the reading code agreeing with the row about what a status
	// name means. A status renamed in code still compares correctly against
	// rows written before the rename.
	Progress       int
	TrackingNumber string
	Carrier        string
	PushedAt       time.Time
}

// Store reads and records pushed dispatch state.
type Store interface {
	// Get returns what was last pushed. found is false when nothing has
	// been pushed for this package.
	Get(ctx context.Context, key Key) (Record, bool, error)
	// Record stores what a push sent.
	Record(ctx context.Context, key Key, rec Record) error
}

// Decision is what a sink should do with an incoming shipment.
type Decision struct {
	// Push reports whether anything should be sent at all.
	Push bool
	// AdvanceStatus reports whether the status itself moves forward. False
	// with Push true means details changed but the status did not: a late
	// arrival may correct a tracking number while leaving a more advanced
	// status alone.
	AdvanceStatus bool
	// Reason explains a decision not to push, for the log.
	Reason string
}

// Decide compares an incoming shipment against what was last pushed.
//
// Three outcomes matter:
//
//   - Nothing pushed before: send everything.
//   - The status advances: send everything.
//   - The status does not advance: send nothing unless the tracking number
//     or carrier changed, in which case send the details and leave the
//     status alone. A redelivered event carrying exactly what was already
//     sent is a no-op, which is what makes a sweep safe to repeat.
func Decide(previous Record, found bool, incoming tracking.Shipment) Decision {
	if !found {
		return Decision{Push: true, AdvanceStatus: true}
	}
	if incoming.Status.IsAdvanceOver(previous.Status) {
		return Decision{Push: true, AdvanceStatus: true}
	}

	detailsChanged := changed(previous.TrackingNumber, incoming.TrackingNumber) ||
		changed(previous.Carrier, incoming.Carrier)
	if detailsChanged {
		return Decision{Push: true, AdvanceStatus: false}
	}
	return Decision{
		Reason: "already at " + string(previous.Status) + " with the same dispatch details",
	}
}

// changed reports whether a non-empty incoming value differs from what was
// held. An incoming blank is not a change: a sweep that returns a shipment
// without its tracking number must not erase the one already sent.
func changed(previous, incoming string) bool {
	incoming = strings.TrimSpace(incoming)
	return incoming != "" && !strings.EqualFold(incoming, strings.TrimSpace(previous))
}

// FromShipment builds the record to store for a shipment that was pushed.
//
// keepStatus preserves the held status for a details-only push, so correcting
// a tracking number cannot quietly roll a delivered order back to shipped.
func FromShipment(previous Record, s tracking.Shipment, advanceStatus bool, at time.Time) Record {
	rec := Record{
		Status:         previous.Status,
		Progress:       previous.Progress,
		TrackingNumber: preferNonEmpty(s.TrackingNumber, previous.TrackingNumber),
		Carrier:        preferNonEmpty(s.Carrier, previous.Carrier),
		PushedAt:       at,
	}
	if advanceStatus {
		rec.Status = s.Status
		rec.Progress = s.Status.Progress()
	}
	return rec
}

func preferNonEmpty(incoming, previous string) string {
	if v := strings.TrimSpace(incoming); v != "" {
		return v
	}
	return strings.TrimSpace(previous)
}

// MemoryStore is an in-memory Store for tests.
type MemoryStore struct {
	mu      sync.Mutex
	records map[Key]Record
}

// NewMemoryStore builds an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[Key]Record)}
}

// Get implements Store.
func (m *MemoryStore) Get(_ context.Context, key Key) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[key.Normalised()]
	return rec, ok, nil
}

// Record implements Store.
func (m *MemoryStore) Record(_ context.Context, key Key, rec Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[key.Normalised()] = rec
	return nil
}
