package cli

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
	"github.com/spxrogers/agentsync/internal/adapter/cursor"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
	"github.com/spxrogers/agentsync/internal/paths"
)

// registryFactory returns an adapter.Registry. Production wires real
// adapters; tests can override via setRegistryFactory. Every valid agent
// (see validateAgent) now has a real adapter — there are no noop placeholders.
var registryFactory = func() *adapter.Registry {
	r := adapter.NewRegistry()
	home := paths.HomeDir(paths.OSEnv{})
	_ = r.Register(claude.New(claude.Options{TargetRoot: home}))
	_ = r.Register(opencode.New(opencode.Options{TargetRoot: home}))
	_ = r.Register(codex.New(codex.Options{TargetRoot: home}))
	_ = r.Register(cursor.New(cursor.Options{TargetRoot: home}))
	_ = r.Register(gemini.New(gemini.Options{TargetRoot: home}))
	return r
}
