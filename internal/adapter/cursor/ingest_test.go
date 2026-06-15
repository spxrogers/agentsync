package cursor_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/adapter/cursor"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderApply renders c at the given scope and commits the ops to disk under the
// adapter's target root via a PassThroughWriter, returning the adapter so the
// caller can Ingest from the same paths.
func renderApply(t *testing.T, a *cursor.Adapter, c source.Canonical, scope adapter.Scope, project string) {
	t.Helper()
	ops, _, err := a.Render(secrets.ForRender(c), scope, project)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

// TestRoundTrip_MCP renders both a stdio and a remote server, applies to disk,
// ingests, and asserts the canonical specs survive (Type included).
func TestRoundTrip_MCP(t *testing.T) {
	tmp := t.TempDir()
	a := cursor.New(cursor.Options{TargetRoot: tmp})
	in := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "stdio-srv", Server: source.MCPServerSpec{Type: "stdio", Command: "npx", Args: []string{"-y", "pkg"}, Env: map[string]string{"K": "v"}}},
		{ID: "http-srv", Server: source.MCPServerSpec{Type: "http", URL: "https://x/mcp", Headers: map[string]string{"A": "b"}}},
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
}

// TestRoundTrip_MCP_ExtraPassthrough verifies a native MCP key agentsync doesn't
// model (e.g. timeout) survives via Extra on the dest->source round-trip.
func TestRoundTrip_MCP_ExtraPassthrough(t *testing.T) {
	tmp := t.TempDir()
	mcpPath := filepath.Join(tmp, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	native := `{ "mcpServers": { "srv": { "command": "x", "timeout": 5000, "disabled": false } } }`
	if err := os.WriteFile(mcpPath, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	a := cursor.New(cursor.Options{TargetRoot: tmp})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServers) != 1 {
		t.Fatalf("want 1 server, got %d", len(got.MCPServers))
	}
	extra := got.MCPServers[0].Server.Extra
	if extra["timeout"] == nil || extra["disabled"] == nil {
		t.Fatalf("native keys not captured into Extra: %+v", extra)
	}
}

// TestRoundTrip_Hooks renders, applies, ingests, and asserts events reverse-map
// back to canonical (PascalCase) with a missing type defaulting to "command".
func TestRoundTrip_Hooks(t *testing.T) {
	tmp := t.TempDir()
	a := cursor.New(cursor.Options{TargetRoot: tmp})
	in := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Matcher: "Shell", Type: "command", Command: "echo a"},
		{Event: "SessionStart", Type: "command", Command: "echo b"},
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
	want := []hk{{"PreToolUse", "Shell", "command", "echo a"}, {"SessionStart", "", "command", "echo b"}}
	if g := norm(got.Hooks); !reflect.DeepEqual(g, want) {
		t.Fatalf("hooks round-trip mismatch:\n got %+v\nwant %+v", g, want)
	}
}

// TestIngest_Hooks_SkipsUnrepresentableEvents is the fidelity guard for the hooks
// capture path: Cursor's documented entry schema is wider than source.Hook
// (prompt-type hooks; timeout/failClosed/loop_limit fields), and apply owns the
// whole per-event array — so capturing a lossy subset would let the next apply
// rewrite the user's native entry without those fields. Ingest must instead leave
// the WHOLE event uncaptured, with a warning, while still capturing sibling
// events it can fully represent. Cursor-native events with no canonical
// equivalent must warn too.
func TestIngest_Hooks_SkipsUnrepresentableEvents(t *testing.T) {
	tmp := t.TempDir()
	hooksPath := filepath.Join(tmp, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	native := `{
  "version": 1,
  "hooks": {
    "preToolUse": [ { "command": "echo ok", "matcher": "Shell" } ],
    "postToolUse": [ { "command": "echo slow", "timeout": 30 } ],
    "sessionStart": [ { "type": "prompt", "prompt": "review the diff", "model": "fast" } ],
    "afterFileEdit": [ { "command": "./format.sh" } ]
  }
}`
	if err := os.WriteFile(hooksPath, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	a := cursor.New(cursor.Options{TargetRoot: tmp, Stderr: &warn})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Hooks) != 1 || got.Hooks[0].Event != "PreToolUse" || got.Hooks[0].Command != "echo ok" {
		t.Fatalf("only the fully-representable preToolUse event should be captured, got %+v", got.Hooks)
	}
	out := warn.String()
	for _, wantMsg := range []string{
		`unmodeled fields (timeout)`,
		`"prompt"-type entry`,
		`"afterFileEdit" has no canonical equivalent`,
	} {
		if !strings.Contains(out, wantMsg) {
			t.Errorf("missing warning %q in:\n%s", wantMsg, out)
		}
	}
}

// TestRoundTrip_Command verifies the body survives (frontmatter is the
// documented, reported loss for Cursor commands).
func TestRoundTrip_Command(t *testing.T) {
	tmp := t.TempDir()
	a := cursor.New(cursor.Options{TargetRoot: tmp})
	in := source.Canonical{Commands: []source.Command{{
		Name: "deploy", Frontmatter: map[string]any{"description": "Deploy"}, Body: "Run the deploy.\n",
	}}}
	renderApply(t, a, in, adapter.ScopeUser, "")
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commands) != 1 || got.Commands[0].Name != "deploy" {
		t.Fatalf("command not ingested: %+v", got.Commands)
	}
	if got.Commands[0].Body != "Run the deploy.\n" {
		t.Fatalf("command body lost: %q", got.Commands[0].Body)
	}
	if len(got.Commands[0].Frontmatter) != 0 {
		t.Fatalf("Cursor commands carry no frontmatter; got %+v", got.Commands[0].Frontmatter)
	}
}

// TestRoundTrip_Subagent verifies supported frontmatter (description/model/
// readonly) survives and the body is preserved.
func TestRoundTrip_Subagent(t *testing.T) {
	tmp := t.TempDir()
	a := cursor.New(cursor.Options{TargetRoot: tmp})
	in := source.Canonical{Subagents: []source.Subagent{{
		Name:        "auditor",
		Frontmatter: map[string]any{"description": "Audit", "model": "inherit", "readonly": true},
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
	if s.Frontmatter["description"] != "Audit" || s.Frontmatter["model"] != "inherit" || s.Frontmatter["readonly"] != true {
		t.Fatalf("supported frontmatter lost: %+v", s.Frontmatter)
	}
}

// TestRoundTrip_Memory_ProjectScope verifies project memory survives as AGENTS.md.
func TestRoundTrip_Memory_ProjectScope(t *testing.T) {
	proj := t.TempDir()
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	body := "# Project rules\n\nUse tabs.\n"
	in := source.Canonical{
		Memory:  source.Memory{Body: body},
		Project: &source.Canonical{Memory: source.Memory{Body: body}},
	}
	renderApply(t, a, in, adapter.ScopeProject, proj)
	got, err := a.Ingest(adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if got.Memory.Body != body {
		t.Fatalf("memory round-trip mismatch: %q", got.Memory.Body)
	}
}

// TestRoundTrip_Skill_ArtifactFidelity anchors to the ON-DISK artifact (per
// CLAUDE.md): a spec-complete skill directory (SKILL.md + bundled binary/script)
// ingested then re-rendered to a fresh target must reproduce every file
// byte-for-byte, with the executable bit preserved.
func TestRoundTrip_Skill_ArtifactFidelity(t *testing.T) {
	src := t.TempDir()
	skillDir := filepath.Join(src, ".cursor", "skills", "demo")
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo\ndescription: Demo skill\n---\nBody.\n"), 0o644)
	writeFile(t, filepath.Join(skillDir, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755)
	writeFile(t, filepath.Join(skillDir, "assets", "logo.bin"), []byte{0x00, 0x01, 0x02, 0xff}, 0o644)

	a := cursor.New(cursor.Options{TargetRoot: src})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 1 {
		t.Fatalf("want 1 skill, got %d", len(got.Skills))
	}

	// Re-render to a fresh target and compare the two skill trees byte-for-byte.
	dst := t.TempDir()
	b := cursor.New(cursor.Options{TargetRoot: dst})
	renderApply(t, b, got, adapter.ScopeUser, "")

	srcSkill := filepath.Join(src, ".cursor", "skills", "demo")
	dstSkill := filepath.Join(dst, ".cursor", "skills", "demo")
	// Bundled scripts/assets are the verbatim-fidelity guarantee — binary content
	// and the executable bit must survive byte-for-byte. (SKILL.md frontmatter key
	// order is normalized on re-encode across ALL adapters, so it is compared
	// semantically below, not byte-for-byte.)
	for _, rel := range []string{"scripts/run.sh", "assets/logo.bin"} {
		sb, err := os.ReadFile(filepath.Join(srcSkill, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		db, err := os.ReadFile(filepath.Join(dstSkill, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("re-rendered skill missing %s: %v", rel, err)
		}
		if string(sb) != string(db) {
			t.Fatalf("skill file %s not byte-identical after round-trip:\n src %q\n dst %q", rel, sb, db)
		}
	}
	si, _ := os.Stat(filepath.Join(dstSkill, "scripts", "run.sh"))
	if si.Mode().Perm()&0o100 == 0 {
		t.Fatalf("executable bit not preserved on round-trip: %v", si.Mode())
	}
	// SKILL.md: frontmatter survives semantically (key order normalized).
	srcFM, srcBody, _ := claude.ParseFrontmatter(mustRead(t, filepath.Join(srcSkill, "SKILL.md")))
	dstFM, dstBody, _ := claude.ParseFrontmatter(mustRead(t, filepath.Join(dstSkill, "SKILL.md")))
	if srcBody != dstBody {
		t.Fatalf("SKILL.md body changed: %q vs %q", srcBody, dstBody)
	}
	if !reflect.DeepEqual(srcFM, dstFM) {
		t.Fatalf("SKILL.md frontmatter changed: %+v vs %+v", srcFM, dstFM)
	}
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
