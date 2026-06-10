package continuedev_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestIngest_NativeBlock_RequestOptionsFidelity is the artifact-anchored guard
// for the requestOptions round trip: a hand-authored native block carrying
// non-headers request options (timeout, verifySsl) must survive
// ingest → re-render with those subkeys intact ON DISK — dropping them
// silently would let the next apply destroy the user's block.
func TestIngest_NativeBlock_RequestOptionsFidelity(t *testing.T) {
	tmp := t.TempDir()
	blockDir := filepath.Join(tmp, ".continue", "mcpServers")
	if err := os.MkdirAll(blockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	native := `name: api
version: 0.0.1
schema: v1
mcpServers:
  - name: api
    type: streamable-http
    url: https://api.example.com/mcp
    requestOptions:
      timeout: 5000
      verifySsl: false
      headers:
        Authorization: Bearer tok
`
	if err := os.WriteFile(filepath.Join(blockDir, "api.yaml"), []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	a := continuedev.New(continuedev.Options{TargetRoot: tmp})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServers) != 1 {
		t.Fatalf("expected one server, got %+v", got.MCPServers)
	}
	srv := got.MCPServers[0].Server
	if srv.Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("headers not captured: %+v", srv)
	}
	ro, ok := srv.Extra["requestOptions"].(map[string]any)
	if !ok {
		t.Fatalf("non-headers requestOptions must be preserved in Extra, got %+v", srv.Extra)
	}
	if _, ok := ro["timeout"]; !ok {
		t.Fatalf("requestOptions.timeout lost on ingest: %+v", ro)
	}
	if _, ok := ro["headers"]; ok {
		t.Fatalf("headers must live in the canonical Headers field, not Extra: %+v", ro)
	}

	// Re-render and assert the ON-DISK artifact still carries both the headers
	// and the residual request options.
	renderApply(t, a, got)
	data, err := os.ReadFile(filepath.Join(blockDir, "api.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"timeout", "verifySsl", "Authorization", "Bearer tok"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("re-rendered block lost %q:\n%s", want, data)
		}
	}
}

// TestIngest_Prompts_SkipsNonInvokable: Continue only treats a prompt as a slash
// command when `invokable: true`; capturing anything else (and re-applying it
// with invokable forced true) would silently convert a user's plain prompt into
// a slash command.
func TestIngest_Prompts_SkipsNonInvokable(t *testing.T) {
	tmp := t.TempDir()
	promptsDir := filepath.Join(tmp, ".continue", "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"plain.md":   "---\nname: plain\ndescription: not invokable\n---\nbody\n",
		"command.md": "---\nname: command\ndescription: a command\ninvokable: true\n---\nbody\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(promptsDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var warn bytes.Buffer
	a := continuedev.New(continuedev.Options{TargetRoot: tmp, Stderr: &warn})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commands) != 1 || got.Commands[0].Name != "command" {
		t.Fatalf("only the invokable prompt should be captured, got %+v", got.Commands)
	}
	if !strings.Contains(warn.String(), `prompt "plain" is not invokable`) {
		t.Fatalf("missing non-invokable warning:\n%s", warn.String())
	}
}
