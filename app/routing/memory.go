package routing

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// MemoryResolver is an in-memory Resolver for tests.
type MemoryResolver struct {
	mu     sync.RWMutex
	routes map[string]Resolution // keyed by integrationID|type|value
}

// NewMemoryResolver returns an empty resolver.
func NewMemoryResolver() *MemoryResolver {
	return &MemoryResolver{routes: make(map[string]Resolution)}
}

// Add registers r; a disabled route is stored but never resolved.
func (m *MemoryResolver) Add(r Resolution) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Route.Status == "" {
		r.Route.Status = StatusActive
	}
	m.routes[memoryKey(r.IntegrationID, Key{Type: r.Route.Type, Value: r.Route.Value})] = r
}

// Resolve implements Resolver.
func (m *MemoryResolver) Resolve(_ context.Context, integrationID uuid.UUID, key Key) (*Resolution, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.routes[memoryKey(integrationID, key)]
	if !ok || r.Route.Status != StatusActive {
		return nil, ErrNoRoute
	}
	out := r
	return &out, nil
}

func memoryKey(integrationID uuid.UUID, key Key) string {
	return integrationID.String() + "|" + key.Type + "|" + key.Value
}
