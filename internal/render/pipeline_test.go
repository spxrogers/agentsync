package render_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/noop"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/source"
)

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
