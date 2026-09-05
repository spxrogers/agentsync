package render_test

import (
	"path/filepath"
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
		Action:        adapter.ActionWrite,
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

// TestPlan_RejectsTraversalInProjectOverlay exercises the ComponentIDs `c.Project`
// recursion (internal/secrets) end-to-end: a traversal-bearing id present ONLY in
// the project overlay still refuses the whole plan. The model is DELIBERATELY
// synthetic — in the production flow project.Merge (overlayByKey) always mirrors
// every project id into the top-level slices too, so the top-level loop would
// already catch this id and the recursion is defense-in-depth, not the sole guard.
// The test pins that defense-in-depth branch: if a future caller ever fed Plan an
// unmerged project-only model, a traversal id there must still be rejected, never
// rendered as a write-anywhere FileOp.
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
			Action:        adapter.ActionWrite,
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

// TestPlan_ZeroActionIsWriteAtTheGuards pins that the containment backstop
// cannot be dodged by leaving Action unset. Before Action was typed, "" was a
// documented write default that every executor wrote while the backstop and
// the dedup/divergence check matched the literal "write" — so Plan rewrote ""
// at intake, and this test pinned the rewrite. The rewrite is gone because
// there is nothing left to rewrite: the zero value IS adapter.ActionWrite. The
// property that survives is the one that mattered — there is no spelling that
// writes while dodging a guard — so the ops here are built WITHOUT an Action on
// purpose (do not "tidy" them to an explicit ActionWrite: the point is that the
// zero value writes, and swapping the iota order so the zero value is
// ActionDelete must fail this test).
func TestPlan_ZeroActionIsWriteAtTheGuards(t *testing.T) {
	t.Run("zero-Action traversal op is refused", func(t *testing.T) {
		reg := adapter.NewRegistry()
		esc := &countingAdapter{
			Adapter: noop.New("claude"),
			renderOps: []adapter.FileOp{{
				// No Action: the zero value writes — and must not dodge the backstop.
				Path:          "../escape.md",
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
			t.Fatal("Plan accepted a zero-Action FileOp whose path escapes via '..'; want rejection")
		}
		if !strings.Contains(err.Error(), "escapes its destination") {
			t.Errorf("error %q does not describe the containment failure", err.Error())
		}
	})
	t.Run("zero Action reads back as ActionWrite in the plan", func(t *testing.T) {
		reg := adapter.NewRegistry()
		a := &countingAdapter{
			Adapter: noop.New("claude"),
			renderOps: []adapter.FileOp{{
				// No Action on purpose: the zero value is the write.
				Path:          "/tmp/dest/file.md",
				Content:       []byte("x"),
				Mode:          0o644,
				MergeStrategy: "replace",
			}},
		}
		if err := reg.Register(a); err != nil {
			t.Fatal(err)
		}
		plan, err := render.Plan(secrets.ForRender(source.Canonical{}), reg, []string{"claude"}, adapter.ScopeUser, "", nil, "/tmp")
		if err != nil {
			t.Fatal(err)
		}
		ops := plan.PerAgent["claude"].Ops
		if len(ops) != 1 || ops[0].Action != adapter.ActionWrite {
			t.Fatalf("a zero-Action op must be an ActionWrite in the plan, got %+v", ops)
		}
	})
}

// TestApply_ZeroActionForCallerBuiltPlans pins the same property at the OTHER
// exported entries: Apply and PreviewApply accept a caller-built RenderPlan
// that never went through Plan and re-run the containment backstop there. The
// ops are built without an Action on purpose — the zero value writes — so a
// hand-built plan carrying an escaping zero-Action op must be refused exactly
// like Plan refuses it, and a benign zero-Action op must reach the adapter as
// an ActionWrite (there is no intake rewrite left to make it one).
func TestApply_ZeroActionForCallerBuiltPlans(t *testing.T) {
	planFor := func(op adapter.FileOp) render.RenderPlan {
		return render.RenderPlan{PerAgent: map[string]render.AgentResult{
			"claude": {Ops: []adapter.FileOp{op}},
		}}
	}
	escaping := adapter.FileOp{
		// No Action: the zero value writes — hand-built, never seen by Plan.
		Path:          "../escape.md",
		Content:       []byte("x"),
		Mode:          0o644,
		MergeStrategy: "replace",
	}

	entries := []struct {
		name string
		run  func(p render.RenderPlan, reg *adapter.Registry) error
	}{
		{"Apply", func(p render.RenderPlan, reg *adapter.Registry) error {
			_, _, _, err := render.Apply(p, reg, state.New(), t.TempDir(), t.TempDir(), adapter.ScopeUser, "")
			return err
		}},
		{"PreviewApply", func(p render.RenderPlan, reg *adapter.Registry) error {
			_, _, _, err := render.PreviewApply(p, reg, state.New(), t.TempDir(), t.TempDir(), adapter.ScopeUser, "")
			return err
		}},
	}
	for _, e := range entries {
		t.Run(e.name+"/zero-Action traversal op is refused", func(t *testing.T) {
			a := &countingAdapter{Adapter: noop.New("claude")}
			reg := adapter.NewRegistry()
			if err := reg.Register(a); err != nil {
				t.Fatal(err)
			}
			err := e.run(planFor(escaping), reg)
			if err == nil {
				t.Fatalf("%s accepted a zero-Action FileOp whose path escapes via '..'; want refusal", e.name)
			}
			if !strings.Contains(err.Error(), "escapes its destination") {
				t.Errorf("error %q does not describe the containment failure", err.Error())
			}
			if len(a.ops) != 0 {
				t.Errorf("adapter Apply ran despite the refusal; got ops %+v", a.ops)
			}
		})
	}
	t.Run("benign zero Action reaches the adapter as ActionWrite", func(t *testing.T) {
		a := &countingAdapter{Adapter: noop.New("claude")}
		reg := adapter.NewRegistry()
		if err := reg.Register(a); err != nil {
			t.Fatal(err)
		}
		benign := escaping
		benign.Path = filepath.Join(t.TempDir(), "dest", "file.md")
		if _, _, _, err := render.Apply(planFor(benign), reg, state.New(), t.TempDir(), t.TempDir(), adapter.ScopeUser, ""); err != nil {
			t.Fatal(err)
		}
		if len(a.ops) != 1 || a.ops[0].Action != adapter.ActionWrite {
			t.Fatalf("a zero-Action op must reach the adapter as ActionWrite, got %+v", a.ops)
		}
	})
}

// TestPipeline_OwnedKeysInjected verifies that Plan injects OwnedKeys from
// state.Keys into merge-json-keys ops for the matching agent+scope+project+path.
func TestPipeline_OwnedKeysInjected(t *testing.T) {
	home := "/tmp/fake-root"
	destPath := "/tmp/fake-root/.claude.json"
	mergeOp := adapter.FileOp{
		Action:        adapter.ActionWrite,
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
	key := state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude.json", Pointer: "/mcpServers/github"}
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
