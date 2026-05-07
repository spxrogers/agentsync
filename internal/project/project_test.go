package project_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/source"
)

// ─── Task 1: Discover ────────────────────────────────────────────────────────

func TestDiscover_FoundAtRoot(t *testing.T) {
	tmp := t.TempDir()
	deep := filepath.Join(tmp, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".agentsync.toml"), []byte(`agents = ["claude"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := project.Discover(deep)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected discovery")
	}
	if len(m.Agents) != 1 || m.Agents[0] != "claude" {
		t.Fatalf("agents = %v", m.Agents)
	}
	if m.Root != tmp {
		t.Fatalf("root = %q, want %q", m.Root, tmp)
	}
}

func TestDiscover_FoundInCwd(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".agentsync.toml"), []byte(`agents = ["codex"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := project.Discover(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected discovery in cwd")
	}
	if len(m.Agents) != 1 || m.Agents[0] != "codex" {
		t.Fatalf("agents = %v", m.Agents)
	}
}

func TestDiscover_NotFound(t *testing.T) {
	m, err := project.Discover(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatalf("expected nil marker, got %+v", m)
	}
}

func TestDiscover_ParseError(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".agentsync.toml"), []byte(`not valid toml ][`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := project.Discover(tmp)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestDiscover_FirstMatchWins(t *testing.T) {
	// parent has marker with agents=["claude"], child has marker with agents=["codex"]
	parent := t.TempDir()
	child := filepath.Join(parent, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, ".agentsync.toml"), []byte(`agents = ["claude"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".agentsync.toml"), []byte(`agents = ["codex"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := project.Discover(child)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || len(m.Agents) == 0 || m.Agents[0] != "codex" {
		t.Fatalf("expected child marker to win; agents = %v", m.Agents)
	}
}

// ─── Task 2: Merge ───────────────────────────────────────────────────────────

func TestMerge_NilMarker(t *testing.T) {
	base := source.Canonical{
		Config: source.Config{
			Agents: map[string]source.Agent{"claude": {Enabled: true}},
		},
	}
	out := project.Merge(base, nil)
	if len(out.Config.Agents) != 1 {
		t.Fatalf("nil merge should return base unchanged; got %+v", out.Config.Agents)
	}
}

func TestMerge_AgentsFilter(t *testing.T) {
	base := source.Canonical{
		Config: source.Config{
			Agents: map[string]source.Agent{
				"claude":   {Enabled: true},
				"codex":    {Enabled: true},
				"opencode": {Enabled: true},
			},
		},
	}
	m := &project.Marker{
		Agents: []string{"claude"},
	}
	out := project.Merge(base, m)
	if len(out.Config.Agents) != 1 {
		t.Fatalf("expected 1 agent after filter, got %d: %v", len(out.Config.Agents), out.Config.Agents)
	}
	if _, ok := out.Config.Agents["claude"]; !ok {
		t.Fatal("expected claude to be retained")
	}
}

func TestMerge_AgentsFilter_EmptyAllowlist(t *testing.T) {
	// Empty Agents allowlist = use all enabled (no filtering)
	base := source.Canonical{
		Config: source.Config{
			Agents: map[string]source.Agent{
				"claude": {Enabled: true},
				"codex":  {Enabled: true},
			},
		},
	}
	m := &project.Marker{
		Agents: nil,
	}
	out := project.Merge(base, m)
	if len(out.Config.Agents) != 2 {
		t.Fatalf("empty allowlist should retain all agents; got %d", len(out.Config.Agents))
	}
}

func TestMerge_MCPOverlay_Append(t *testing.T) {
	base := source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "existing", Server: source.MCPServerSpec{Type: "stdio", Command: "echo"}},
		},
	}
	m := &project.Marker{
		MCP: []project.ProjectMCP{
			{ID: "proj-mcp", Type: "stdio", Command: "npx", Args: []string{"-y", "@proj/mcp"}},
		},
	}
	out := project.Merge(base, m)
	if len(out.MCPServers) != 2 {
		t.Fatalf("expected 2 MCP servers after append, got %d", len(out.MCPServers))
	}
	found := false
	for _, s := range out.MCPServers {
		if s.ID == "proj-mcp" {
			found = true
			if s.Server.Command != "npx" {
				t.Fatalf("proj-mcp command = %q", s.Server.Command)
			}
		}
	}
	if !found {
		t.Fatal("proj-mcp not found in merged MCPServers")
	}
}

func TestMerge_MCPOverlay_Replace(t *testing.T) {
	base := source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "existing", Server: source.MCPServerSpec{Type: "stdio", Command: "old-cmd"}},
		},
	}
	m := &project.Marker{
		MCP: []project.ProjectMCP{
			{ID: "existing", Type: "stdio", Command: "new-cmd"},
		},
	}
	out := project.Merge(base, m)
	if len(out.MCPServers) != 1 {
		t.Fatalf("expected 1 MCP server after replace, got %d", len(out.MCPServers))
	}
	if out.MCPServers[0].Server.Command != "new-cmd" {
		t.Fatalf("expected new-cmd, got %q", out.MCPServers[0].Server.Command)
	}
}

func TestMerge_PluginsDisabled(t *testing.T) {
	base := source.Canonical{
		Plugins: []source.Plugin{
			{ID: "screenshot"},
			{ID: "deploy"},
			{ID: "lint"},
		},
	}
	m := &project.Marker{
		Plugins: project.ProjectPluginsSection{
			Disabled: []string{"screenshot", "deploy"},
		},
	}
	out := project.Merge(base, m)
	if len(out.Plugins) != 1 {
		t.Fatalf("expected 1 plugin after disable, got %d: %v", len(out.Plugins), out.Plugins)
	}
	if out.Plugins[0].ID != "lint" {
		t.Fatalf("expected lint to remain; got %q", out.Plugins[0].ID)
	}
}

func TestMerge_MemoryImport(t *testing.T) {
	tmp := t.TempDir()
	agentsFile := filepath.Join(tmp, "AGENTS.md")
	if err := os.WriteFile(agentsFile, []byte("# Project Agents\nDo stuff."), 0o644); err != nil {
		t.Fatal(err)
	}

	base := source.Canonical{
		Memory: source.Memory{Body: "# Base memory"},
	}
	m := &project.Marker{
		Root: tmp,
		Memory: project.ProjectMemorySection{
			Import: []string{"AGENTS.md"},
		},
	}
	out := project.Merge(base, m)
	if out.Memory.Body == base.Memory.Body {
		t.Fatal("memory body should have been extended")
	}
	if len(out.Memory.Body) <= len(base.Memory.Body) {
		t.Fatalf("memory body not extended: %q", out.Memory.Body)
	}
	if !contains(out.Memory.Body, "Project Agents") {
		t.Fatalf("imported content not in memory body: %q", out.Memory.Body)
	}
}

func TestMerge_MemoryImport_MissingFileSkipped(t *testing.T) {
	tmp := t.TempDir()
	base := source.Canonical{
		Memory: source.Memory{Body: "# Base"},
	}
	m := &project.Marker{
		Root: tmp,
		Memory: project.ProjectMemorySection{
			Import: []string{"nonexistent.md"},
		},
	}
	out := project.Merge(base, m)
	// Missing file is silently skipped; body unchanged.
	if out.Memory.Body != base.Memory.Body {
		t.Fatalf("missing import should be skipped; body = %q", out.Memory.Body)
	}
}

func TestMerge_DoesNotMutateBase(t *testing.T) {
	base := source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "existing", Server: source.MCPServerSpec{Type: "stdio", Command: "cmd"}},
		},
	}
	originalLen := len(base.MCPServers)
	m := &project.Marker{
		MCP: []project.ProjectMCP{
			{ID: "new-srv", Type: "stdio", Command: "new"},
		},
	}
	_ = project.Merge(base, m)
	if len(base.MCPServers) != originalLen {
		t.Fatalf("Merge mutated base MCPServers slice")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
