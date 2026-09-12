// Package workflowstate provides workflow.Repository implementations: an
// atomic file-based repository for production and an in-memory one for
// tests. Both round-trip state through JSON so that anything a test stores
// is guaranteed to be serialisable.
package workflowstate

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gluzo/integration-gateway/app/workflow"
)

// MemoryRepository keeps states in a map.
type MemoryRepository struct {
	mu     sync.Mutex
	states map[string][]byte
	// Saves counts Save calls, so tests can assert persistence points.
	Saves int
}

// NewMemoryRepository returns an empty repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{states: make(map[string][]byte)}
}

// Get implements workflow.Repository.
func (m *MemoryRepository) Get(_ context.Context, correlationID string) (*workflow.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.states[correlationID]
	if !ok {
		return nil, workflow.ErrStateNotFound
	}
	var st workflow.State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("workflowstate: decode: %w", err)
	}
	return &st, nil
}

// Save implements workflow.Repository.
func (m *MemoryRepository) Save(_ context.Context, state *workflow.State) error {
	if state == nil || state.CorrelationID == "" {
		return fmt.Errorf("workflowstate: state needs a correlation id")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("workflowstate: encode: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[state.CorrelationID] = data
	m.Saves++
	return nil
}

// ListActive implements workflow.Repository.
func (m *MemoryRepository) ListActive(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id, data := range m.states {
		var st workflow.State
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("workflowstate: decode: %w", err)
		}
		if !st.Status.IsFinal() {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
