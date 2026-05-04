package cli

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/noop"
)

// registryFactory returns an adapter.Registry. Production wires real
// adapters; tests can override via setRegistryFactory.
var registryFactory = func() *adapter.Registry {
	r := adapter.NewRegistry()
	// M0: only NoopAdapters under each known name
	for _, name := range []string{"claude", "opencode", "codex", "cursor"} {
		_ = r.Register(noop.New(name))
	}
	return r
}
