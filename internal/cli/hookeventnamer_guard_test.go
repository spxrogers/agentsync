package cli

import (
	"encoding/json"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestHookEventNamer_CoversEveryRenamedPointer is the registry-wide guard for
// adapter.HookEventNamer: import's stale-hook retirement disowns each retired
// event's "/hooks/<name>" state keys by its alias set — the canonical spelling
// plus every HookEventNamer's native spelling. A future adapter that RENAMES a
// hook event in its owned pointers but forgets to implement NativeHookEvent
// would silently escape that disown, and the next apply's orphan cleanup would
// delete the event from the user's native config. This test renders a canonical
// command hook through every registered adapter (both scopes) and asserts every
// emitted "/hooks/<X>" child key is either the canonical event name or the
// adapter's own declared native spelling — so the renaming-without-declaring
// state cannot exist.
func TestHookEventNamer_CoversEveryRenamedPointer(t *testing.T) {
	testenv.RequireContainer(t)
	const event = "PreToolUse"
	fixture := source.Canonical{Hooks: []source.Hook{{
		Event:   event,
		Matcher: "Bash",
		Type:    "command",
		Command: "echo hi",
	}}}
	resolved := secrets.ForRender(fixture)

	reg := registryFactory()
	hookEmitters := map[string]int{}
	for _, name := range reg.Names() {
		a := reg.Lookup(name)
		t.Run(name, func(t *testing.T) {
			for _, sc := range []struct {
				scope   adapter.Scope
				project string
			}{{adapter.ScopeUser, ""}, {adapter.ScopeProject, t.TempDir()}} {
				ops, _, err := a.Render(resolved, sc.scope, sc.project)
				if err != nil {
					t.Fatalf("render (%v): %v", sc.scope, err)
				}
				for _, op := range ops {
					var top map[string]any
					if json.Unmarshal(op.Content, &top) != nil {
						continue // non-JSON content (markdown, TOML replace files)
					}
					hooks, ok := top["hooks"].(map[string]any)
					if !ok {
						continue
					}
					hookEmitters[name]++
					want := map[string]bool{event: true}
					if namer, ok := a.(adapter.HookEventNamer); ok {
						if native, ok := namer.NativeHookEvent(event); ok {
							want[native] = true
						}
					}
					for child := range hooks {
						if !want[child] {
							t.Errorf("scope %v: adapter emits /hooks/%s for canonical %s without "+
								"declaring it via adapter.HookEventNamer — import's retirement "+
								"disown cannot see this key, and the next apply's orphan cleanup "+
								"would delete the event from the user's native config", sc.scope, child, event)
						}
					}
				}
			}
		})
	}
	// Vacuity guard: assert the four hook-emitting agents BY NAME at both
	// scopes (claude/codex under the canonical spelling, gemini/cursor
	// renamed) — a bare count could be satisfied by claude+codex alone,
	// leaving the guard vacuous for exactly the renaming adapters it exists
	// to check.
	for _, agent := range []string{"claude", "codex", "gemini", "cursor"} {
		if hookEmitters[agent] < 2 {
			t.Fatalf("agent %s emitted a hooks section in %d renders; expected both scopes — the guard went vacuous for it", agent, hookEmitters[agent])
		}
	}
}
