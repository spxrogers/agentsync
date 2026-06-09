package roo_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/roo"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestRoundTrip_ProjectScope renders MCP + memory + commands at project scope,
// applies, ingests, and asserts they survive (MCP transport + headers, command
// description + argument-hint, memory byte-clean).
func TestRoundTrip_ProjectScope(t *testing.T) {
	proj := t.TempDir()
	a := roo.New(roo.Options{TargetRoot: t.TempDir()})
	in := projOf(source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "stdio-srv", Server: source.MCPServerSpec{Type: "stdio", Command: "npx", Args: []string{"-y", "pkg"}, Env: map[string]string{"K": "v"}}},
			{ID: "http-srv", Server: source.MCPServerSpec{Type: "http", URL: "https://x/mcp", Headers: map[string]string{"A": "b"}}},
		},
		Memory:   source.Memory{Body: "# Conventions\n\nUse tabs.\n"},
		Commands: []source.Command{{Name: "deploy", Frontmatter: map[string]any{"description": "Deploy", "argument-hint": "<env>", "allowed-tools": "Bash"}, Body: "Deploy it.\n"}},
	})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	got, err := a.Ingest(adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}

	byID := map[string]source.MCPServerSpec{}
	for _, m := range got.MCPServers {
		byID[m.ID] = m.Server
	}
	if s := byID["stdio-srv"]; s.Type != "stdio" || s.Command != "npx" || !reflect.DeepEqual(s.Args, []string{"-y", "pkg"}) || s.Env["K"] != "v" {
		t.Fatalf("stdio round-trip lost data: %+v", s)
	}
	if s := byID["http-srv"]; s.Type != "http" || s.URL != "https://x/mcp" || s.Headers["A"] != "b" {
		t.Fatalf("http round-trip lost data: %+v", s)
	}
	if got.Memory.Body != "# Conventions\n\nUse tabs.\n" {
		t.Fatalf("memory round-trip mismatch: %q", got.Memory.Body)
	}
	if len(got.Commands) != 1 {
		t.Fatalf("command not ingested: %+v", got.Commands)
	}
	cmd := got.Commands[0]
	if cmd.Name != "deploy" || cmd.Body != "Deploy it.\n" {
		t.Fatalf("command name/body lost: %+v", cmd)
	}
	// description + argument-hint survive (Roo keeps both); allowed-tools dropped.
	if cmd.Frontmatter["description"] != "Deploy" || cmd.Frontmatter["argument-hint"] != "<env>" {
		t.Fatalf("description/argument-hint lost: %+v", cmd.Frontmatter)
	}
	if _, ok := cmd.Frontmatter["allowed-tools"]; ok {
		t.Fatalf("allowed-tools should be dropped: %+v", cmd.Frontmatter)
	}
}

// TestRoundTrip_MCP_ExtraPassthrough verifies a native key agentsync doesn't model
// (timeout) survives via Extra.
func TestRoundTrip_MCP_ExtraPassthrough(t *testing.T) {
	proj := t.TempDir()
	mcpPath := filepath.Join(proj, ".roo", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{ "mcpServers": { "srv": { "command": "x", "timeout": 60, "alwaysAllow": ["a"] } } }`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := roo.New(roo.Options{TargetRoot: t.TempDir()}).Ingest(adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServers) != 1 {
		t.Fatalf("want 1 server, got %d", len(got.MCPServers))
	}
	extra := got.MCPServers[0].Server.Extra
	if extra["timeout"] == nil || extra["alwaysAllow"] == nil {
		t.Fatalf("native keys not captured into Extra: %+v", extra)
	}
}

// TestRoundTrip_UserScope_RulesAndCommands verifies the user-scope memory+command
// round-trip (both live under ~/.roo).
func TestRoundTrip_UserScope_RulesAndCommands(t *testing.T) {
	tmp := t.TempDir()
	a := roo.New(roo.Options{TargetRoot: tmp})
	in := source.Canonical{
		Memory:   source.Memory{Body: "global rules\n"},
		Commands: []source.Command{{Name: "g", Frontmatter: map[string]any{"description": "G"}, Body: "g body\n"}},
	}
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Memory.Body != "global rules\n" {
		t.Fatalf("user memory round-trip mismatch: %q", got.Memory.Body)
	}
	if len(got.Commands) != 1 || got.Commands[0].Body != "g body\n" || got.Commands[0].Frontmatter["description"] != "G" {
		t.Fatalf("user command round-trip lost data: %+v", got.Commands)
	}
}
