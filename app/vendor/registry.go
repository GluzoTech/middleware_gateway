package vendor

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Role names a capability a vendor adapter may implement.
type Role string

// Roles.
const (
	RoleOrderReceiver      Role = "ORDER_RECEIVER"
	RoleStockProvider      Role = "STOCK_PROVIDER"
	RoleFulfilmentProvider Role = "FULFILMENT_PROVIDER"
)

// Registry holds the vendor adapters a process has been wired with, indexed
// by platform name and by the roles each one implements.
//
// Adding a fulfilment partner is one Register call: the adapter declares its
// capabilities by which interfaces it satisfies, and the registry discovers
// them. Nothing in the workflows, the queue, the retry machinery or the
// execution log changes.
//
// A Registry is safe for concurrent use.
type Registry struct {
	mu          sync.RWMutex
	orders      map[string]OrderReceiver
	stock       map[string]StockProvider
	fulfilments map[string]FulfilmentProvider
	names       map[string]struct{}
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		orders:      make(map[string]OrderReceiver),
		stock:       make(map[string]StockProvider),
		fulfilments: make(map[string]FulfilmentProvider),
		names:       make(map[string]struct{}),
	}
}

// Register files v under every role it implements.
//
// An adapter implementing no role is rejected. Such a vendor would resolve
// at routing time and then fail at the first action; catching it during
// wiring turns a production incident into a start-up error.
func (r *Registry) Register(v Vendor) error {
	if v == nil {
		return fmt.Errorf("vendor: cannot register a nil adapter")
	}
	name := strings.TrimSpace(v.Platform())
	if name == "" {
		return fmt.Errorf("vendor: adapter reports an empty platform name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.names[name]; exists {
		return fmt.Errorf("vendor: %q is already registered", name)
	}

	var roles int
	if a, ok := v.(OrderReceiver); ok {
		r.orders[name] = a
		roles++
	}
	if a, ok := v.(StockProvider); ok {
		r.stock[name] = a
		roles++
	}
	if a, ok := v.(FulfilmentProvider); ok {
		r.fulfilments[name] = a
		roles++
	}
	if roles == 0 {
		return fmt.Errorf("vendor: %q implements no role; it must satisfy at least one of OrderReceiver, StockProvider or FulfilmentProvider", name)
	}
	r.names[name] = struct{}{}
	return nil
}

// MustRegister panics when Register fails. Intended for process wiring,
// where a misconfiguration should stop start-up.
func (r *Registry) MustRegister(v Vendor) {
	if err := r.Register(v); err != nil {
		panic(err)
	}
}

// OrderReceiver returns the adapter that accepts orders for platform.
func (r *Registry) OrderReceiver(platform string) (OrderReceiver, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.orders[strings.TrimSpace(platform)]
	if !ok {
		return nil, r.missingLocked(platform, RoleOrderReceiver)
	}
	return a, nil
}

// StockProvider returns the adapter that publishes stock for platform.
func (r *Registry) StockProvider(platform string) (StockProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.stock[strings.TrimSpace(platform)]
	if !ok {
		return nil, r.missingLocked(platform, RoleStockProvider)
	}
	return a, nil
}

// FulfilmentProvider returns the adapter that publishes dispatch information
// for platform.
func (r *Registry) FulfilmentProvider(platform string) (FulfilmentProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.fulfilments[strings.TrimSpace(platform)]
	if !ok {
		return nil, r.missingLocked(platform, RoleFulfilmentProvider)
	}
	return a, nil
}

// Has reports whether platform is registered at all.
func (r *Registry) Has(platform string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.names[strings.TrimSpace(platform)]
	return ok
}

// Platforms lists every registered vendor, sorted.
func (r *Registry) Platforms() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.platformsLocked()
}

func (r *Registry) platformsLocked() []string {
	out := make([]string, 0, len(r.names))
	for name := range r.names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Roles lists the roles platform implements, sorted. An unregistered
// platform yields nil.
func (r *Registry) Roles(platform string) []Role {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rolesLocked(strings.TrimSpace(platform))
}

func (r *Registry) rolesLocked(platform string) []Role {
	var out []Role
	if _, ok := r.orders[platform]; ok {
		out = append(out, RoleOrderReceiver)
	}
	if _, ok := r.fulfilments[platform]; ok {
		out = append(out, RoleFulfilmentProvider)
	}
	if _, ok := r.stock[platform]; ok {
		out = append(out, RoleStockProvider)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// missingLocked explains what is configured, so that a wiring mistake
// surfaces as a readable error rather than a nil dereference. The caller
// holds at least a read lock.
func (r *Registry) missingLocked(platform string, role Role) error {
	platform = strings.TrimSpace(platform)
	if _, known := r.names[platform]; known {
		return fmt.Errorf("vendor: %q is registered but does not implement %s (it implements %v)", platform, role, r.rolesLocked(platform))
	}
	names := r.platformsLocked()
	if len(names) == 0 {
		return fmt.Errorf("vendor: no vendor adapters are registered; %q cannot be resolved", platform)
	}
	return fmt.Errorf("vendor: no adapter for %q; registered: %s", platform, strings.Join(names, ", "))
}
