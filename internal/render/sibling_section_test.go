package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// applyOnce runs the real plan→apply→prune→record cycle the apply command uses.
func applyOnce(t *testing.T, reg *adapter.Registry, c source.Canonical, st *state.Targets, home, userHome string) {
	t.Helper()
	plan, err := render.Plan(secrets.ForRender(c), reg, []string{"claude"}, adapter.ScopeUser, "", st, userHome)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, _, _, err := render.Apply(plan, reg, st, home, userHome, adapter.ScopeUser, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for name, res := range plan.PerAgent {
		render.PruneStaleState(st, userHome, name, adapter.ScopeUser, "", res.Ops)
	}
	for name, res := range plan.PerAgent {
		if err := render.RecordOpsState(st, userHome, name, adapter.ScopeUser, "", res.Ops); err != nil {
			t.Fatalf("RecordOpsState: %v", err)
		}
	}
}

func readSettings(t *testing.T, tmp string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tmp, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	return m
}

func TestApply_ClaudeLSPSkipsSettingsJSON(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	reg := adapter.NewRegistry()
	if err := reg.Register(claude.New(claude.Options{TargetRoot: tmp})); err != nil {
		t.Fatal(err)
	}
	c := source.Canonical{
		Hooks:      []source.Hook{{Event: "Stop", Type: "command", Command: "echo x"}},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
	}
	st := state.New()
	applyOnce(t, reg, c, st, home, tmp)

	m := readSettings(t, tmp)
	hooks, _ := m["hooks"].(map[string]any)
	if len(hooks) == 0 {
		t.Fatalf("hooks section was not rendered: %+v", m)
	}
	if _, ok := m["lspServers"]; ok {
		t.Fatalf("Claude LSP must be skipped instead of written to ignored settings.json key: %+v", m)
	}
}
