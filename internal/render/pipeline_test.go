package render_test

import (
	"strings"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/noop"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// countingAdapter wraps noop but records every Apply call.
type countingAdapter struct {
	*noop.Adapter
	ops []adapter.FileOp
	// renderOps are returned verbatim from Render.
	renderOps []adapter.FileOp
}

func (c *countingAdapter) Render(secrets.Resolved, adapter.Scope, string) ([]adapter.FileOp, []adapter.Skip, error) {
	return c.renderOps, nil, nil
}

func (c *countingAdapter) Apply(ops []adapter.FileOp, _ adapter.DestWriter) error {
	c.ops = append(c.ops, ops...)
	return nil
}

func TestPipeline_PlanEmpty(t *testing.T) {
	reg := adapter.NewRegistry()
	_ = reg.Register(noop.New("claude"))
	_ = reg.Register(noop.New("opencode"))

	plan, err := render.Plan(secrets.ForRender(source.Canonical{}), reg, []string{"claude", "opencode"}, adapter.ScopeUser, "", nil, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Total() != 0 {
		t.Fatalf("expected empty plan, got %+v", plan)
	}
	if len(plan.PerAgent) != 2 {
		t.Fatalf("expected per-agent entries for both agents")
	}
}

func TestPipeline_UnknownAgentError(t *testing.T) {
	reg := adapter.NewRegistry()
	_ = reg.Register(noop.New("claude"))
	_, err := render.Plan(secrets.ForRender(source.Canonical{}), reg, []string{"missing"}, adapter.ScopeUser, "", nil, "/tmp")
	if err == nil {
		t.Fatal("expected error for unknown adapter")
	}
}

// TestPipeline_DedupesIdenticalWritesAcrossAdapters verifies that when two
// adapters emit a "write" op for the same path (e.g. shared skill SKILL.md),
// Apply only passes it to the first adapter — the second adapter gets an empty
// list for that path.
func TestPipeline_DedupesIdenticalWritesAcrossAdapters(t *testing.T) {
	sharedPath := "/tmp/fake-root/.claude/skills/my-skill/SKILL.md"
	sharedOp := adapter.FileOp{
		Action:        "write",
		Path:          sharedPath,
		Content:       []byte("# My skill\n"),
		Mode:          0o644,
		SourceID:      "skills/my-skill/SKILL.md",
		MergeStrategy: "replace",
	}

	a1 := &countingAdapter{Adapter: noop.New("claude"), renderOps: []adapter.FileOp{sharedOp}}
	a2 := &countingAdapter{Adapter: noop.New("opencode"), renderOps: []adapter.FileOp{sharedOp}}

	reg := adapter.NewRegistry()
	_ = reg.Register(a1)
	_ = reg.Register(a2)

	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude":   {Ops: a1.renderOps},
			"opencode": {Ops: a2.renderOps},
		},
	}

	if _, _, _, err := render.Apply(plan, reg, state.New(), t.TempDir(), t.TempDir(), adapter.ScopeUser, ""); err != nil {
		t.Fatal(err)
	}

	totalWrites := 0
	for _, a := range []*countingAdapter{a1, a2} {
		for _, op := range a.ops {
			if op.Path == sharedPath {
				totalWrites++
			}
		}
	}
	if totalWrites != 1 {
		t.Fatalf("expected exactly 1 write to shared path across adapters, got %d", totalWrites)
	}
}

// TestPlan_RejectsTraversalComponentName pins the Render-time path-traversal
// guard at its own layer, independent of the full adapter registry. Plan must
// refuse any plan whose component Name (subagent/command/skill) fails
// source.ValidateComponentID — path separators, "..", absolute, bare ".",
// all-whitespace, AND control/deceptive runes — proving it delegates to the
// shared sanitizer (untrusted.Sanitize) rather than a hand-rolled "..-only"
// check that would miss the rune cases. A legitimate name still renders.
func TestPlan_RejectsTraversalComponentName(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"legitimate", "review", false},
		{"parent-traversal", "../../../tmp/x", true},
		{"slash", "a/b", true},
		{"backslash", `a\b`, true},
		{"absolute", "/etc/passwd", true},
		{"bare-dot", ".", true},
		{"whitespace", "  ", true},
		{"bidi-override", "name\u202egnp", true}, // RLO — delegation-to-Sanitize proof
		{"esc-recolor", "good\x1b[31m", true},    // ESC/CSI — delegation-to-Sanitize proof
		{"zero-width", "na\u200bme", true},       // ZWSP — delegation-to-Sanitize proof
	}

	// Each component kind that becomes a filename stem must be guarded.
	kinds := []struct {
		kind  string
		build func(id string) source.Canonical
	}{
		{"subagent", func(id string) source.Canonical {
			return source.Canonical{Subagents: []source.Subagent{{Name: id, Body: "b\n"}}}
		}},
		{"command", func(id string) source.Canonical {
			return source.Canonical{Commands: []source.Command{{Name: id, Body: "b\n"}}}
		}},
		{"skill", func(id string) source.Canonical {
			return source.Canonical{Skills: []source.Skill{{Name: id, Frontmatter: map[string]any{"name": "s"}, Body: "b\n"}}}
		}},
		// MCP/LSP server ids also become a destination filename stem for adapters
		// that write one file per server (continuedev joins id into
		// filepath.Join(MCPDir, id+".yaml")). A marketplace-projected id is a raw
		// manifest map key with no traversal check of its own, so the dispatch-waist
		// guard MUST validate these ids too — a plugin declaring an mcpServers key
		// like "../../../etc/x" would otherwise render a write-anywhere FileOp.
		{"mcp", func(id string) source.Canonical {
			return source.Canonical{MCPServers: []source.MCPServer{{ID: id, Server: source.MCPServerSpec{Type: "stdio", Command: "x"}}}}
		}},
		{"lsp", func(id string) source.Canonical {
			return source.Canonical{LSPServers: []source.LSPServer{{ID: id, Spec: source.LSPServerSpec{Command: "x"}}}}
		}},
	}

	for _, k := range kinds {
		for _, tc := range cases {
			t.Run(k.kind+"/"+tc.name, func(t *testing.T) {
				reg := adapter.NewRegistry()
				if err := reg.Register(noop.New("claude")); err != nil {
					t.Fatal(err)
				}
				model := k.build(tc.id)
				_, err := render.Plan(secrets.ForRender(model), reg, []string{"claude"}, adapter.ScopeUser, "", nil, "/tmp")
				if tc.wantErr != (err != nil) {
					t.Fatalf("Plan(%s=%q) err=%v; wantErr=%v", k.kind, tc.id, err, tc.wantErr)
				}
				// The error must name the component kind + id and (via the wrapped
				// ValidateComponentID error) explain why — so an operator can locate
				// the offending component. It is deliberately agent-AGNOSTIC: the id
				// check is hoisted out of the per-agent loop (a traversal id is
				// model-wide, not one agent's fault), so it names "render:" + the
				// component, not any single agent.
				if tc.wantErr {
					msg := err.Error()
					if !strings.Contains(msg, "render:") || !strings.Contains(msg, "component "+k.kind) {
						t.Errorf("error %q missing render/kind context (want 'render:' + 'component %s')", msg, k.kind)
					}
				}
			})
		}
	}
}

// TestPlan_RejectsTraversalInProjectOverlay proves the id-validation recursion is
// wired end-to-end: a traversal-bearing id living ONLY in the project overlay (the
// top-level model is clean) still refuses the whole plan. This ties the
// ComponentIDs `c.Project` recursion (internal/secrets) to the render-time guard —
// a regression that stopped descending into the overlay would render a
// write-anywhere FileOp for a project-only component.
func TestPlan_RejectsTraversalInProjectOverlay(t *testing.T) {
	reg := adapter.NewRegistry()
	if err := reg.Register(noop.New("claude")); err != nil {
		t.Fatal(err)
	}
	proj := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "../../../etc/x", Server: source.MCPServerSpec{Type: "stdio", Command: "x"}}},
	}
	model := source.Canonical{Project: &proj}
	_, err := render.Plan(secrets.ForRender(model), reg, []string{"claude"}, adapter.ScopeProject, "/tmp/proj", nil, "/tmp")
	if err == nil {
		t.Fatal("Plan accepted a traversal MCP id living only in the project overlay")
	}
	if msg := err.Error(); !strings.Contains(msg, "render:") || !strings.Contains(msg, "component mcp") {
		t.Errorf("error %q missing render/kind context (want 'render:' + 'component mcp')", msg)
	}
}

// TestPlan_ContainmentBackstopRejectsEscapingFileOp exercises the second,
// path-based guard directly: a stub adapter emits a write op whose path still
// contains a surviving ".." after cleaning, while the model carries no component
// ids at all (so the primary id check passes). The backstop must still refuse it
// — this is the defense-in-depth layer that also covers bundled skill-file paths,
// which are not single component ids.
func TestPlan_ContainmentBackstopRejectsEscapingFileOp(t *testing.T) {
	reg := adapter.NewRegistry()
	esc := &countingAdapter{
		Adapter: noop.New("claude"),
		renderOps: []adapter.FileOp{{
			Action:        "write",
			Path:          "../escape.md", // filepath.Clean keeps the leading ".."
			Content:       []byte("x"),
			Mode:          0o644,
			MergeStrategy: "replace",
		}},
	}
	if err := reg.Register(esc); err != nil {
		t.Fatal(err)
	}
	_, err := render.Plan(secrets.ForRender(source.Canonical{}), reg, []string{"claude"}, adapter.ScopeUser, "", nil, "/tmp")
	if err == nil {
		t.Fatal("Plan accepted a FileOp whose path escapes via '..'; want rejection")
	}
	if !strings.Contains(err.Error(), "escapes its destination") {
		t.Errorf("error %q does not describe the containment failure", err.Error())
	}
}

// TestPipeline_OwnedKeysInjected verifies that Plan injects OwnedKeys from
// state.Keys into merge-json-keys ops for the matching agent+scope+project+path.
func TestPipeline_OwnedKeysInjected(t *testing.T) {
	home := "/tmp/fake-root"
	destPath := "/tmp/fake-root/.claude.json"
	mergeOp := adapter.FileOp{
		Action:        "write",
		Path:          destPath,
		Content:       []byte(`{"mcpServers":{"github":{}}}`),
		Mode:          0o644,
		MergeStrategy: "merge-json-keys",
	}

	a := &countingAdapter{Adapter: noop.New("claude"), renderOps: []adapter.FileOp{mergeOp}}
	reg := adapter.NewRegistry()
	_ = reg.Register(a)

	// Pre-populate state with one owned pointer in HOME-relative form.
	s := state.New()
	key := "claude:user::${HOME}/.claude.json:/mcpServers/github"
	s.Keys[key] = state.KeyEntry{SHA256: "abc123", AppliedAt: time.Now()}

	plan, err := render.Plan(secrets.ForRender(source.Canonical{}), reg, []string{"claude"}, adapter.ScopeUser, "", s, home)
	if err != nil {
		t.Fatal(err)
	}
	res := plan.PerAgent["claude"]
	if len(res.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(res.Ops))
	}
	op := res.Ops[0]
	if len(op.OwnedKeys) != 1 || op.OwnedKeys[0] != "/mcpServers/github" {
		t.Fatalf("expected OwnedKeys=[/mcpServers/github], got %v", op.OwnedKeys)
	}
}
