package adapter

import (
	"fmt"
	"sort"
)

// Registry is an in-memory map of adapter name -> Adapter.
type Registry struct {
	items map[string]Adapter
}

func NewRegistry() *Registry {
	return &Registry{items: map[string]Adapter{}}
}

// Register adds a; returns error if name collides.
func (r *Registry) Register(a Adapter) error {
	name := a.Name()
	if _, ok := r.items[name]; ok {
		return fmt.Errorf("adapter %q already registered", name)
	}
	r.items[name] = a
	return nil
}

// Lookup returns the adapter for name, or nil.
func (r *Registry) Lookup(name string) Adapter { return r.items[name] }

// Names returns adapter names in deterministic order (sorted).
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.items))
	for n := range r.items {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
