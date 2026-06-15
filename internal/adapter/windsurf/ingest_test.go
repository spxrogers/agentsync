package windsurf_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestRoundTrip_GlobalRulesAndWorkflows_UserScope: user-scope memory renders to
// the documented global rules file (~/.codeium/windsurf/memories/global_rules.md,
// always-on, frontmatter-less) and commands to global workflows; both round-trip
// byte-clean.
func TestRoundTrip_GlobalRulesAndWorkflows_UserScope(t *testing.T) {
	tmp := t.TempDir()
	a := windsurf.New(windsurf.Options{TargetRoot: tmp})
	in := source.Canonical{
		Memory:   source.Memory{Body: "# Global memory\n\nAlways on.\n"},
		Commands: []source.Command{{Name: "release", Body: "1. tag\n2. push\n"}},
	}
	ops, skips, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(skips) != 0 {
		t.Fatalf("no user-scope skips expected, got %+v", skips)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(filepath.Join(tmp, ".codeium", "windsurf", "memories", "global_rules.md"))
	if err != nil {
		t.Fatalf("global_rules.md not written: %v", err)
	}
	if string(onDisk) != in.Memory.Body {
		t.Fatalf("global rules must be frontmatter-less verbatim body: %q", onDisk)
	}
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Memory.Body != in.Memory.Body {
		t.Fatalf("memory round-trip not byte-clean: %q", got.Memory.Body)
	}
	if len(got.Commands) != 1 || got.Commands[0].Body != "1. tag\n2. push\n" {
		t.Fatalf("global workflow round-trip lost: %+v", got.Commands)
	}
}

// TestRoundTrip_ProjectRule_FrontmatterStripped: the project rule renders with
// the `trigger: always_on` activation frontmatter (workspace rules declare their
// trigger in frontmatter) and ingest strips exactly that block, keeping the
// canonical body byte-clean. A rule whose frontmatter was hand-changed warns.
func TestRoundTrip_ProjectRule_FrontmatterStripped(t *testing.T) {
	tmp := t.TempDir()
	proj := t.TempDir()
	a := windsurf.New(windsurf.Options{TargetRoot: tmp})
	body := "# Project rules\n\nBe terse.\n"
	projC := source.Canonical{Memory: source.Memory{Body: body}}
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
	if got.Memory.Body != body {
		t.Fatalf("frontmatter must be stripped on ingest: %q", got.Memory.Body)
	}

	// Hand-changed trigger → body still captured, with a warning.
	ruleFile := filepath.Join(proj, ".windsurf", "rules", "agentsync.md")
	if err := os.WriteFile(ruleFile, []byte("---\ntrigger: manual\n---\n\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	a2 := windsurf.New(windsurf.Options{TargetRoot: tmp, Stderr: &warn})
	if _, err := a2.Ingest(adapter.ScopeProject, proj); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn.String(), "activation metadata") {
		t.Fatalf("expected a warning about uncaptured activation metadata:\n%s", warn.String())
	}
}

// TestIngest_MCP_ExtraPassthrough_ArtifactAnchored starts from a hand-authored
// native mcp_config.json carrying an unmodeled key and asserts it survives
// ingest (into Extra) and a re-render ON DISK — the fidelity-test pattern
// CLAUDE.md mandates (the model must not be the oracle).
func TestIngest_MCP_ExtraPassthrough_ArtifactAnchored(t *testing.T) {
	tmp := t.TempDir()
	mcpPath := filepath.Join(tmp, ".codeium", "windsurf", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	native := `{
  "mcpServers": {
    "gh": { "command": "npx", "args": ["-y", "x"], "timeout": 60 }
  }
}`
	if err := os.WriteFile(mcpPath, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	a := windsurf.New(windsurf.Options{TargetRoot: tmp})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServers) != 1 {
		t.Fatalf("expected one server: %+v", got.MCPServers)
	}
	if v, ok := got.MCPServers[0].Server.Extra["timeout"]; !ok {
		t.Fatalf("unmodeled native key must be preserved in Extra, got %v", v)
	}
	ops, _, err := a.Render(secrets.ForRender(got), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), `"timeout"`) {
		t.Fatalf("unmodeled native key lost on re-render:\n%s", onDisk)
	}
}
