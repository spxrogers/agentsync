package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// applyOnce runs the real plan→apply→prune→record cycle the apply command uses
// for the given agents.
func applyOnce(t *testing.T, reg *adapter.Registry, agents []string, c source.Canonical, st *state.Targets, home, userHome string) {
	t.Helper()
	plan, err := render.Plan(secrets.ForRender(c), reg, agents, adapter.ScopeUser, "", st, userHome)
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

func readCodexConfig(t *testing.T, tmp string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tmp, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	var m map[string]any
	if err := toml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse config.toml: %v", err)
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
	applyOnce(t, reg, []string{"claude"}, c, st, home, tmp)

	m := readSettings(t, tmp)
	hooks, _ := m["hooks"].(map[string]any)
	if len(hooks) == 0 {
		t.Fatalf("hooks section was not rendered: %+v", m)
	}
	if _, ok := m["lspServers"]; ok {
		t.Fatalf("Claude LSP must be skipped instead of written to ignored settings.json key: %+v", m)
	}
}

// codexTwoSection is the multi-section canonical the two regressions below drive
// through the real pipeline: Codex renders MCP servers AND hooks as two separate
// merge-toml-keys ops to the SAME ~/.codex/config.toml (`[mcp_servers.*]` +
// `[[hooks.*]]`). This is the shape Claude used to have with settings.json
// hooks+lspServers; #66/#73 removed Claude's ignored LSP write, so Codex is now
// the live vehicle for the pipeline's per-op OwnedKeys section-scoping.
func codexTwoSection() source.Canonical {
	return source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Type: "stdio", Command: "npx"}}},
		Hooks:      []source.Hook{{Event: "Stop", Type: "command", Command: "echo x"}},
	}
}

// TestApply_MultiSectionSameFile_NoSiblingWipe is the regression for the
// showstopper: two key-merge ops target one file as separate sections. Each op
// received ALL owned pointers for the file, so on the second apply the
// mcp_servers op deleted the hooks pointer and vice versa — last op wins, the
// other section is wiped. scopeOwnedToSections (pipeline.go) fixes it; this
// proves it stays fixed. (Re-homed from Claude hooks+lspServers to Codex
// mcp_servers+hooks after #73 dropped Claude's settings.json LSP write.)
//
// This guards the interaction at the render-pipeline layer. The same
// no-sibling-wipe property is also covered end-to-end through the CLI by
// TestApply_Codex_MCPAndHooks_Converge (internal/cli), and for the single-apply
// case with a fake adapter by TestRenderApply_MultipleMergeOpsSamePathAllApplied
// (writer_test.go) — three altitudes, one property.
func TestApply_MultiSectionSameFile_NoSiblingWipe(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	reg := adapter.NewRegistry()
	if err := reg.Register(codex.New(codex.Options{TargetRoot: tmp})); err != nil {
		t.Fatal(err)
	}
	st := state.New()
	applyOnce(t, reg, []string{"codex"}, codexTwoSection(), st, home, tmp)
	applyOnce(t, reg, []string{"codex"}, codexTwoSection(), st, home, tmp) // second apply is where the wipe happened

	m := readCodexConfig(t, tmp)
	if servers, _ := m["mcp_servers"].(map[string]any); len(servers) == 0 {
		t.Fatalf("mcp_servers section was wiped on re-apply: %+v", m)
	}
	if hooks, _ := m["hooks"].(map[string]any); len(hooks) == 0 {
		t.Fatalf("hooks section was wiped on re-apply: %+v", m)
	}
}

// TestApply_WholeSectionRemoval_StillCleansUp guards the removal case that the
// per-op-section scoping must not regress: removing ALL hooks (while keeping the
// MCP server) must still delete the orphaned [hooks.*] section from config.toml,
// even though another section's op still writes that file. Unlike the
// convergence tests cross-referenced above, this is the only coverage for
// per-section orphan cleanup while a sibling section stays live (it exercises
// orphanCleanupOps, a different pipeline path than scopeOwnedToSections).
func TestApply_WholeSectionRemoval_StillCleansUp(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	reg := adapter.NewRegistry()
	if err := reg.Register(codex.New(codex.Options{TargetRoot: tmp})); err != nil {
		t.Fatal(err)
	}
	st := state.New()
	applyOnce(t, reg, []string{"codex"}, codexTwoSection(), st, home, tmp)

	// Remove hooks entirely; keep the MCP server.
	noHooks := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Type: "stdio", Command: "npx"}}},
	}
	applyOnce(t, reg, []string{"codex"}, noHooks, st, home, tmp)

	m := readCodexConfig(t, tmp)
	if hooks, _ := m["hooks"].(map[string]any); len(hooks) != 0 {
		t.Fatalf("removed hook section was not cleaned up: %+v", m["hooks"])
	}
	if servers, _ := m["mcp_servers"].(map[string]any); len(servers) == 0 {
		t.Fatalf("mcp_servers wrongly removed when only hooks were dropped: %+v", m)
	}
}
