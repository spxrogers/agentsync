package opencode_test

import (
	"os"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

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
	if srv.Env["TOKEN"] != "abc" {
		t.Fatalf("env token missing: %+v", srv.Env)
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
