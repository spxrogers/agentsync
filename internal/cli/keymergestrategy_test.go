package cli

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// keyMergeFixture is a spec-complete canonical config that exercises the
// key-merge surface of every adapter: one MCP server (Claude/OpenCode/Codex/
// Cursor/Gemini/Windsurf/generic all co-own MCP keys in a shared file) and one
// command hook (Claude/Codex/Cursor/Gemini co-own the hooks key). Rendering it
// forces each adapter to emit whatever key-merge FileOps it produces, so the
// test can compare KeyMergeStrategy() against the strategy actually stamped on
// those ops instead of against a hand-built op.
func keyMergeFixture() source.Canonical {
	enabled := true
	return source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "github",
			Server: source.MCPServerSpec{
				Type:    "stdio",
				Command: "npx",
				Args:    []string{"-y", "@modelcontextprotocol/server-github"},
				Env:     map[string]string{"GITHUB_TOKEN": "xyz"},
				Agents:  []string{"*"},
				Enabled: &enabled,
			},
		}},
		Hooks: []source.Hook{{
			Event:   "PreToolUse",
			Matcher: "Bash",
			Type:    "command",
			Command: "echo hi",
		}},
	}
}

// TestKeyMergeStrategy_MatchesEmittedOps is the C3 dual-list guard: an adapter's
// single KeyMergeStrategy() accessor is the *only* input to orphanCleanupOps'
// destructive cleanup write, yet the strategy is separately hand-stamped onto
// each key-merge FileOp in the adapter's Render. If the two drift, a cleanup
// write could decode a JSONC/TOML file as strict JSON and clobber it. This test
// renders a real MCP+hook fixture through every registered adapter and asserts
// the accessor agrees with every emitted key-merge op, for both scopes and all
// 22 generic specs (via the registry loop). It replaces the need for the
// per-adapter accessor/op pins to stay in lockstep manually.
func TestKeyMergeStrategy_MatchesEmittedOps(t *testing.T) {
	testenv.RequireContainer(t)
	fixture := keyMergeFixture()
	resolved := secrets.ForRender(fixture)

	type scopeCase struct {
		name    string
		scope   adapter.Scope
		project string
	}

	reg := registryFactory()
	totalKeyMergeOps := 0

	for _, name := range reg.Names() {
		a := reg.Lookup(name)
		if a == nil {
			t.Fatalf("registry has name %q but Lookup returned nil", name)
		}
		t.Run(name, func(t *testing.T) {
			accessor := a.KeyMergeStrategy()
			cases := []scopeCase{
				{"user", adapter.ScopeUser, ""},
				{"project", adapter.ScopeProject, t.TempDir()},
			}
			for _, sc := range cases {
				ops, _, err := a.Render(resolved, sc.scope, sc.project)
				if err != nil {
					t.Errorf("[%s] Render(%s) errored: %v", name, sc.name, err)
					continue
				}
				distinct := map[string]bool{}
				keyMergeCount := 0
				for _, op := range ops {
					if !render.IsKeyMerge(op.MergeStrategy) {
						continue
					}
					keyMergeCount++
					distinct[op.MergeStrategy] = true
					// (a) every key-merge op must carry the accessor's strategy.
					if op.MergeStrategy != accessor {
						t.Errorf("[%s/%s] key-merge op %q has MergeStrategy %q but "+
							"KeyMergeStrategy() = %q — the accessor feeds orphanCleanupOps' "+
							"destructive write and MUST match every emitted op",
							name, sc.name, op.Path, op.MergeStrategy, accessor)
					}
				}
				totalKeyMergeOps += keyMergeCount
				// (b) an empty accessor must mean the adapter emits no key-merge ops.
				if accessor == "" && keyMergeCount > 0 {
					t.Errorf("[%s/%s] KeyMergeStrategy() = \"\" but the adapter emitted "+
						"%d key-merge op(s) — the accessor claims no key merging",
						name, sc.name, keyMergeCount)
				}
				// (c) single-strategy invariant: at most one distinct key-merge
				// strategy across all ops (orphanCleanupOps applies the single
				// accessor value to every key-merge destination).
				if len(distinct) > 1 {
					t.Errorf("[%s/%s] adapter emitted %d distinct key-merge strategies %v; "+
						"a single KeyMergeStrategy() accessor cannot be exact for all of them",
						name, sc.name, len(distinct), distinct)
				}
			}
		})
	}

	// Guard against a vacuously-green run: the fixture must genuinely drive at
	// least one adapter's key-merge surface, or assertion (a) never fires.
	if totalKeyMergeOps == 0 {
		t.Fatal("fixture exercised no key-merge ops across the whole registry; the " +
			"MCP/hook fixture is not populating any adapter's key-merge destination")
	}
}

// TestAdapters_NeverRenderEmptyObjectForPopulatedSection is an adapter-fidelity
// guard: an adapter that renders a trimmed-"{}" key-merge op for a POPULATED
// canonical section has silently dropped that section (CLAUDE.md, "models must
// stay faithful to their on-disk artifacts"). It renders the same MCP+hook
// fixture as TestKeyMergeStrategy_MatchesEmittedOps through every registered
// adapter at both scopes — the only registry-wide check, so it also covers a
// thinly-tested breadth-tier agent — and asserts none emits it. It is a narrow
// guard: a `{"mcpServers":{}}` render for a populated section passes it.
func TestAdapters_NeverRenderEmptyObjectForPopulatedSection(t *testing.T) {
	testenv.RequireContainer(t)
	fixture := keyMergeFixture()
	resolved := secrets.ForRender(fixture)

	reg := registryFactory()
	totalKeyMergeOps := 0
	for _, name := range reg.Names() {
		a := reg.Lookup(name)
		if a == nil {
			t.Fatalf("registry has name %q but Lookup returned nil", name)
		}
		for _, sc := range []struct {
			name    string
			scope   adapter.Scope
			project string
		}{
			{"user", adapter.ScopeUser, ""},
			{"project", adapter.ScopeProject, t.TempDir()},
		} {
			ops, _, err := a.Render(resolved, sc.scope, sc.project)
			if err != nil {
				t.Errorf("[%s] Render(%s) errored: %v", name, sc.name, err)
				continue
			}
			for _, op := range ops {
				if !render.IsKeyMerge(op.MergeStrategy) {
					continue
				}
				totalKeyMergeOps++
				if strings.TrimSpace(string(op.Content)) == "{}" {
					t.Errorf("[%s/%s] rendered a trimmed-\"{}\" key-merge op for %q "+
						"from a populated fixture — the adapter silently dropped the section",
						name, sc.name, op.Path)
				}
			}
		}
	}
	if totalKeyMergeOps == 0 {
		t.Fatal("fixture exercised no key-merge ops across the whole registry; the guard is vacuous")
	}
}
