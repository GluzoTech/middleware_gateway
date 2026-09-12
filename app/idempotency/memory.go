package idempotency

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for tests and single-process use. It
// applies the same semantics as the PostgreSQL store.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	Now     func() time.Time
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]Record), Now: time.Now}
}

// Claim implements Store.
func (m *MemoryStore) Claim(_ context.Context, rec Record) (Claim, error) {
	if rec.Key == "" {
		return Claim{}, fmt.Errorf("idempotency: key is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.records[rec.Key]; ok {
		e := existing
		return Claim{Accepted: false, Existing: &e}, nil
	}
	if rec.Status == "" {
		rec.Status = StatusAccepted
	}
	rec.CreatedAt = m.Now()
	m.records[rec.Key] = rec
	return Claim{Accepted: true}, nil
}

// Release implements Store.
func (m *MemoryStore) Release(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, key)
	return nil
}

// SetStatus implements Store.
func (m *MemoryStore) SetStatus(_ context.Context, key string, status Status) error {
	if !validStatus(status) {
		return fmt.Errorf("idempotency: invalid status %q", status)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.records[key]
	if !ok {
		return ErrNotFound
	}
	rec.Status = status
	if status == StatusCompleted || status == StatusFailed {
		now := m.Now()
		rec.ProcessedAt = &now
	}
	m.records[key] = rec
	return nil
}

// Get implements Store.
func (m *MemoryStore) Get(_ context.Context, key string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[key]
	if !ok {
		return nil, ErrNotFound
	}
	out := rec
	return &out, nil
}

// DeleteOlderThan implements Store.
func (m *MemoryStore) DeleteOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for key, rec := range m.records {
		if rec.CreatedAt.Before(cutoff) {
			delete(m.records, key)
			n++
		}
	}
	return n, nil
}
