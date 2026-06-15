package cline_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/cline"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestRoundTrip_MCP_UserScope renders stdio + remote servers to ~/.cline/mcp.json,
// applies, ingests, and asserts the canonical specs survive (url → http transport).
func TestRoundTrip_MCP_UserScope(t *testing.T) {
	tmp := t.TempDir()
	a := cline.New(cline.Options{TargetRoot: tmp})
	in := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "stdio-srv", Server: source.MCPServerSpec{Type: "stdio", Command: "npx", Args: []string{"-y", "pkg"}, Env: map[string]string{"K": "v"}}},
		{ID: "remote-srv", Server: source.MCPServerSpec{Type: "http", URL: "https://x/mcp", Headers: map[string]string{"API_KEY": "v"}}},
	}}
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
	byID := map[string]source.MCPServerSpec{}
	for _, m := range got.MCPServers {
		byID[m.ID] = m.Server
	}
	if s := byID["stdio-srv"]; s.Type != "stdio" || s.Command != "npx" || !reflect.DeepEqual(s.Args, []string{"-y", "pkg"}) || s.Env["K"] != "v" {
		t.Fatalf("stdio round-trip lost data: %+v", s)
	}
	if s := byID["remote-srv"]; s.Type != "http" || s.URL != "https://x/mcp" || s.Headers["API_KEY"] != "v" {
		t.Fatalf("remote round-trip lost data: %+v", s)
	}
}

// TestApply_MCP_PreservesForeignServers verifies merge-json-keys preserves a
// hand-authored ~/.cline/mcp.json's foreign servers.
func TestApply_MCP_PreservesForeignServers(t *testing.T) {
	tmp := t.TempDir()
	mcpPath := filepath.Join(tmp, ".cline", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{ "mcpServers": { "user-server": { "command": "mine", "disabled": false } } }`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := source.Canonical{MCPServers: []source.MCPServer{{ID: "ours", Server: source.MCPServerSpec{Command: "npx"}}}}
	a := cline.New(cline.Options{TargetRoot: tmp})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, m := range got.MCPServers {
		ids[m.ID] = true
		if m.ID == "user-server" && m.Server.Extra["disabled"] == nil {
			t.Fatalf("foreign server's unmodeled key not preserved: %+v", m.Server.Extra)
		}
	}
	if !ids["user-server"] || !ids["ours"] {
		t.Fatalf("expected both foreign + ours servers, got %v", ids)
	}
}

// TestRoundTrip_MemoryAndCommand_ProjectScope verifies rules/workflows round-trip
// byte-clean (both plain markdown).
func TestRoundTrip_MemoryAndCommand_ProjectScope(t *testing.T) {
	proj := t.TempDir()
	a := cline.New(cline.Options{TargetRoot: t.TempDir()})
	in := projOf(source.Canonical{
		Memory:   source.Memory{Body: "# Conventions\n\nUse tabs.\n"},
		Commands: []source.Command{{Name: "deploy", Frontmatter: map[string]any{"description": "Deploy"}, Body: "Run the deploy.\n"}},
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
	if got.Memory.Body != "# Conventions\n\nUse tabs.\n" {
		t.Fatalf("memory round-trip mismatch: %q", got.Memory.Body)
	}
	if len(got.Commands) != 1 || got.Commands[0].Name != "deploy" || got.Commands[0].Body != "Run the deploy.\n" {
		t.Fatalf("command round-trip lost data: %+v", got.Commands)
	}
	if len(got.Commands[0].Frontmatter) != 0 {
		t.Fatalf("Cline workflows carry no frontmatter; got %+v", got.Commands[0].Frontmatter)
	}
}
