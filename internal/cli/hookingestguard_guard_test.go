package cli

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestHookIngestGuard_ReportsCanonicalNames is the registry-wide guard for
// the CANONICAL-SPELLING half of the adapter.HookIngestGuard contract
// (round-1 test-rigor finding: it was promised on the interface but pinned
// only by per-adapter tables). For every registered adapter implementing the
// guard, it renders a canonical command hook into the adapter's own native
// file, asserts a clean render refuses nothing, then enriches every emitted
// handler object with an unmodeled "timeout" and asserts the refusal
// surfaces as exactly ["PreToolUse"] — a renaming adapter that reported its
// native spelling (preToolUse, BeforeTool) fails the equality.
//
// The contract's OTHER half — a native-only event is never returned — cannot
// be pinned here: the fixture renders a canonical event, so no native-only
// event ever exists in the file to leak. That half stays pinned per-adapter
// (the "-only event is never refused" rows in each guard table); a future
// implementor gets the spelling check free and must add its own native-only
// row. Injection walks whatever decodes (every object carrying a "command"
// key gains the field) and understands the two native formats currently
// emitted, JSON and TOML; an implementor with a third format fails loudly at
// the no-hook-file-found Fatal below rather than passing vacuously.
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
			// unmodeled field, as a user would — decoding whichever of the two
			// emitted native formats (JSON, TOML) the file speaks and re-marshaling
			// in kind.
			injected := false
			for _, op := range ops {
				data, rerr := os.ReadFile(op.Path)
				if rerr != nil {
					continue
				}
				var top map[string]any
				marshal := func(v map[string]any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
				if json.Unmarshal(data, &top) != nil {
					top = map[string]any{}
					if toml.Unmarshal(data, &top) != nil {
						continue // neither JSON nor TOML — not this adapter's hook home
					}
					marshal = func(v map[string]any) ([]byte, error) { return toml.Marshal(v) }
				}
				if _, ok := top["hooks"]; !ok {
					continue
				}
				injectUnmodeledField(top)
				out, merr := marshal(top)
				if merr != nil {
					t.Fatal(merr)
				}
				if werr := os.WriteFile(op.Path, out, 0o644); werr != nil {
					t.Fatal(werr)
				}
				injected = true
			}
			if !injected {
				t.Fatal("no JSON or TOML hooks file found to enrich — the guard went vacuous for this adapter")
			}
			refused, err = g.RefusedHookEvents(adapter.ScopeUser, "")
			if err != nil {
				t.Fatalf("RefusedHookEvents (enriched): %v", err)
			}
			if len(refused) != 1 || refused[0] != event {
				t.Fatalf("enriched event must be refused as exactly [%q] (the CANONICAL "+
					"spelling import retires); got %v — an empty list means the refusal "+
					"was lost entirely, and a native spelling would make retirement "+
					"delete nothing while the stale canonical file keeps the next apply "+
					"rewriting the user's native entry", event, refused)
			}
		})
	}
	// Vacuity guard: the four adapters whose hook ingests refuse semantically
	// must implement HookIngestGuard BY NAME — losing one silently reopens the
	// issue #124 second-order clobber for that agent.
	for _, agent := range []string{"claude", "gemini", "cursor", "codex"} {
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
