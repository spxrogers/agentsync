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
	hookEmitters := 0
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
					hookEmitters++
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
	// Vacuity guard: claude, codex, gemini, and cursor each emit a hooks
	// section for this fixture at BOTH scopes (8 emitters today — claude/codex
	// under the canonical spelling, gemini/cursor renamed). Require all 8 so a
	// single renaming adapter silently dropping out of the walk cannot leave
	// the guard vacuously green for exactly the agent it exists to check.
	if hookEmitters < 8 {
		t.Fatalf("fixture exercised only %d hook-emitting renders; expected >= 8 (4 agents x 2 scopes) — the guard went partially vacuous", hookEmitters)
	}
}
