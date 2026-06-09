package continuedev_test

import (
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/continuedev"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func renderApply(t *testing.T, a *continuedev.Adapter, c source.Canonical) {
	t.Helper()
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

// TestRoundTrip_MCP renders stdio + http + sse blocks, applies, ingests, and
// asserts the canonical specs survive (transport + headers round-trip).
func TestRoundTrip_MCP(t *testing.T) {
	tmp := t.TempDir()
	a := continuedev.New(continuedev.Options{TargetRoot: tmp})
	in := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "stdio-srv", Server: source.MCPServerSpec{Type: "stdio", Command: "npx", Args: []string{"-y", "pkg"}, Env: map[string]string{"K": "v"}}},
		{ID: "http-srv", Server: source.MCPServerSpec{Type: "http", URL: "https://x/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}},
		{ID: "sse-srv", Server: source.MCPServerSpec{Type: "sse", URL: "https://x/sse"}},
	}}
	renderApply(t, a, in)
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
	if s := byID["http-srv"]; s.Type != "http" || s.URL != "https://x/mcp" || s.Headers["Authorization"] != "Bearer t" {
		t.Fatalf("http round-trip lost data: %+v", s)
	}
	if s := byID["sse-srv"]; s.Type != "sse" || s.URL != "https://x/sse" {
		t.Fatalf("sse round-trip lost data: %+v", s)
	}
}

// TestRoundTrip_MCP_ExtraPassthrough verifies a native key agentsync doesn't
// model (cwd) survives via Extra.
func TestRoundTrip_MCP_ExtraPassthrough(t *testing.T) {
	tmp := t.TempDir()
	a := continuedev.New(continuedev.Options{TargetRoot: tmp})
	renderApply(t, a, source.Canonical{MCPServers: []source.MCPServer{{
		ID: "srv", Server: source.MCPServerSpec{Command: "x", Extra: map[string]any{"cwd": "/work"}},
	}}})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServers) != 1 {
		t.Fatalf("want 1 server, got %d", len(got.MCPServers))
	}
	if got.MCPServers[0].Server.Extra["cwd"] != "/work" {
		t.Fatalf("cwd not captured into Extra: %+v", got.MCPServers[0].Server.Extra)
	}
}

// TestRoundTrip_Memory verifies the memory body round-trips byte-clean (the rule
// has no frontmatter to munge).
func TestRoundTrip_Memory(t *testing.T) {
	tmp := t.TempDir()
	a := continuedev.New(continuedev.Options{TargetRoot: tmp})
	body := "# Conventions\n\nUse tabs.\n"
	renderApply(t, a, source.Canonical{Memory: source.Memory{Body: body}})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Memory.Body != body {
		t.Fatalf("memory round-trip mismatch: %q", got.Memory.Body)
	}
}

// TestRoundTrip_Command verifies the body + description survive (argument-hint is
// the documented projected loss).
func TestRoundTrip_Command(t *testing.T) {
	tmp := t.TempDir()
	a := continuedev.New(continuedev.Options{TargetRoot: tmp})
	in := source.Canonical{Commands: []source.Command{{
		Name: "deploy", Frontmatter: map[string]any{"description": "Deploy", "argument-hint": "<env>"}, Body: "Deploy it.\n",
	}}}
	renderApply(t, a, in)
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commands) != 1 || got.Commands[0].Name != "deploy" {
		t.Fatalf("command not ingested: %+v", got.Commands)
	}
	if got.Commands[0].Body != "Deploy it.\n" {
		t.Fatalf("command body lost: %q", got.Commands[0].Body)
	}
	if got.Commands[0].Frontmatter["description"] != "Deploy" {
		t.Fatalf("description lost: %+v", got.Commands[0].Frontmatter)
	}
	if _, ok := got.Commands[0].Frontmatter["argument-hint"]; ok {
		t.Fatalf("argument-hint should be dropped: %+v", got.Commands[0].Frontmatter)
	}
	// Continue-specific frontmatter must not leak into the canonical model.
	if _, ok := got.Commands[0].Frontmatter["invokable"]; ok {
		t.Fatalf("invokable should not be captured into canonical: %+v", got.Commands[0].Frontmatter)
	}
}
