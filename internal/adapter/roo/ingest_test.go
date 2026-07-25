package roo_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/roo"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestRoundTrip_ProjectScope renders MCP + memory + commands at project scope,
// applies, ingests, and asserts they survive (MCP transport + headers, command
// description + argument-hint, memory byte-clean).
//
// MODEL-ANCHORED: this deliberately stays a model-level round-trip check (asserts the
// parsed MCP/memory/command MODEL survives). The on-disk BYTE oracle for the command
// artifact is now provided separately by TestRoundTrip_Command_ByteStable in this file,
// so this one is kept as the model-level unit check, not the byte-stability oracle.
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

// TestIngest_Command_WarnsOnDroppedRooKeys: Roo commands may carry keys the
// canonical command doesn't model (notably `mode`); ingest keeps the round-trip
// clean by dropping them, but must say so — a captured command re-applies
// without them.
func TestIngest_Command_WarnsOnDroppedRooKeys(t *testing.T) {
	tmp := t.TempDir()
	proj := t.TempDir()
	cmdDir := filepath.Join(proj, ".roo", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ndescription: Review code\nargument-hint: <file>\nmode: architect\n---\nReview it.\n"
	if err := os.WriteFile(filepath.Join(cmdDir, "review.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	a := roo.New(roo.Options{TargetRoot: tmp, Stderr: &warn})
	got, err := a.Ingest(adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commands) != 1 {
		t.Fatalf("expected one command, got %+v", got.Commands)
	}
	fm := got.Commands[0].Frontmatter
	if fm["description"] != "Review code" || fm["argument-hint"] != "<file>" {
		t.Fatalf("modeled keys must be captured: %+v", fm)
	}
	if _, ok := fm["mode"]; ok {
		t.Fatalf("mode must not be captured (no canonical home): %+v", fm)
	}
	if !strings.Contains(warn.String(), `command "review" frontmatter keys not modeled by agentsync dropped on import: "mode"`) {
		t.Fatalf("expected dropped-keys warning:\n%s", warn.String())
	}
}

// TestRoundTrip_Command_ByteStable is a byte-stability oracle for the Roo command
// artifact: render a command to disk, ingest the on-disk file, re-render from what
// was ingested, and assert the on-disk bytes are IDENTICAL. Unlike
// TestRoundTrip_ProjectScope (which asserts the parsed MODEL survives), this anchors
// on the on-disk artifact — a render→ingest→render that isn't a fixed point would
// churn the native file on every apply. Mirrors TestRoundTrip_ProjectScope's fixture.
func TestRoundTrip_Command_ByteStable(t *testing.T) {
	proj := t.TempDir()
	a := roo.New(roo.Options{TargetRoot: t.TempDir()})
	in := projOf(source.Canonical{
		Commands: []source.Command{{
			Name:        "deploy",
			Frontmatter: map[string]any{"description": "Deploy", "argument-hint": "<env>"},
			Body:        "Deploy it.\n",
		}},
	})

	render := func(c source.Canonical) {
		t.Helper()
		ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
			t.Fatal(err)
		}
	}

	// First render → apply → capture the on-disk command bytes.
	render(in)
	cmdPath := filepath.Join(proj, ".roo", "commands", "deploy.md")
	first, err := os.ReadFile(cmdPath)
	if err != nil {
		t.Fatalf("read rendered command: %v", err)
	}

	// Ingest the on-disk artifact, then re-render from what we captured. The ingested
	// canonical carries the command at top level (Project nil), which project-scope
	// Render renders directly.
	got, err := a.Ingest(adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commands) != 1 {
		t.Fatalf("ingest did not recover the command: %+v", got.Commands)
	}
	render(got)
	second, err := os.ReadFile(cmdPath)
	if err != nil {
		t.Fatalf("read re-rendered command: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Fatalf("command file not byte-stable across ingest→re-render:\nfirst:\n%q\nsecond:\n%q", first, second)
	}
}
