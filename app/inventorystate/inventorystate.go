// Package inventorystate remembers what the gateway last pushed to the
// origin platform, so a stock sweep sends only what changed.
//
// A sweep reads the vendor's whole catalogue every time; most of it has not
// moved. Pushing all of it would spend the origin platform's rate limit on
// writes that change nothing.
//
// This is a cache of the gateway's own writes, never a source of truth. It
// can be wrong in one direction — a run that wrote the platform and then
// failed before recording here leaves the table behind — which is exactly
// why a periodic full push ignores it. Treating it as authoritative would
// make that drift permanent.
package inventorystate

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Entry is one SKU's last pushed quantity.
type Entry struct {
	GluzoSKU string
	// Quantity is the figure that was sent, after the safety buffer and the
	// platform's cap were applied. Storing the sent figure rather than the
	// vendor's is what makes the comparison meaningful: two different vendor
	// quantities that clamp to the same sent value are the same write.
	Quantity int
	PushedAt time.Time
}

// Key identifies one origin-side stock position.
type Key struct {
	IntegrationID uuid.UUID
	// OriginReference is the origin location the quantity was written to.
	// Empty is a legitimate value meaning the process default.
	OriginReference string
}

// Store reads and records pushed quantities.
type Store interface {
	// Load returns the last pushed quantity per SKU for one origin location.
	Load(ctx context.Context, key Key) (map[string]int, error)
	// Record stores what a push sent. Entries replace what is held.
	Record(ctx context.Context, key Key, entries []Entry) error
	// Forget removes the recorded quantities for one origin location, so the
	// next sweep pushes everything. An operator's repair tool.
	Forget(ctx context.Context, key Key) error
}

// Changed reports which levels differ from what was last pushed.
//
// A SKU with no recorded quantity is treated as changed: the gateway has
// never pushed it, so it cannot know the platform agrees.
func Changed(last map[string]int, current []Entry) []Entry {
	out := make([]Entry, 0, len(current))
	for _, e := range current {
		if prev, ok := last[normalise(e.GluzoSKU)]; ok && prev == e.Quantity {
			continue
		}
		out = append(out, e)
	}
	return out
}

// normalise matches SKUs the way the rest of the gateway does, so a code that
// changes case between runs is not mistaken for a changed quantity.
func normalise(sku string) string { return strings.ToLower(strings.TrimSpace(sku)) }

// MemoryStore is an in-memory Store for tests.
type MemoryStore struct {
	mu   sync.Mutex
	data map[Key]map[string]Entry
}

// NewMemoryStore builds an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[Key]map[string]Entry)}
}

// Load implements Store.
func (m *MemoryStore) Load(_ context.Context, key Key) (map[string]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int, len(m.data[key]))
	for sku, e := range m.data[key] {
		out[sku] = e.Quantity
	}
	return out, nil
}

// Record implements Store.
func (m *MemoryStore) Record(_ context.Context, key Key, entries []Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[key] == nil {
		m.data[key] = make(map[string]Entry, len(entries))
	}
	for _, e := range entries {
		m.data[key][normalise(e.GluzoSKU)] = e
	}
	return nil
}

// Forget implements Store.
func (m *MemoryStore) Forget(_ context.Context, key Key) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}
