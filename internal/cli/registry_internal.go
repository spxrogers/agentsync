package cli

import (
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/adapter/cline"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
	"github.com/spxrogers/agentsync/internal/adapter/continuedev"
	"github.com/spxrogers/agentsync/internal/adapter/cursor"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
	"github.com/spxrogers/agentsync/internal/adapter/generic"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
	"github.com/spxrogers/agentsync/internal/adapter/roo"
	"github.com/spxrogers/agentsync/internal/adapter/windsurf"
	"github.com/spxrogers/agentsync/internal/paths"
)

// registryFactory returns an adapter.Registry wired with the production
// adapters. Tests reassign this var directly to inject a stub registry. Every
// valid agent (see validateAgent) now has a real adapter — there are no noop
// placeholders.
//
// A Register error only ever means a name collision (two registrations claiming
// the same Name()) — a wiring bug, never a user-recoverable runtime condition —
// so mustRegister panics on it rather than silently letting the first
// registration win and mis-resolving Lookup for that agent. The subset guard in
// registry_internal_test.go pins this at CI time; the panic is the runtime
// backstop. See #160.
var registryFactory = func() *adapter.Registry {
	r := adapter.NewRegistry()
	home := paths.HomeDir(paths.OSEnv{})
	mustRegister := func(a adapter.Adapter) {
		if err := r.Register(a); err != nil {
			panic(fmt.Errorf("agentsync: adapter registry wiring bug: %w", err))
		}
	}
	mustRegister(claude.New(claude.Options{TargetRoot: home}))
	mustRegister(opencode.New(opencode.Options{TargetRoot: home}))
	mustRegister(codex.New(codex.Options{TargetRoot: home}))
	mustRegister(cursor.New(cursor.Options{TargetRoot: home}))
	mustRegister(gemini.New(gemini.Options{TargetRoot: home}))
	mustRegister(continuedev.New(continuedev.Options{TargetRoot: home}))
	mustRegister(windsurf.New(windsurf.Options{TargetRoot: home}))
	mustRegister(roo.New(roo.Options{TargetRoot: home}))
	mustRegister(cline.New(cline.Options{TargetRoot: home}))
	// Breadth tier: one generic adapter per verified Spec (memory + optional MCP).
	for _, spec := range generic.Specs() {
		mustRegister(generic.New(spec, generic.Options{TargetRoot: home}))
	}
	return r
}
