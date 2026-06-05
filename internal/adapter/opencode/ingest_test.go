package opencode_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestIngest_ProjectScopeMCP_ReadsRootNotDotOpencode is artifact-anchored: it
// writes a real <project>/opencode.json (OpenCode's project config location)
// AND a <project>/.opencode/opencode.json trap, then ingests at project scope
// and asserts ONLY the root file's server is captured — OpenCode does not read
// .opencode/opencode.json, so neither does agentsync.
func TestIngest_ProjectScopeMCP_ReadsRootNotDotOpencode(t *testing.T) {
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "opencode.json"),
		[]byte(`{"mcp":{"projapi":{"type":"local","command":["node","s.js"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Trap: a server in .opencode/opencode.json must NOT be read.
	dotDir := filepath.Join(proj, ".opencode")
	if err := os.MkdirAll(dotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotDir, "opencode.json"),
		[]byte(`{"mcp":{"shouldNotImport":{"type":"local","command":["trap"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	out, err := a.Ingest(adapter.ScopeProject, proj)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.MCPServers) != 1 {
		t.Fatalf("want exactly 1 MCP server (from root opencode.json), got %d: %+v", len(out.MCPServers), out.MCPServers)
	}
	if out.MCPServers[0].ID != "projapi" {
		t.Fatalf("want %q from <root>/opencode.json, got %q (.opencode/opencode.json wrongly read?)", "projapi", out.MCPServers[0].ID)
	}
}

// TestIngest_RoundTripsMCP exercises: Render → Apply → Ingest for MCP servers.
func TestIngest_RoundTripsMCP(t *testing.T) {
	tmp := t.TempDir()
	enabled := true
	in := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "github",
			Server: source.MCPServerSpec{
				Type: "stdio", Command: "npx", Args: []string{"-y", "x"},
				Env: map[string]string{"TOKEN": "abc"}, Enabled: &enabled,
			},
		}},
	}
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.MCPServers) != 1 || out.MCPServers[0].ID != "github" {
		t.Fatalf("MCP roundtrip lost: %+v", out.MCPServers)
	}
	srv := out.MCPServers[0].Server
	if srv.Command != "npx" {
		t.Fatalf("command = %q, want npx", srv.Command)
	}
	// The command array [npx -y x] must split back into command + args so a
	// reconcile write-back doesn't fold the flags into the command string.
	if len(srv.Args) != 2 || srv.Args[0] != "-y" || srv.Args[1] != "x" {
		t.Fatalf("args = %v, want [-y x]", srv.Args)
	}
	if srv.Type != "stdio" {
		t.Fatalf("type = %q, want stdio (from OpenCode \"local\")", srv.Type)
	}
	if srv.Env["TOKEN"] != "abc" {
		t.Fatalf("env token missing: %+v", srv.Env)
	}
}

// TestRender_MCP_SSEMapsToRemote proves the canonical "sse" transport renders as
// OpenCode "remote" (OpenCode has no separate sse transport).
func TestRender_MCP_SSEMapsToRemote(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "stream",
		Server: source.MCPServerSpec{Type: "sse", URL: "https://sse.example.com"},
	}}}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.MCPServers) != 1 || out.MCPServers[0].Server.URL != "https://sse.example.com" {
		t.Fatalf("sse->remote round-trip lost the server: %+v", out.MCPServers)
	}
}

// TestIngest_RoundTripsMCPHeaders is the regression for OpenCode ingest
// silently dropping a remote MCP server's auth headers. Render writes
// spec["headers"]; ingest must read them back, or an `import`/reconcile
// round-trip produces a server config missing its Authorization header,
// which then 401s on the next apply.
func TestIngest_RoundTripsMCPHeaders(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "remote",
			Server: source.MCPServerSpec{
				Type:    "http",
				URL:     "https://mcp.example.com",
				Headers: map[string]string{"Authorization": "Bearer xyz"},
			},
		}},
	}
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.MCPServers) != 1 {
		t.Fatalf("MCP roundtrip lost: %+v", out.MCPServers)
	}
	if got := out.MCPServers[0].Server.Headers["Authorization"]; got != "Bearer xyz" {
		t.Fatalf("auth header dropped on round-trip: got %q, headers=%+v", got, out.MCPServers[0].Server.Headers)
	}
}

// TestIngest_RoundTripsMemory exercises: Render → Apply → Ingest for AGENTS.md.
func TestIngest_RoundTripsMemory(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		Memory: source.Memory{Body: "# Memory\n\nAlways be helpful.\n"},
	}
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if out.Memory.Body != in.Memory.Body {
		t.Fatalf("Memory roundtrip: got %q, want %q", out.Memory.Body, in.Memory.Body)
	}
}

// TestIngest_RoundTripsSubagentsAndCommands exercises roundtrip for agents + commands.
// Fields dropped on render (tools, color) are not expected back; description+model survive.
func TestIngest_RoundTripsSubagentsAndCommands(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		Subagents: []source.Subagent{{
			Name: "reviewer",
			Frontmatter: map[string]any{
				"description": "code review agent",
				"model":       "claude-opus-4-5",
			},
			Body: "You are a code reviewer.\n",
		}},
		Commands: []source.Command{{
			Name:        "format",
			Frontmatter: map[string]any{"description": "Format code"},
			Body:        "Run gofmt on the repo.\n",
		}},
	}
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if len(out.Subagents) != 1 || out.Subagents[0].Name != "reviewer" {
		t.Fatalf("Subagent roundtrip lost: %+v", out.Subagents)
	}
	// `mode` must not be present after ingest (OpenCode artifact, not canonical).
	if _, ok := out.Subagents[0].Frontmatter["mode"]; ok {
		t.Fatalf("'mode' key leaked into canonical subagent frontmatter")
	}
	if out.Subagents[0].Frontmatter["description"] != "code review agent" {
		t.Fatalf("description missing from subagent: %+v", out.Subagents[0].Frontmatter)
	}
	if out.Subagents[0].Frontmatter["model"] != "claude-opus-4-5" {
		t.Fatalf("model missing from subagent: %+v", out.Subagents[0].Frontmatter)
	}

	if len(out.Commands) != 1 || out.Commands[0].Name != "format" {
		t.Fatalf("Command roundtrip lost: %+v", out.Commands)
	}
}

// TestIngest_JSONC_WithComments verifies that opencode.json with JSONC comments
// is ingested correctly via hujson.
func TestIngest_JSONC_WithComments(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID:     "github",
			Server: source.MCPServerSpec{Command: "npx"},
		}},
	}
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Manually inject JSONC comments into the written file, simulating a user
	// who hand-edited the file with comments.
	commentedContent := `{
  // managed by agentsync
  "mcp": {
    // github MCP server
    "github": {"command":"npx"}
  }
}
`
	_ = os.WriteFile(ops[0].Path, []byte(commentedContent), 0o644)

	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest JSONC with comments: %v", err)
	}
	if len(out.MCPServers) != 1 || out.MCPServers[0].ID != "github" {
		t.Fatalf("JSONC ingest lost MCP: %+v", out.MCPServers)
	}
}
