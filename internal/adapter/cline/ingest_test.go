package cline_test

import (
	"encoding/json"
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

// TestCline_WorkflowIngestOwnershipFilter is artifact-anchored: it writes two
// workflows to .clinerules/workflows/ on disk — one agentsync-owned (carrying the
// leading managed marker, as Render writes it) and one human-authored (no marker) —
// and asserts Ingest captures ONLY the owned one, with the marker stripped from its
// body. This pins the ownership-scoping fix: workflow ingest no longer over-captures
// foreign workflows, matching memory ingest (which reads only agentsync.md).
func TestCline_WorkflowIngestOwnershipFilter(t *testing.T) {
	proj := t.TempDir()
	wfDir := filepath.Join(proj, ".clinerules", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Owned: exactly what renderCommands writes (marker + body).
	if err := os.WriteFile(filepath.Join(wfDir, "deploy.md"), []byte("<!-- agentsync:managed cline-workflow -->\nRun the deploy.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Foreign: a human-authored workflow agentsync never wrote (no marker).
	if err := os.WriteFile(filepath.Join(wfDir, "mine.md"), []byte("# My own workflow\n\nDo my thing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := cline.New(cline.Options{TargetRoot: t.TempDir()}).Ingest(adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commands) != 1 {
		t.Fatalf("expected exactly the owned workflow; got %d commands: %+v", len(got.Commands), got.Commands)
	}
	if got.Commands[0].Name != "deploy" {
		t.Fatalf("captured the wrong workflow: %+v", got.Commands[0])
	}
	if got.Commands[0].Body != "Run the deploy.\n" {
		t.Fatalf("owned workflow body should have the marker stripped, got %q", got.Commands[0].Body)
	}
}

// TestCline_MCPTransportRoundTrip is artifact-anchored and pins the DOCUMENTED
// sse→http collapse: Cline infers transport from keys (no `type` field), so a
// canonical `sse` server renders to a url-bearing entry and comes back as `http`.
// Render → apply (writes ~/.cline/mcp.json) → ingest, asserting the collapse is
// exactly what happens (backing the capability-matrix note). A stdio server is
// included to show non-remote transport is preserved.
func TestCline_MCPTransportRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	a := cline.New(cline.Options{TargetRoot: tmp})
	in := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "sse-srv", Server: source.MCPServerSpec{Type: "sse", URL: "https://x/sse", Headers: map[string]string{"A": "b"}}},
		{ID: "stdio-srv", Server: source.MCPServerSpec{Type: "stdio", Command: "npx"}},
	}}
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	// On disk, the sse server carries a url and NO type key (Cline infers transport),
	// which is exactly why the transport cannot round-trip as sse.
	data, err := os.ReadFile(filepath.Join(tmp, ".cline", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	sse := top["mcpServers"].(map[string]any)["sse-srv"].(map[string]any)
	if sse["url"] != "https://x/sse" {
		t.Fatalf("sse server must carry url on disk: %v", sse)
	}
	if _, hasType := sse["type"]; hasType {
		t.Fatalf("Cline records no transport type; got %v", sse)
	}
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]source.MCPServerSpec{}
	for _, m := range got.MCPServers {
		byID[m.ID] = m.Server
	}
	if s := byID["sse-srv"]; s.Type != "http" || s.URL != "https://x/sse" {
		t.Fatalf("sse must collapse to http on round-trip: got %+v", s)
	}
	if s := byID["stdio-srv"]; s.Type != "stdio" || s.Command != "npx" {
		t.Fatalf("stdio round-trip lost data: %+v", s)
	}
}
