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

// TestApply_MultiSectionSameFile_NoSiblingWipe is the regression for the
// showstopper: claude writes hooks AND lspServers to settings.json as separate
// key-merge ops. Each op received ALL owned pointers for the file, so on the
// second apply the hooks op deleted the lsp pointer and vice versa — last op
// wins, the other section is wiped.
func TestApply_MultiSectionSameFile_NoSiblingWipe(t *testing.T) {
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
	applyOnce(t, reg, c, st, home, tmp) // second apply is where the wipe happened

	m := readSettings(t, tmp)
	hooks, _ := m["hooks"].(map[string]any)
	lsp, _ := m["lspServers"].(map[string]any)
	if len(hooks) == 0 {
		t.Fatalf("hooks section was wiped on re-apply: %+v", m)
	}
	if len(lsp) == 0 {
		t.Fatalf("lspServers section was wiped on re-apply: %+v", m)
	}
}

// TestApply_WholeSectionRemoval_StillCleansUp guards the removal case that the
// per-op-section scoping must not regress: removing ALL hooks (while keeping
// the lsp server) must still delete the orphaned hook from settings.json, even
// though another section's op still writes that file.
func TestApply_WholeSectionRemoval_StillCleansUp(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	reg := adapter.NewRegistry()
	if err := reg.Register(claude.New(claude.Options{TargetRoot: tmp})); err != nil {
		t.Fatal(err)
	}
	st := state.New()
	withHooks := source.Canonical{
		Hooks:      []source.Hook{{Event: "Stop", Type: "command", Command: "echo x"}},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
	}
	applyOnce(t, reg, withHooks, st, home, tmp)

	// Remove hooks entirely; keep the lsp server.
	noHooks := source.Canonical{
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
	}
	applyOnce(t, reg, noHooks, st, home, tmp)

	m := readSettings(t, tmp)
	if hooks, _ := m["hooks"].(map[string]any); len(hooks) != 0 {
		t.Fatalf("removed hook section was not cleaned up: %+v", m["hooks"])
	}
	if lsp, _ := m["lspServers"].(map[string]any); len(lsp) == 0 {
		t.Fatalf("lspServers wrongly removed when only hooks were dropped: %+v", m)
	}
}
