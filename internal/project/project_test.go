package project_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/source"
)

// scaffold writes an empty project source tree at <root>/.agentsync/ so Discover
// treats <root> as a project root. Returns the project home (<root>/.agentsync).
func scaffold(t *testing.T, root string) string {
	t.Helper()
	home := filepath.Join(root, project.DirName)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte("[agents]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// ─── Discover ────────────────────────────────────────────────────────────────

func TestDiscover_FoundAtRoot(t *testing.T) {
	tmp := t.TempDir()
	scaffold(t, tmp)
	deep := filepath.Join(tmp, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	root, found, err := project.Discover(deep)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected discovery")
	}
	if root != tmp {
		t.Fatalf("root = %q, want %q", root, tmp)
	}
}

func TestDiscover_FoundInCwd(t *testing.T) {
	tmp := t.TempDir()
	scaffold(t, tmp)
	root, found, err := project.Discover(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !found || root != tmp {
		t.Fatalf("expected discovery in cwd; found=%v root=%q", found, root)
	}
}

func TestDiscover_NotFound(t *testing.T) {
	root, found, err := project.Discover(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("expected no project, got root=%q", root)
	}
}

func TestDiscover_FirstMatchWins(t *testing.T) {
	parent := t.TempDir()
	scaffold(t, parent)
	child := filepath.Join(parent, "sub")
	childHome := filepath.Join(child, project.DirName)
	if err := os.MkdirAll(childHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childHome, "agentsync.toml"), []byte("[agents]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, found, err := project.Discover(child)
	if err != nil {
		t.Fatal(err)
	}
	if !found || root != child {
		t.Fatalf("expected nearest project to win; found=%v root=%q want %q", found, root, child)
	}
}

// A bare .agentsync.toml FILE (no .agentsync/ dir) is the retired M5 marker.
// Discover must surface a migration error rather than silently ignore it — a
// user upgrading from M5 would otherwise see their project config vanish.
func TestDiscover_LegacyMarkerErrors(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, project.LegacyMarkerFile), []byte(`agents = ["claude"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := project.Discover(tmp)
	if err == nil {
		t.Fatal("expected a migration error for a legacy .agentsync.toml marker")
	}
	if !strings.Contains(err.Error(), "init --scope project") {
		t.Fatalf("error should point to migration; got %v", err)
	}
}

// A .agentsync/ dir present alongside a stray legacy file: the dir wins, no error
// (the dir is authoritative; the file is ignored).
func TestDiscover_DirWinsOverLegacyFile(t *testing.T) {
	tmp := t.TempDir()
	scaffold(t, tmp)
	if err := os.WriteFile(filepath.Join(tmp, project.LegacyMarkerFile), []byte(`agents = ["claude"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	root, found, err := project.Discover(tmp)
	if err != nil {
		t.Fatalf("dir should win without error; got %v", err)
	}
	if !found || root != tmp {
		t.Fatalf("found=%v root=%q", found, root)
	}
}

// Home derives the project canonical home from a project root.
func TestHome(t *testing.T) {
	got := project.Home("/repo")
	want := filepath.Join("/repo", project.DirName)
	if got != want {
		t.Fatalf("Home = %q, want %q", got, want)
	}
}

// ─── Merge: overlay project canonical onto base ──────────────────────────────

func TestMerge_AgentsReplaceWhenDeclared(t *testing.T) {
	base := source.Canonical{Config: source.Config{Agents: map[string]source.Agent{
		"claude": {Enabled: true}, "codex": {Enabled: true}, "opencode": {Enabled: true},
	}}}
	proj := source.Canonical{Config: source.Config{Agents: map[string]source.Agent{
		"claude": {Enabled: true},
	}}}
	out := project.Merge(base, proj)
	if len(out.Config.Agents) != 1 {
		t.Fatalf("project agent set should replace base; got %v", out.Config.Agents)
	}
	if _, ok := out.Config.Agents["claude"]; !ok {
		t.Fatal("claude should be retained")
	}
}

func TestMerge_AgentsInheritWhenProjectEmpty(t *testing.T) {
	base := source.Canonical{Config: source.Config{Agents: map[string]source.Agent{
		"claude": {Enabled: true}, "codex": {Enabled: true},
	}}}
	proj := source.Canonical{} // no agents declared
	out := project.Merge(base, proj)
	if len(out.Config.Agents) != 2 {
		t.Fatalf("empty project agents should inherit base; got %v", out.Config.Agents)
	}
}

func TestMerge_MCPOverlay_AppendAndReplace(t *testing.T) {
	base := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "keep", Server: source.MCPServerSpec{Type: "stdio", Command: "old-keep"}},
		{ID: "shadowed", Server: source.MCPServerSpec{Type: "stdio", Command: "user-cmd"}},
	}}
	proj := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "shadowed", Server: source.MCPServerSpec{Type: "stdio", Command: "proj-cmd"}},
		{ID: "added", Server: source.MCPServerSpec{Type: "stdio", Command: "npx"}},
	}}
	out := project.Merge(base, proj)
	got := map[string]string{}
	for _, s := range out.MCPServers {
		got[s.ID] = s.Server.Command
	}
	if got["keep"] != "old-keep" || got["shadowed"] != "proj-cmd" || got["added"] != "npx" {
		t.Fatalf("unexpected overlay result: %v", got)
	}
	if len(out.MCPServers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(out.MCPServers))
	}
}

func TestMerge_ComponentsOverlayByName(t *testing.T) {
	base := source.Canonical{
		Skills:    []source.Skill{{Name: "a", Body: "base-a"}, {Name: "b", Body: "base-b"}},
		Subagents: []source.Subagent{{Name: "rev", Body: "base-rev"}},
		Commands:  []source.Command{{Name: "deploy", Body: "base-deploy"}},
		LSPServers: []source.LSPServer{
			{ID: "gopls", Spec: source.LSPServerSpec{Command: "base"}},
		},
	}
	proj := source.Canonical{
		Skills:     []source.Skill{{Name: "b", Body: "proj-b"}, {Name: "c", Body: "proj-c"}},
		Subagents:  []source.Subagent{{Name: "rev", Body: "proj-rev"}},
		Commands:   []source.Command{{Name: "ship", Body: "proj-ship"}},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "proj"}}},
	}
	out := project.Merge(base, proj)
	skills := map[string]string{}
	for _, s := range out.Skills {
		skills[s.Name] = s.Body
	}
	if skills["a"] != "base-a" || skills["b"] != "proj-b" || skills["c"] != "proj-c" {
		t.Fatalf("skills overlay wrong: %v", skills)
	}
	if len(out.Subagents) != 1 || out.Subagents[0].Body != "proj-rev" {
		t.Fatalf("subagent overlay wrong: %+v", out.Subagents)
	}
	if len(out.Commands) != 2 {
		t.Fatalf("commands should append: %+v", out.Commands)
	}
	if len(out.LSPServers) != 1 || out.LSPServers[0].Spec.Command != "proj" {
		t.Fatalf("lsp overlay wrong: %+v", out.LSPServers)
	}
}

func TestMerge_HooksOverlayByEvent(t *testing.T) {
	base := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Command: "base-pre"},
		{Event: "PostToolUse", Command: "base-post"},
	}}
	proj := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Command: "proj-pre"},
	}}
	out := project.Merge(base, proj)
	byEvent := map[string][]string{}
	for _, h := range out.Hooks {
		byEvent[h.Event] = append(byEvent[h.Event], h.Command)
	}
	// All hooks for an overridden event are replaced by the project's set.
	if len(byEvent["PreToolUse"]) != 1 || byEvent["PreToolUse"][0] != "proj-pre" {
		t.Fatalf("PreToolUse should be replaced: %v", byEvent)
	}
	if len(byEvent["PostToolUse"]) != 1 || byEvent["PostToolUse"][0] != "base-post" {
		t.Fatalf("PostToolUse should be inherited: %v", byEvent)
	}
}

func TestMerge_MemoryAppends(t *testing.T) {
	base := source.Canonical{Memory: source.Memory{Body: "# Base"}}
	proj := source.Canonical{Memory: source.Memory{Body: "# Project"}}
	out := project.Merge(base, proj)
	if !strings.Contains(out.Memory.Body, "# Base") || !strings.Contains(out.Memory.Body, "# Project") {
		t.Fatalf("memory should concatenate base then project: %q", out.Memory.Body)
	}
	if strings.Index(out.Memory.Body, "# Base") > strings.Index(out.Memory.Body, "# Project") {
		t.Fatalf("base memory should come before project memory: %q", out.Memory.Body)
	}
}

func TestMerge_MemoryProjectOnly(t *testing.T) {
	base := source.Canonical{}
	proj := source.Canonical{Memory: source.Memory{Body: "# Project"}}
	out := project.Merge(base, proj)
	if strings.TrimSpace(out.Memory.Body) != "# Project" {
		t.Fatalf("project-only memory should pass through verbatim: %q", out.Memory.Body)
	}
}

func TestMerge_DoesNotMutateBase(t *testing.T) {
	base := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "existing", Server: source.MCPServerSpec{Command: "cmd"}},
	}}
	orig := len(base.MCPServers)
	proj := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "added", Server: source.MCPServerSpec{Command: "new"}},
	}}
	_ = project.Merge(base, proj)
	if len(base.MCPServers) != orig {
		t.Fatal("Merge mutated base MCPServers slice")
	}
}

// TestMerge_StoresProjectOverlay verifies that Merge populates out.Project with
// the project-only canonical so scope-aware render paths can distinguish which
// items originated from the project vs the user.
func TestMerge_StoresProjectOverlay(t *testing.T) {
	base := source.Canonical{
		Skills:     []source.Skill{{Name: "user-skill", Body: "user"}},
		MCPServers: []source.MCPServer{{ID: "user-mcp", Server: source.MCPServerSpec{Command: "user-cmd"}}},
	}
	proj := source.Canonical{
		Skills:     []source.Skill{{Name: "proj-skill", Body: "proj"}},
		MCPServers: []source.MCPServer{{ID: "proj-mcp", Server: source.MCPServerSpec{Command: "proj-cmd"}}},
	}
	out := project.Merge(base, proj)

	// Merged result contains items from both scopes.
	if len(out.Skills) != 2 {
		t.Fatalf("expected 2 merged skills, got %d", len(out.Skills))
	}
	if len(out.MCPServers) != 2 {
		t.Fatalf("expected 2 merged MCP servers, got %d", len(out.MCPServers))
	}

	// out.Project must be non-nil and hold only the project-scope items.
	if out.Project == nil {
		t.Fatal("Merge must set out.Project to the project-only canonical")
	}
	if len(out.Project.Skills) != 1 || out.Project.Skills[0].Name != "proj-skill" {
		t.Fatalf("Project.Skills should contain only project skills: %+v", out.Project.Skills)
	}
	if len(out.Project.MCPServers) != 1 || out.Project.MCPServers[0].ID != "proj-mcp" {
		t.Fatalf("Project.MCPServers should contain only project servers: %+v", out.Project.MCPServers)
	}
}
