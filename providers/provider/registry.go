package provider

import (
	"fmt"
	"sort"
	"sync"
)

// Registry is the per-provider strategy registry (spec §11): the one place
// the platform looks providers up by name. Registration is where the
// capability matrix is structurally enforced — an adapter with an invalid
// matrix never becomes visible. It is a value passed where needed, not a
// package global, so tests compose freely.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register adds a provider. It rejects empty or duplicate names and any
// matrix that fails Validate — required behaviors missing, unknown rows,
// no working transport.
func (r *Registry) Register(p Provider) error {
	name := p.Name()
	if name == "" {
		return fmt.Errorf("provider has no name")
	}
	if err := p.Matrix().Validate(); err != nil {
		return fmt.Errorf("provider %q: %w", name, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.providers[name]; dup {
		return fmt.Errorf("provider %q already registered", name)
	}
	r.providers[name] = p
	return nil
}

// Get returns the named provider.
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	return p, nil
}

// Names lists registered providers, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
