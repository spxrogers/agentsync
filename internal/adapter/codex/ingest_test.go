package codex_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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
	// Codex writes `name` (required) into the TOML from the effective name, which
	// falls back to the canonical name when no frontmatter name is set — so ingest
	// re-populates it unconditionally on presence.
	if sa.Frontmatter["name"] != "reviewer" {
		t.Fatalf("name should be re-populated from the TOML name field: %+v", sa.Frontmatter)
	}
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

// TestIngest_RoundTripsSubagent_DivergentName is the load-bearing fidelity test
// for issue #144: a subagent whose frontmatter `name` deliberately differs from
// its file stem must have that divergent name survive Render → Apply → Ingest.
// Before the fix, ingest reconstructed the name from the filename only and the
// frontmatter `name` was silently erased.
func TestIngest_RoundTripsSubagent_DivergentName(t *testing.T) {
	in := source.Canonical{Subagents: []source.Subagent{{
		Name: "reviewer",
		Frontmatter: map[string]any{
			"name":        "Senior Reviewer",
			"description": "code review agent",
			"model":       "gpt-5.5",
		},
		Body: "You are a code reviewer.\n",
	}}}
	out := roundTrip(t, in, adapter.ScopeUser, "")
	if len(out.Subagents) != 1 {
		t.Fatalf("want exactly one subagent, got %+v", out.Subagents)
	}
	sa := out.Subagents[0]
	if sa.Frontmatter["name"] != "Senior Reviewer" {
		t.Fatalf("divergent frontmatter name did not survive round-trip: %+v", sa.Frontmatter)
	}
	if sa.Frontmatter["description"] != "code review agent" {
		t.Fatalf("description missing: %+v", sa.Frontmatter)
	}
	if sa.Frontmatter["model"] != "gpt-5.5" {
		t.Fatalf("model missing: %+v", sa.Frontmatter)
	}
	if sa.Body != "You are a code reviewer.\n" {
		t.Fatalf("body (developer_instructions) roundtrip: got %q", sa.Body)
	}
}

// TestIngest_Subagent_RepopulatesName_WhenMatchingFilename covers the low finding
// that ingest must re-populate `name` even when it equals the file stem — the
// re-population is unconditional on presence, not gated on divergence.
func TestIngest_Subagent_RepopulatesName_WhenMatchingFilename(t *testing.T) {
	in := source.Canonical{Subagents: []source.Subagent{{
		Name: "ai-architect",
		Frontmatter: map[string]any{
			"name":        "ai-architect",
			"description": "Designs the system",
			"model":       "gpt-5.5",
		},
		Body: "Architect the thing.\n",
	}}}
	out := roundTrip(t, in, adapter.ScopeUser, "")
	if len(out.Subagents) != 1 {
		t.Fatalf("want exactly one subagent, got %+v", out.Subagents)
	}
	if got := out.Subagents[0].Frontmatter["name"]; got != "ai-architect" {
		t.Fatalf("name should be re-populated even when it matches the file stem, got %v: %+v",
			got, out.Subagents[0].Frontmatter)
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

// TestIngest_RoundTripsHooks_MultiEventMultiGroup exercises the fragile parts of
// the TOML arrays-of-tables round-trip: multiple matcher groups under one event
// AND multiple events. Cross-event order is map-iteration-dependent, so assert
// set membership, not slice order.
func TestIngest_RoundTripsHooks_MultiEventMultiGroup(t *testing.T) {
	in := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Matcher: "Bash", Type: "command", Command: "a"},
		{Event: "PreToolUse", Matcher: "Edit", Type: "command", Command: "b"},
		{Event: "Stop", Type: "command", Command: "c"},
	}}
	out := roundTrip(t, in, adapter.ScopeUser, "")
	if len(out.Hooks) != 3 {
		t.Fatalf("expected 3 hooks round-tripped, got %d: %+v", len(out.Hooks), out.Hooks)
	}
	type key struct{ event, matcher, command string }
	seen := map[key]bool{}
	for _, h := range out.Hooks {
		if h.Type != "command" {
			t.Fatalf("hook type mangled: %+v", h)
		}
		seen[key{h.Event.Unverified(), h.Matcher, h.Command}] = true
	}
	for _, want := range []key{
		{"PreToolUse", "Bash", "a"},
		{"PreToolUse", "Edit", "b"},
		{"Stop", "", "c"},
	} {
		if !seen[want] {
			t.Fatalf("missing hook %+v after round-trip; got %+v", want, out.Hooks)
		}
	}
}

// TestIngest_RoundTripsMCPAndHooksCoexist proves the INGEST surface handles an
// MCP server AND a hook that share config.toml: both must ingest back with their
// fields intact, and — because renderMCP and renderHooks each emit a separate
// merge-toml-keys FileOp for the SAME config.toml — the applied file on disk must
// carry BOTH the `[mcp_servers.*]` and `[hooks.*]` tables. If Apply's per-op
// merge clobbered instead of folding, one of the two tables would be missing.
// (This is the ingest-surface complement to #180's render-pipeline coexistence
// test; here we assert the ingested model AND the on-disk merge outcome.)
func TestIngest_RoundTripsMCPAndHooksCoexist(t *testing.T) {
	enabled := true
	in := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "github",
			Server: source.MCPServerSpec{
				Type: "stdio", Command: "npx", Args: []string{"-y", "server"},
				Env: map[string]string{"TOKEN": "abc"}, Enabled: &enabled,
			},
		}},
		Hooks: []source.Hook{
			{Event: "PreToolUse", Matcher: "Bash", Type: "command", Command: "echo hi"},
		},
	}

	// Mirror the roundTrip helper but keep a handle on tmp so we can read the
	// merged config.toml back off disk.
	tmp := t.TempDir()
	a := codex.New(codex.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// Exactly one MCP server, command/args/env intact.
	if len(out.MCPServers) != 1 {
		t.Fatalf("want exactly one MCP server, got %+v", out.MCPServers)
	}
	srv := out.MCPServers[0]
	if srv.ID != "github" {
		t.Fatalf("MCP id = %q, want github", srv.ID)
	}
	if srv.Server.Command != "npx" {
		t.Fatalf("command = %q, want npx", srv.Server.Command)
	}
	if len(srv.Server.Args) != 2 || srv.Server.Args[0] != "-y" || srv.Server.Args[1] != "server" {
		t.Fatalf("args = %v, want [-y server]", srv.Server.Args)
	}
	if srv.Server.Env["TOKEN"] != "abc" {
		t.Fatalf("env TOKEN = %q, want abc", srv.Server.Env["TOKEN"])
	}

	// Exactly one hook, event/matcher/type/command intact.
	if len(out.Hooks) != 1 {
		t.Fatalf("want exactly one hook, got %+v", out.Hooks)
	}
	h := out.Hooks[0]
	if h.Event != "PreToolUse" || h.Matcher != "Bash" || h.Type != "command" || h.Command != "echo hi" {
		t.Fatalf("hook roundtrip mismatch: %+v", h)
	}

	// The single config.toml on disk must carry BOTH tables — proof that the two
	// merge-toml-keys FileOps folded into one file rather than clobbering.
	cfgPath := codex.ResolvePaths(tmp, "", false).Config
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "[mcp_servers.") {
		t.Fatalf("config.toml missing [mcp_servers.*] table:\n%s", got)
	}
	if !strings.Contains(got, "[hooks.") {
		t.Fatalf("config.toml missing [hooks.*] table:\n%s", got)
	}
}

// TestIngest_SkillBundledFilesSurvive is the load-bearing bundled-file fidelity
// test: a spec-complete on-disk skill directory (SKILL.md + an executable
// scripts/run.sh + a binary assets/icon.bin) must survive ingest → render →
// apply byte-for-byte, and the script must keep its 0o755 permission bits. The
// oracle is the on-disk artifact at the rendered destination, not the parsed
// model.
func TestIngest_SkillBundledFilesSurvive(t *testing.T) {
	const skillName = "demo"
	scriptBytes := []byte("#!/bin/sh\necho hello\n")
	iconBytes := []byte{0x00, 0xff, 0x10, 0x80} // deliberately non-UTF-8

	// Build the spec-complete on-disk source skill directory.
	srcRoot := t.TempDir()
	skillDir := filepath.Join(srcRoot, ".agents", "skills", skillName)
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillMD := "---\nname: demo\ndescription: A demo skill\n---\nDo the demo.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(skillDir, "scripts", "run.sh")
	if err := os.WriteFile(scriptPath, scriptBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile is subject to the umask, so chmod explicitly to make the +x
	// bit real before ReadSkillFiles captures Mode().Perm().
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "assets", "icon.bin"), iconBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Load into canonical by ingesting the source root — this exercises the real
	// source.ReadSkillFiles capture path (SKILL.md skipped, bundled files kept
	// with their mode).
	loader := codex.New(codex.Options{TargetRoot: srcRoot})
	in, err := loader.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest source skill: %v", err)
	}
	if len(in.Skills) != 1 {
		t.Fatalf("want exactly one skill loaded, got %+v", in.Skills)
	}

	// Render + apply to a SECOND temp root and assert the rendered artifact.
	dstRoot := t.TempDir()
	a := codex.New(codex.Options{TargetRoot: dstRoot})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	rendered := filepath.Join(dstRoot, ".agents", "skills", skillName)
	cases := []struct {
		name      string
		relPath   string
		wantBytes []byte
		wantMode  os.FileMode // 0 means do not assert mode
	}{
		{name: "executable script", relPath: "scripts/run.sh", wantBytes: scriptBytes, wantMode: 0o755},
		{name: "binary asset", relPath: "assets/icon.bin", wantBytes: iconBytes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(rendered, filepath.FromSlash(tc.relPath))
			got, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read %s: %v", tc.relPath, err)
			}
			if !bytes.Equal(got, tc.wantBytes) {
				t.Fatalf("%s bytes changed on round-trip:\n got %v\nwant %v", tc.relPath, got, tc.wantBytes)
			}
			if tc.wantMode != 0 {
				info, err := os.Stat(p)
				if err != nil {
					t.Fatalf("stat %s: %v", tc.relPath, err)
				}
				if info.Mode().Perm() != tc.wantMode {
					t.Fatalf("%s mode = %o, want %o", tc.relPath, info.Mode().Perm(), tc.wantMode)
				}
			}
		})
	}
}

// TestIngest_RoundTripsSubagentBody_SpecComplete anchors the subagent body
// fidelity to a spec-complete artifact: a multi-paragraph developer_instructions
// body with blank lines, an embedded fenced code block, inline markdown, and a
// trailing newline must survive Render (→ TOML developer_instructions) → Apply →
// Ingest exactly, byte-for-byte.
func TestIngest_RoundTripsSubagentBody_SpecComplete(t *testing.T) {
	body := "First paragraph with **bold** and `inline code`.\n\n" +
		"Second paragraph before a fenced block:\n\n" +
		"```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```\n\n" +
		"Trailing paragraph with a list:\n\n- one\n- two\n"
	in := source.Canonical{Subagents: []source.Subagent{{
		Name: "writer",
		Frontmatter: map[string]any{
			"description": "prose agent",
			"model":       "gpt-5.5",
		},
		Body: body,
	}}}
	out := roundTrip(t, in, adapter.ScopeUser, "")
	if len(out.Subagents) != 1 {
		t.Fatalf("want exactly one subagent, got %+v", out.Subagents)
	}
	if got := out.Subagents[0].Body; got != body {
		t.Fatalf("developer_instructions body not preserved exactly:\n got %q\nwant %q", got, body)
	}
}

// TestIngest_MCP_EnabledFalse pins the `enabled -> Enabled` ingest: Codex's
// per-server `enabled = false` under [mcp_servers.<id>] must be captured into the
// canonical tri-state *bool (non-nil, false) and, being modeled, must NOT leak
// into Extra. The native config is written directly (never through render, which
// drops disabled servers before they reach config.toml), so this is the only way
// to exercise the disabled-server ingest.
func TestIngest_MCP_EnabledFalse(t *testing.T) {
	tmp := t.TempDir()
	cfg := codex.ResolvePaths(tmp, "", false).Config
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	native := "[mcp_servers.x]\ncommand = \"foo\"\nenabled = false\n"
	if err := os.WriteFile(cfg, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}

	a := codex.New(codex.Options{TargetRoot: tmp})
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.MCPServers) != 1 || out.MCPServers[0].ID != "x" {
		t.Fatalf("want exactly one MCP server 'x', got %+v", out.MCPServers)
	}
	srv := out.MCPServers[0].Server
	if srv.Enabled == nil {
		t.Fatalf("enabled key was present but Enabled is nil (want *false): %+v", srv)
	}
	if *srv.Enabled {
		t.Fatalf("Enabled = true, want false")
	}
	if _, ok := srv.Extra["enabled"]; ok {
		t.Fatalf("enabled is modeled and must be excluded from Extra: %+v", srv.Extra)
	}
}
