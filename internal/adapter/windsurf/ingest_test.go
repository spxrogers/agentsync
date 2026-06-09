package windsurf_test

import (
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/windsurf"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestRoundTrip_MCP_UserScope renders stdio + remote servers to mcp_config.json,
// applies, ingests, and asserts the canonical specs survive (serverUrl round-trips
// as the http transport).
func TestRoundTrip_MCP_UserScope(t *testing.T) {
	tmp := t.TempDir()
	a := windsurf.New(windsurf.Options{TargetRoot: tmp})
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

// TestRoundTrip_MemoryAndCommand_ProjectScope verifies rules/workflows round-trip
// byte-clean (both are plain markdown).
func TestRoundTrip_MemoryAndCommand_ProjectScope(t *testing.T) {
	proj := t.TempDir()
	a := windsurf.New(windsurf.Options{TargetRoot: t.TempDir()})
	projC := source.Canonical{
		Memory:   source.Memory{Body: "# Conventions\n\nUse tabs.\n"},
		Commands: []source.Command{{Name: "deploy", Frontmatter: map[string]any{"description": "Deploy"}, Body: "Run the deploy.\n"}},
	}
	c := projC
	c.Project = &projC
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
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
	// description is the documented projected loss (Windsurf workflows are plain).
	if len(got.Commands[0].Frontmatter) != 0 {
		t.Fatalf("Windsurf workflows carry no frontmatter; got %+v", got.Commands[0].Frontmatter)
	}
}
