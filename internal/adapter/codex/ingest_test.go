package codex_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// roundTrip renders, applies to a temp filesystem, and ingests back.
func roundTrip(t *testing.T, in source.Canonical, scope adapter.Scope, project string) source.Canonical {
	t.Helper()
	tmp := t.TempDir()
	a := codex.New(codex.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), scope, project)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(scope, project)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return out
}

func TestIngest_RoundTripsMCP_Stdio(t *testing.T) {
	enabled := true
	in := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "github",
		Server: source.MCPServerSpec{
			Type: "stdio", Command: "npx", Args: []string{"-y", "x"},
			Env: map[string]string{"TOKEN": "abc"}, Enabled: &enabled,
		},
	}}}
	out := roundTrip(t, in, adapter.ScopeUser, "")
	if len(out.MCPServers) != 1 || out.MCPServers[0].ID != "github" {
		t.Fatalf("MCP roundtrip lost: %+v", out.MCPServers)
	}
	srv := out.MCPServers[0].Server
	if srv.Command != "npx" {
		t.Fatalf("command = %q, want npx", srv.Command)
	}
	if len(srv.Args) != 2 || srv.Args[0] != "-y" || srv.Args[1] != "x" {
		t.Fatalf("args = %v, want [-y x]", srv.Args)
	}
	if srv.Type != "stdio" {
		t.Fatalf("type = %q, want stdio", srv.Type)
	}
	if srv.Env["TOKEN"] != "abc" {
		t.Fatalf("env token missing: %+v", srv.Env)
	}
}

func TestIngest_RoundTripsMCP_RemoteHeaders(t *testing.T) {
	in := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "remote",
		Server: source.MCPServerSpec{
			Type: "http", URL: "https://mcp.example.com",
			Headers: map[string]string{"Authorization": "Bearer xyz"},
		},
	}}}
	out := roundTrip(t, in, adapter.ScopeUser, "")
	if len(out.MCPServers) != 1 {
		t.Fatalf("MCP roundtrip lost: %+v", out.MCPServers)
	}
	srv := out.MCPServers[0].Server
	if srv.Type != "http" || srv.URL != "https://mcp.example.com" {
		t.Fatalf("remote roundtrip lost: %+v", srv)
	}
	if srv.Headers["Authorization"] != "Bearer xyz" {
		t.Fatalf("auth header dropped on round-trip: %+v", srv.Headers)
	}
}

// TestIngest_MCP_SSEMapsToHTTP proves the canonical "sse" transport renders as a
// Codex URL server and canonicalises back to "http" (Codex has no sse).
func TestIngest_MCP_SSEMapsToHTTP(t *testing.T) {
	in := source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "stream",
		Server: source.MCPServerSpec{Type: "sse", URL: "https://sse.example.com"},
	}}}
	out := roundTrip(t, in, adapter.ScopeUser, "")
	if len(out.MCPServers) != 1 || out.MCPServers[0].Server.URL != "https://sse.example.com" {
		t.Fatalf("sse->http round-trip lost the server: %+v", out.MCPServers)
	}
	if out.MCPServers[0].Server.Type != "http" {
		t.Fatalf("sse should canonicalise to http, got %q", out.MCPServers[0].Server.Type)
	}
}

func TestIngest_RoundTripsMemory(t *testing.T) {
	in := source.Canonical{Memory: source.Memory{Body: "# Memory\n\nAlways be helpful.\n"}}
	out := roundTrip(t, in, adapter.ScopeUser, "")
	if out.Memory.Body != in.Memory.Body {
		t.Fatalf("Memory roundtrip: got %q, want %q", out.Memory.Body, in.Memory.Body)
	}
}

func TestIngest_RoundTripsSubagent(t *testing.T) {
	in := source.Canonical{Subagents: []source.Subagent{{
		Name: "reviewer",
		Frontmatter: map[string]any{
			"description": "code review agent",
			"model":       "gpt-5.5",
			"tools":       []string{"Read"}, // dropped on render, not expected back
		},
		Body: "You are a code reviewer.\n",
	}}}
	out := roundTrip(t, in, adapter.ScopeUser, "")
	if len(out.Subagents) != 1 || out.Subagents[0].Name != "reviewer" {
		t.Fatalf("Subagent roundtrip lost: %+v", out.Subagents)
	}
	sa := out.Subagents[0]
	if sa.Frontmatter["description"] != "code review agent" {
		t.Fatalf("description missing: %+v", sa.Frontmatter)
	}
	if sa.Frontmatter["model"] != "gpt-5.5" {
		t.Fatalf("model missing: %+v", sa.Frontmatter)
	}
	if _, ok := sa.Frontmatter["tools"]; ok {
		t.Fatalf("tools should not survive (no Codex target): %+v", sa.Frontmatter)
	}
	if sa.Body != "You are a code reviewer.\n" {
		t.Fatalf("body (developer_instructions) roundtrip: got %q", sa.Body)
	}
}

func TestIngest_RoundTripsCommand(t *testing.T) {
	in := source.Canonical{Commands: []source.Command{{
		Name:        "format",
		Frontmatter: map[string]any{"description": "Format code", "argument-hint": "<paths>"},
		Body:        "Run gofmt on $ARGUMENTS.\n",
	}}}
	out := roundTrip(t, in, adapter.ScopeUser, "")
	if len(out.Commands) != 1 || out.Commands[0].Name != "format" {
		t.Fatalf("Command roundtrip lost: %+v", out.Commands)
	}
	if out.Commands[0].Frontmatter["argument-hint"] != "<paths>" {
		t.Fatalf("argument-hint not round-tripped: %+v", out.Commands[0].Frontmatter)
	}
}

func TestIngest_RoundTripsHooks(t *testing.T) {
	in := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Matcher: "Bash", Type: "command", Command: "echo hi"},
	}}
	out := roundTrip(t, in, adapter.ScopeUser, "")
	if len(out.Hooks) != 1 {
		t.Fatalf("Hooks roundtrip lost: %+v", out.Hooks)
	}
	h := out.Hooks[0]
	if h.Event != "PreToolUse" || h.Matcher != "Bash" || h.Type != "command" || h.Command != "echo hi" {
		t.Fatalf("hook roundtrip mismatch: %+v", h)
	}
}
