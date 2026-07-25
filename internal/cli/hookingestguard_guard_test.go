package cli

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestHookIngestGuard_ReportsCanonicalNames is the registry-wide guard for the
// adapter.HookIngestGuard contract (round-1 test-rigor finding: the contract —
// "event names are always CANONICAL; a native-only event is never returned" —
// was promised on the interface but pinned only by per-adapter tables). For
// every registered adapter implementing the guard, it renders a canonical
// command hook into the adapter's own native file, asserts a clean render
// refuses nothing, then enriches every emitted handler object with an
// unmodeled "timeout" and asserts the refusal surfaces as exactly
// ["PreToolUse"] — a renaming adapter that reported its native spelling
// (preToolUse, BeforeTool) fails the equality, as does one leaking a
// native-only event. Enrichment is injected format-agnostically (every JSON
// object carrying a "command" key gains the field), so a future implementor
// with a different native hook shape is covered without editing this test.
func TestHookIngestGuard_ReportsCanonicalNames(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	t.Setenv("AGENTSYNC_TARGET_ROOT", tmp)

	const event = "PreToolUse"
	fixture := source.Canonical{Hooks: []source.Hook{{
		Event:   event,
		Matcher: "Bash",
		Type:    "command",
		Command: "echo hi",
	}}}
	resolved := secrets.ForRender(fixture)

	reg := registryFactory()
	guards := map[string]bool{}
	for _, name := range reg.Names() {
		a := reg.Lookup(name)
		g, ok := a.(adapter.HookIngestGuard)
		if !ok {
			continue
		}
		guards[name] = true
		t.Run(name, func(t *testing.T) {
			ops, _, err := a.Render(resolved, adapter.ScopeUser, "")
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
				t.Fatalf("apply: %v", err)
			}
			refused, err := g.RefusedHookEvents(adapter.ScopeUser, "")
			if err != nil {
				t.Fatalf("RefusedHookEvents (clean): %v", err)
			}
			if len(refused) != 0 {
				t.Fatalf("clean render must refuse nothing, got %v", refused)
			}
			// Enrich every handler object in the emitted native file(s) with an
			// unmodeled field, as a user would.
			injected := false
			for _, op := range ops {
				data, rerr := os.ReadFile(op.Path)
				if rerr != nil {
					continue
				}
				var top map[string]any
				if json.Unmarshal(data, &top) != nil {
					continue // non-JSON native file — not this adapter's hook home
				}
				if _, ok := top["hooks"]; !ok {
					continue
				}
				injectUnmodeledField(top)
				out, merr := json.MarshalIndent(top, "", "  ")
				if merr != nil {
					t.Fatal(merr)
				}
				if werr := os.WriteFile(op.Path, out, 0o644); werr != nil {
					t.Fatal(werr)
				}
				injected = true
			}
			if !injected {
				t.Fatal("no JSON hooks file found to enrich — the guard went vacuous for this adapter")
			}
			refused, err = g.RefusedHookEvents(adapter.ScopeUser, "")
			if err != nil {
				t.Fatalf("RefusedHookEvents (enriched): %v", err)
			}
			if len(refused) != 1 || refused[0] != event {
				t.Fatalf("enriched event must be refused as exactly [%q] (the CANONICAL "+
					"spelling import retires); got %v — a native spelling here would make "+
					"retirement delete nothing while the stale canonical file keeps the "+
					"next apply rewriting the user's native entry", event, refused)
			}
		})
	}
	// Vacuity guard: the three adapters whose hook ingests refuse semantically
	// must implement HookIngestGuard BY NAME — losing one silently reopens the
	// issue #124 second-order clobber for that agent.
	for _, agent := range []string{"claude", "gemini", "cursor"} {
		if !guards[agent] {
			t.Fatalf("agent %s no longer implements adapter.HookIngestGuard — its native "+
				"hook enrichments would silently stop triggering import's stale-hook retirement", agent)
		}
	}
}

// injectUnmodeledField walks any decoded JSON value and adds an unmodeled
// "timeout" field to every object that carries a "command" key — the handler
// objects, wherever an adapter's native hook shape nests them.
func injectUnmodeledField(v any) {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["command"]; ok {
			x["timeout"] = 30
		}
		for _, child := range x {
			injectUnmodeledField(child)
		}
	case []any:
		for _, child := range x {
			injectUnmodeledField(child)
		}
	}
}
