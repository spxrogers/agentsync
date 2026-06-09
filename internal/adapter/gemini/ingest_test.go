package gemini_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderApply renders c and commits the ops to disk via a PassThroughWriter so
// Ingest can read them back from the same paths.
func renderApply(t *testing.T, a *gemini.Adapter, c source.Canonical, scope adapter.Scope, project string) {
	t.Helper()
	ops, _, err := a.Render(secrets.ForRender(c), scope, project)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

// TestRoundTrip_MCP renders stdio + HTTP + SSE servers, applies, ingests, and
// asserts the canonical specs survive (the url/httpUrl transport split round-trips).
func TestRoundTrip_MCP(t *testing.T) {
	tmp := t.TempDir()
	a := gemini.New(gemini.Options{TargetRoot: tmp})
	in := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "stdio-srv", Server: source.MCPServerSpec{Type: "stdio", Command: "npx", Args: []string{"-y", "pkg"}, Env: map[string]string{"K": "v"}}},
		{ID: "http-srv", Server: source.MCPServerSpec{Type: "http", URL: "https://x/mcp", Headers: map[string]string{"A": "b"}}},
		{ID: "sse-srv", Server: source.MCPServerSpec{Type: "sse", URL: "https://x/sse"}},
	}}
	renderApply(t, a, in, adapter.ScopeUser, "")
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
	if s := byID["http-srv"]; s.Type != "http" || s.URL != "https://x/mcp" || s.Headers["A"] != "b" {
		t.Fatalf("http round-trip lost data: %+v", s)
	}
	if s := byID["sse-srv"]; s.Type != "sse" || s.URL != "https://x/sse" {
		t.Fatalf("sse round-trip lost data: %+v", s)
	}
}

// TestRoundTrip_MCP_ExtraPassthrough verifies a native MCP key agentsync doesn't
// model (timeout, trust) survives via Extra on the dest->source round-trip.
func TestRoundTrip_MCP_ExtraPassthrough(t *testing.T) {
	tmp := t.TempDir()
	a := gemini.New(gemini.Options{TargetRoot: tmp})
	// Ingest a native settings.json directly to exercise Extra capture.
	renderApply(t, a, source.Canonical{MCPServers: []source.MCPServer{{
		ID: "srv", Server: source.MCPServerSpec{Command: "x", Extra: map[string]any{"timeout": float64(5000), "trust": true}},
	}}}, adapter.ScopeUser, "")
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServers) != 1 {
		t.Fatalf("want 1 server, got %d", len(got.MCPServers))
	}
	extra := got.MCPServers[0].Server.Extra
	if extra["timeout"] == nil || extra["trust"] == nil {
		t.Fatalf("native keys not captured into Extra: %+v", extra)
	}
}

// TestRoundTrip_Hooks renders, applies, ingests, and asserts events reverse-map
// back to canonical names.
func TestRoundTrip_Hooks(t *testing.T) {
	tmp := t.TempDir()
	a := gemini.New(gemini.Options{TargetRoot: tmp})
	in := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Matcher: "write_file", Type: "command", Command: "echo a"},
		{Event: "Stop", Type: "command", Command: "echo b"},
	}}
	renderApply(t, a, in, adapter.ScopeUser, "")
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	type hk struct{ e, m, t_, c string }
	norm := func(hs []source.Hook) []hk {
		out := make([]hk, 0, len(hs))
		for _, h := range hs {
			out = append(out, hk{h.Event, h.Matcher, h.Type, h.Command})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].e < out[j].e })
		return out
	}
	want := []hk{{"PreToolUse", "write_file", "command", "echo a"}, {"Stop", "", "command", "echo b"}}
	if g := norm(got.Hooks); !reflect.DeepEqual(g, want) {
		t.Fatalf("hooks round-trip mismatch:\n got %+v\nwant %+v", g, want)
	}
}

// TestRoundTrip_Command verifies the body (prompt) and description survive the
// markdown->TOML->markdown round-trip.
func TestRoundTrip_Command(t *testing.T) {
	tmp := t.TempDir()
	a := gemini.New(gemini.Options{TargetRoot: tmp})
	in := source.Canonical{Commands: []source.Command{{
		Name: "deploy", Frontmatter: map[string]any{"description": "Deploy", "argument-hint": "<env>"}, Body: "Deploy to {{args}}.\n",
	}}}
	renderApply(t, a, in, adapter.ScopeUser, "")
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commands) != 1 || got.Commands[0].Name != "deploy" {
		t.Fatalf("command not ingested: %+v", got.Commands)
	}
	if got.Commands[0].Body != "Deploy to {{args}}.\n" {
		t.Fatalf("command body lost: %q", got.Commands[0].Body)
	}
	if got.Commands[0].Frontmatter["description"] != "Deploy" {
		t.Fatalf("description lost: %+v", got.Commands[0].Frontmatter)
	}
	// argument-hint is the documented projected loss for Gemini commands.
	if _, ok := got.Commands[0].Frontmatter["argument-hint"]; ok {
		t.Fatalf("argument-hint should not survive (dropped): %+v", got.Commands[0].Frontmatter)
	}
}

// TestRoundTrip_Subagent verifies name/description/model survive (tools/color are
// the documented projected loss).
func TestRoundTrip_Subagent(t *testing.T) {
	tmp := t.TempDir()
	a := gemini.New(gemini.Options{TargetRoot: tmp})
	in := source.Canonical{Subagents: []source.Subagent{{
		Name:        "auditor",
		Frontmatter: map[string]any{"description": "Audit", "model": "gemini-3-flash", "tools": []any{"Read"}},
		Body:        "Audit it.\n",
	}}}
	renderApply(t, a, in, adapter.ScopeUser, "")
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Subagents) != 1 {
		t.Fatalf("subagent not ingested: %+v", got.Subagents)
	}
	s := got.Subagents[0]
	if s.Name != "auditor" || s.Body != "Audit it.\n" {
		t.Fatalf("subagent name/body lost: %+v", s)
	}
	if s.Frontmatter["description"] != "Audit" || s.Frontmatter["model"] != "gemini-3-flash" {
		t.Fatalf("supported frontmatter lost: %+v", s.Frontmatter)
	}
	if _, ok := s.Frontmatter["tools"]; ok {
		t.Fatalf("tools should be dropped (vocabulary mismatch): %+v", s.Frontmatter)
	}
}

// TestRoundTrip_Memory verifies memory survives as GEMINI.md.
func TestRoundTrip_Memory(t *testing.T) {
	tmp := t.TempDir()
	a := gemini.New(gemini.Options{TargetRoot: tmp})
	body := "# Rules\n\nUse tabs.\n"
	renderApply(t, a, source.Canonical{Memory: source.Memory{Body: body}}, adapter.ScopeUser, "")
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Memory.Body != body {
		t.Fatalf("memory round-trip mismatch: %q", got.Memory.Body)
	}
}
