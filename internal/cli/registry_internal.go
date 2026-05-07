package cli

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/adapter/noop"
	"github.com/spxrogers/agentsync/internal/paths"
)

// registryFactory returns an adapter.Registry. Production wires real
// adapters; tests can override via setRegistryFactory.
var registryFactory = func() *adapter.Registry {
	r := adapter.NewRegistry()
	home := paths.HomeDir(paths.OSEnv{})
	_ = r.Register(claude.New(claude.Options{TargetRoot: home}))
	for _, name := range []string{"opencode", "codex", "cursor"} {
		_ = r.Register(noop.New(name))
	}
	return r
}
