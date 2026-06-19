package cli

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestEveryAdapterClassifiesSkips is the exhaustiveness guard the typed-Kind
// refactor (issue #98) installs in place of the old "-frontmatter" string
// convention. The reduced-vs-dropped distinction is now a typed field every skip
// site MUST set explicitly (adapter.SkipKind); its zero value, SkipKindUnset, is
// invalid. A new adapter or a new skip site that forgets to classify its Kind
// leaves it SkipKindUnset, which compiles fine and no other test would catch — so
// here we render every REGISTERED adapter (the same set apply uses, via
// registryFactory) over a component-complete fixture at both scopes and fail if
// any emitted skip is unclassified.
//
// The fixture deliberately carries reduced-triggering frontmatter (a subagent
// with tools/color, a command with argument-hint/allowed-tools) AND
// whole-component drops (skill/hook/lsp on agents with no such concept), so the
// run also asserts both Kinds are actually exercised — the guard can't pass
// vacuously by triggering no skips.
func TestEveryAdapterClassifiesSkips(t *testing.T) {
	// Component-complete canonical. The frontmatter keys are the ones the deep
	// adapters drop with a field-level reduction (so reduced sites fire); the
	// skill/hook/lsp components have no native home on several agents (so dropped
	// sites fire). SessionEnd is an event no adapter maps.
	base := source.Canonical{
		Memory:     source.Memory{Body: "remember this\n"},
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Type: "stdio", Command: "npx"}}},
		Skills:     []source.Skill{{Name: "demo", Frontmatter: map[string]any{"name": "demo"}, Body: "x\n"}},
		Subagents: []source.Subagent{{
			Name:        "review",
			Frontmatter: map[string]any{"description": "Code review", "tools": []any{"Read", "Grep"}, "color": "blue"},
			Body:        "Review.\n",
		}},
		Commands: []source.Command{{
			Name:        "summarize",
			Frontmatter: map[string]any{"description": "Summarize", "argument-hint": "<text>", "allowed-tools": "Read"},
			Body:        "Summarize.\n",
		}},
		Hooks: []source.Hook{
			{Event: "PreToolUse", Matcher: "*", Type: "command", Command: "echo hi"},
			{Event: "SessionEnd", Matcher: "*", Type: "command", Command: "echo bye"},
		},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
	}

	// At project scope the scope-aware adapters render the Project overlay rather
	// than the merged canonical — populate it with the same components so their
	// project-scope skip sites fire too.
	proj := base
	proj.Project = nil
	withProject := base
	withProject.Project = &proj

	reg := registryFactory()

	var (
		offenders   []string
		sawReduced  bool
		sawDropped  bool
		totalSkips  int
		renderCases = []struct {
			scope   adapter.Scope
			project string
			model   source.Canonical
		}{
			{adapter.ScopeUser, "", base},
			{adapter.ScopeProject, t.TempDir(), withProject},
		}
	)

	for _, name := range reg.Names() {
		a := reg.Lookup(name)
		for _, rc := range renderCases {
			_, skips, err := a.Render(secrets.ForRender(rc.model), rc.scope, rc.project)
			if err != nil {
				t.Fatalf("%s.Render(scope=%s): %v", name, rc.scope, err)
			}
			for _, s := range skips {
				totalSkips++
				switch s.Kind {
				case adapter.SkipReduced:
					sawReduced = true
				case adapter.SkipDropped:
					sawDropped = true
				default:
					offenders = append(offenders, name+" ["+rc.scope.String()+"]: "+
						s.Component+" "+s.Name+" — Kind is unset (set adapter.SkipReduced or adapter.SkipDropped at the skip site)")
				}
			}
		}
	}

	for _, o := range offenders {
		t.Errorf("unclassified skip: %s", o)
	}
	// Guard against a vacuous pass: if the fixture stopped triggering skips, the
	// "no unclassified skip" assertion above would be meaningless.
	if totalSkips == 0 {
		t.Fatal("no skips emitted by any adapter — the fixture no longer exercises skip sites")
	}
	if !sawReduced {
		t.Error("no SkipReduced observed — the reduced (field-level) sites are not exercised by the fixture")
	}
	if !sawDropped {
		t.Error("no SkipDropped observed — the dropped (whole-component) sites are not exercised by the fixture")
	}
}
