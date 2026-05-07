package render_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/noop"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/source"
)

// countingAdapter wraps noop but records every Apply call.
type countingAdapter struct {
	*noop.Adapter
	ops []adapter.FileOp
	// renderOps are returned verbatim from Render.
	renderOps []adapter.FileOp
}

func (c *countingAdapter) Render(source.Canonical, adapter.Scope, string) ([]adapter.FileOp, []adapter.Skip, error) {
	return c.renderOps, nil, nil
}
func (c *countingAdapter) Apply(ops []adapter.FileOp) error {
	c.ops = append(c.ops, ops...)
	return nil
}

func TestPipeline_PlanEmpty(t *testing.T) {
	reg := adapter.NewRegistry()
	_ = reg.Register(noop.New("claude"))
	_ = reg.Register(noop.New("opencode"))

	plan, err := render.Plan(source.Canonical{}, reg, []string{"claude", "opencode"}, adapter.ScopeUser, "")
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
	_, err := render.Plan(source.Canonical{}, reg, []string{"missing"}, adapter.ScopeUser, "")
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

	if err := render.Apply(plan, reg); err != nil {
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
