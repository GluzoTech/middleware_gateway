package workflow

import (
	"fmt"
	"sync"
)

// Registry maps workflow names and event types to workflows.
type Registry struct {
	mu      sync.RWMutex
	byName  map[string]Workflow
	byEvent map[string]string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Workflow), byEvent: make(map[string]string)}
}

// Register adds wf and binds it to eventTypes. Names must be unique and an
// event type can be bound to one workflow only.
func (r *Registry) Register(wf Workflow, eventTypes ...string) error {
	if wf == nil || wf.Name() == "" {
		return fmt.Errorf("workflow: cannot register an unnamed workflow")
	}
	if err := validateSteps(wf.Steps()); err != nil {
		return fmt.Errorf("workflow %s: %w", wf.Name(), err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[wf.Name()]; exists {
		return fmt.Errorf("workflow: %q is already registered", wf.Name())
	}
	for _, et := range eventTypes {
		if existing, bound := r.byEvent[et]; bound {
			return fmt.Errorf("workflow: event %s is already bound to %q", et, existing)
		}
	}
	r.byName[wf.Name()] = wf
	for _, et := range eventTypes {
		r.byEvent[et] = wf.Name()
	}
	return nil
}

// ByName returns the workflow registered under name.
func (r *Registry) ByName(name string) (Workflow, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	wf, ok := r.byName[name]
	return wf, ok
}

// ForEvent returns the workflow bound to eventType.
func (r *Registry) ForEvent(eventType string) (Workflow, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	name, ok := r.byEvent[eventType]
	if !ok {
		return nil, false
	}
	wf, ok := r.byName[name]
	return wf, ok
}
