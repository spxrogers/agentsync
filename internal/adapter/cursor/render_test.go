package cursor_test

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/adapter/cursor"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// findOp returns the first op whose path ends with suffix.
func findOp(ops []adapter.FileOp, suffix string) *adapter.FileOp {
	for i := range ops {
		if strings.HasSuffix(filepath.ToSlash(ops[i].Path), suffix) {
			return &ops[i]
		}
	}
	return nil
}

func hasSkip(skips []adapter.Skip, component, name string, kind adapter.SkipKind) *adapter.Skip {
	for i := range skips {
		if skips[i].Component == component && skips[i].Name == name && skips[i].Kind == kind {
			return &skips[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// MCP — .cursor/mcp.json, same mcpServers shape as Claude
// ---------------------------------------------------------------------------

func TestRender_MCP_Stdio(t *testing.T) {
	enabled := true
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "github",
		Server: source.MCPServerSpec{
			Type: "stdio", Command: "npx", Args: []string{"-y", "x"},
			Env: map[string]string{"TOKEN": "abc"}, Agents: []string{"cursor"}, Enabled: &enabled,
		},
	}}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	op := findOp(ops, ".cursor/mcp.json")
	if op == nil {
		t.Fatal("mcp.json op missing")
	}
	if op.MergeStrategy != "merge-json-keys" {
		t.Fatalf("merge strategy = %q, want merge-json-keys", op.MergeStrategy)
	}
	var ours map[string]any
	if err := json.Unmarshal(op.Content, &ours); err != nil {
		t.Fatalf("op.Content is not JSON: %v\n%s", err, op.Content)
	}
	srv := ours["mcpServers"].(map[string]any)["github"].(map[string]any)
	if srv["command"] != "npx" || srv["type"] != "stdio" {
		t.Fatalf("stdio server malformed: %v", srv)
	}
	if env := srv["env"].(map[string]any); env["TOKEN"] != "abc" {
		t.Fatalf("env dropped: %v", srv)
	}
}

func TestRender_MCP_RemoteHeaders(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID: "figma",
		Server: source.MCPServerSpec{
			Type: "http", URL: "https://mcp.figma.com/mcp",
			Headers: map[string]string{"X-Region": "us"},
		},
	}}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".cursor/mcp.json")
	if op == nil {
		t.Fatal("mcp.json op missing")
	}
	var ours map[string]any
	_ = json.Unmarshal(op.Content, &ours)
	srv := ours["mcpServers"].(map[string]any)["figma"].(map[string]any)
	if srv["url"] != "https://mcp.figma.com/mcp" {
		t.Fatalf("url = %v", srv["url"])
	}
	if _, hasCmd := srv["command"]; hasCmd {
		t.Fatalf("remote server must not have command: %v", srv)
	}
	if h := srv["headers"].(map[string]any); h["X-Region"] != "us" {
		t.Fatalf("headers dropped: %v", srv)
	}
}

// TestRender_MCP_Remote_OmitsType asserts a remote (url-bearing) server carries
// NO `type` key — Cursor's documented remote schema is url+headers only — while a
// stdio server still carries `type`. Decodes mcp.json from op.Content.
func TestRender_MCP_Remote_OmitsType(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "remote", Server: source.MCPServerSpec{
			Type: "http", URL: "https://mcp.example.com/mcp",
			Headers: map[string]string{"X-Region": "us"},
		}},
		{ID: "local", Server: source.MCPServerSpec{
			Type: "stdio", Command: "npx", Args: []string{"-y", "pkg"},
		}},
	}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	op := findOp(ops, ".cursor/mcp.json")
	if op == nil {
		t.Fatal("mcp.json op missing")
	}
	var ours map[string]any
	if err := json.Unmarshal(op.Content, &ours); err != nil {
		t.Fatalf("op.Content is not JSON: %v\n%s", err, op.Content)
	}
	servers := ours["mcpServers"].(map[string]any)

	remote := servers["remote"].(map[string]any)
	if _, hasType := remote["type"]; hasType {
		t.Fatalf("remote server must NOT carry a `type` key (Cursor remote schema is url+headers): %v", remote)
	}
	if remote["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("remote url dropped: %v", remote)
	}
	if h := remote["headers"].(map[string]any); h["X-Region"] != "us" {
		t.Fatalf("remote headers dropped: %v", remote)
	}

	local := servers["local"].(map[string]any)
	if local["type"] != "stdio" {
		t.Fatalf("stdio server must still carry type=stdio: %v", local)
	}
	if local["command"] != "npx" {
		t.Fatalf("stdio command dropped: %v", local)
	}
}

func TestRender_MCP_SkipsDisabledAndOtherAgents(t *testing.T) {
	disabled := false
	c := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "off", Server: source.MCPServerSpec{Command: "x", Enabled: &disabled}},
		{ID: "claude-only", Server: source.MCPServerSpec{Command: "x", Agents: []string{"claude"}}},
	}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if op := findOp(ops, "mcp.json"); op != nil {
		t.Fatalf("should emit no mcp.json op for disabled/other-agent servers: %s", op.Content)
	}
}

func TestRender_MCP_ProjectScope_Path(t *testing.T) {
	proj := t.TempDir()
	c := source.Canonical{MCPServers: []source.MCPServer{{ID: "x", Server: source.MCPServerSpec{Command: "y"}}}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(proj, ".cursor", "mcp.json")
	if op := findOp(ops, ".cursor/mcp.json"); op == nil || op.Path != want {
		t.Fatalf("project-scope mcp.json path wrong: got %v want %s", ops, want)
	}
}

// ---------------------------------------------------------------------------
// Memory — AGENTS.md at project scope; skipped at user scope (app-local storage)
// ---------------------------------------------------------------------------

func TestRender_Memory_ProjectScope(t *testing.T) {
	proj := t.TempDir()
	c := source.Canonical{
		Memory:  source.Memory{Body: "# Memory\n\nBe helpful.\n"},
		Project: &source.Canonical{Memory: source.Memory{Body: "# Memory\n\nBe helpful.\n"}},
	}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	op := findOp(ops, "AGENTS.md")
	if op == nil {
		t.Fatal("AGENTS.md op missing at project scope")
	}
	if op.Path != filepath.Join(proj, "AGENTS.md") {
		t.Fatalf("memory should land at repo-root AGENTS.md: %s", op.Path)
	}
	if !strings.Contains(string(op.Content), "Be helpful") {
		t.Fatalf("memory body missing: %s", op.Content)
	}
}

func TestRender_Memory_UserScope_Skipped(t *testing.T) {
	c := source.Canonical{Memory: source.Memory{Body: "# Memory\n"}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, skips, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if op := findOp(ops, "AGENTS.md"); op != nil {
		t.Fatalf("user-scope memory must not be written (app-local storage): %s", op.Path)
	}
	if hasSkip(skips, "memory", "AGENTS.md", adapter.SkipDropped) == nil {
		t.Fatalf("expected a user-scope memory skip, got %+v", skips)
	}
}

// ---------------------------------------------------------------------------
// Skill — .cursor/skills/<name>/
// ---------------------------------------------------------------------------

func TestRender_Skill_Dir(t *testing.T) {
	c := source.Canonical{Skills: []source.Skill{{
		Name:        "my-skill",
		Frontmatter: map[string]any{"description": "My skill"},
		Body:        "Skill body.\n",
		Files:       []source.SkillFile{{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0o755}},
	}}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".cursor/skills/my-skill/SKILL.md")
	if op == nil {
		t.Fatal("SKILL.md op missing under .cursor/skills")
	}
	if !strings.Contains(string(op.Content), "Skill body") {
		t.Fatalf("skill body missing: %s", op.Content)
	}
	bundled := findOp(ops, ".cursor/skills/my-skill/scripts/run.sh")
	if bundled == nil {
		t.Fatal("bundled skill file not projected")
	}
	if bundled.Mode != 0o755 {
		t.Fatalf("bundled file mode not preserved: %o", bundled.Mode)
	}
}

// ---------------------------------------------------------------------------
// Subagent — markdown; tools/color dropped with a Skip
// ---------------------------------------------------------------------------

func TestRender_Subagent_DropsToolsAndColor(t *testing.T) {
	c := source.Canonical{Subagents: []source.Subagent{{
		Name: "review",
		Frontmatter: map[string]any{
			"description": "Code review",
			"model":       "inherit",
			"readonly":    true,
			"tools":       []any{"Read", "Grep"},
			"color":       "blue",
		},
		Body: "Review the code.\n",
	}}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, skips, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".cursor/agents/review.md")
	if op == nil {
		t.Fatal("review.md op missing")
	}
	// Decode the rendered frontmatter and assert dropped KEYS are absent — a
	// whole-file strings.Contains("tools"/"color") is false-passable (a value or
	// body text could carry the substring, and a masked key would go unnoticed).
	fm, body, err := claude.ParseFrontmatter(op.Content)
	if err != nil {
		t.Fatalf("rendered subagent frontmatter did not parse: %v\n%s", err, op.Content)
	}
	if body != "Review the code.\n" {
		t.Fatalf("body not preserved verbatim: %q", body)
	}
	if fm["description"] != "Code review" {
		t.Fatalf("description key should be preserved: %+v", fm)
	}
	if fm["readonly"] != true {
		t.Fatalf("readonly key should be preserved: %+v", fm)
	}
	if _, ok := fm["tools"]; ok {
		t.Fatalf("tools key must be dropped from rendered frontmatter: %+v", fm)
	}
	if _, ok := fm["color"]; ok {
		t.Fatalf("color key must be dropped from rendered frontmatter: %+v", fm)
	}
	sk := hasSkip(skips, "subagent", "review", adapter.SkipReduced)
	if sk == nil || !strings.Contains(sk.Reason, "tools") || !strings.Contains(sk.Reason, "color") {
		t.Fatalf("expected a reduced subagent skip listing tools+color, got %+v", skips)
	}
}

// TestRender_Subagent_IsBackground asserts that `is_background: true` — a Cursor
// subagent key (cursorAgentKnownKeys) — survives into the rendered
// `.cursor/agents/<name>.md` frontmatter and is NOT reported as a dropped key in
// any reduced Skip.
func TestRender_Subagent_IsBackground(t *testing.T) {
	c := source.Canonical{Subagents: []source.Subagent{{
		Name: "bg",
		Frontmatter: map[string]any{
			"description":   "Runs in the background",
			"is_background": true,
		},
		Body: "Do background work.\n",
	}}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	op := findOp(ops, ".cursor/agents/bg.md")
	if op == nil {
		t.Fatal("bg.md op missing")
	}
	fm, _, err := claude.ParseFrontmatter(op.Content)
	if err != nil {
		t.Fatalf("rendered subagent frontmatter did not parse: %v\n%s", err, op.Content)
	}
	if fm["is_background"] != true {
		t.Fatalf("is_background:true must survive into rendered frontmatter: %+v", fm)
	}
	// is_background is a supported key — it must not appear in any reduced Skip.
	for _, sk := range skips {
		if sk.Kind == adapter.SkipReduced && strings.Contains(sk.Reason, "is_background") {
			t.Fatalf("is_background is supported and must not be reported as dropped: %+v", sk)
		}
	}
}

// ---------------------------------------------------------------------------
// Command — plain markdown body; frontmatter dropped with a Skip; both scopes
// ---------------------------------------------------------------------------

func TestRender_Command_BodyOnly(t *testing.T) {
	c := source.Canonical{Commands: []source.Command{{
		Name: "summarize",
		Frontmatter: map[string]any{
			"description":   "Summarize code",
			"argument-hint": "<file>",
		},
		Body: "Summarize $ARGUMENTS.\n",
	}}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, skips, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".cursor/commands/summarize.md")
	if op == nil {
		t.Fatal("summarize.md command op missing")
	}
	// Cursor commands are plain markdown (no frontmatter). Decode and assert the
	// rendered file has NO frontmatter keys at all — precise, unlike a
	// strings.Contains("---"/"argument-hint") over the whole file.
	fm, body, err := claude.ParseFrontmatter(op.Content)
	if err != nil {
		t.Fatalf("rendered command did not parse: %v\n%s", err, op.Content)
	}
	if len(fm) != 0 {
		t.Fatalf("Cursor commands carry no frontmatter; rendered keys %v: %s", sortedFMKeys(fm), op.Content)
	}
	if body != "Summarize $ARGUMENTS.\n" {
		t.Fatalf("body should be written verbatim, got: %q", body)
	}
	sk := hasSkip(skips, "command", "summarize", adapter.SkipReduced)
	if sk == nil || !strings.Contains(sk.Reason, "argument-hint") || !strings.Contains(sk.Reason, "description") {
		t.Fatalf("expected a reduced command skip listing dropped keys, got %+v", skips)
	}
}

// sortedFMKeys returns the frontmatter map's keys sorted, for stable test output.
func sortedFMKeys(fm map[string]any) []string {
	out := make([]string, 0, len(fm))
	for k := range fm {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestRender_Command_ProjectScope_Path(t *testing.T) {
	proj := t.TempDir()
	c := source.Canonical{
		Commands: []source.Command{{Name: "x", Body: "y\n"}},
		Project:  &source.Canonical{Commands: []source.Command{{Name: "x", Body: "y\n"}}},
	}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	want := filepath.Join(proj, ".cursor", "commands", "x.md")
	if op := findOp(ops, ".cursor/commands/x.md"); op == nil || op.Path != want {
		t.Fatalf("project-scope command path wrong; want %s, ops=%+v", want, ops)
	}
}

// ---------------------------------------------------------------------------
// Hooks — .cursor/hooks.json, camelCase events, flat entries, unknown skipped
// ---------------------------------------------------------------------------

func TestRender_Hooks_MapsEventsAndShape(t *testing.T) {
	c := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Matcher: "Shell", Type: "command", Command: "echo hi"},
		{Event: "UserPromptSubmit", Type: "command", Command: "echo prompt"},
		{Event: "Notification", Type: "command", Command: "echo nope"}, // no Cursor equivalent
	}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, skips, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	op := findOp(ops, ".cursor/hooks.json")
	if op == nil {
		t.Fatal("hooks.json op missing")
	}
	if op.MergeStrategy != "merge-json-keys" {
		t.Fatalf("merge strategy = %q, want merge-json-keys", op.MergeStrategy)
	}
	var ours map[string]any
	_ = json.Unmarshal(op.Content, &ours)
	hooks := ours["hooks"].(map[string]any)
	// camelCase mapping
	pre, ok := hooks["preToolUse"].([]any)
	if !ok || len(pre) != 1 {
		t.Fatalf("preToolUse not mapped: %s", op.Content)
	}
	entry := pre[0].(map[string]any)
	if entry["command"] != "echo hi" || entry["matcher"] != "Shell" {
		t.Fatalf("flat hook entry shape wrong: %v", entry)
	}
	if _, hasNested := entry["hooks"]; hasNested {
		t.Fatalf("Cursor hook entries are flat, must not nest a 'hooks' array: %v", entry)
	}
	if _, hasType := entry["type"]; hasType {
		t.Fatalf("default type 'command' should be omitted: %v", entry)
	}
	if _, ok := hooks["beforeSubmitPrompt"]; !ok {
		t.Fatalf("UserPromptSubmit should map to beforeSubmitPrompt: %s", op.Content)
	}
	// op.Content must NOT carry a top-level version (injected post-merge in apply).
	if _, hasVersion := ours["version"]; hasVersion {
		t.Fatalf("version must NOT be in op.Content (it would become an owned key): %s", op.Content)
	}
	// OwnedKeys are scoped to /hooks/<event>, never /version.
	for _, k := range op.OwnedKeys {
		if k == "/version" {
			t.Fatalf("/version must never be an owned key: %v", op.OwnedKeys)
		}
	}
	if hasSkip(skips, "hook", "Notification", adapter.SkipDropped) == nil {
		t.Fatalf("expected a skip for unmapped event Notification, got %+v", skips)
	}
}

// TestRender_Hooks_SkipsNonCommandType: agentsync models only command hooks; a
// canonical hook with another type must be skipped with a report rather than
// rendered as a half-formed entry Cursor can't run.
func TestRender_Hooks_SkipsNonCommandType(t *testing.T) {
	c := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Type: "prompt", Command: ""},
		{Event: "SessionStart", Type: "command", Command: "echo ok"},
	}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	op := findOp(ops, ".cursor/hooks.json")
	if op == nil {
		t.Fatal("hooks.json op missing (the command hook must still render)")
	}
	if strings.Contains(string(op.Content), "preToolUse") {
		t.Fatalf("prompt-type hook must not render: %s", op.Content)
	}
	sk := hasSkip(skips, "hook", "PreToolUse", adapter.SkipDropped)
	if sk == nil || !strings.Contains(sk.Reason, `type "prompt"`) {
		t.Fatalf("expected a typed skip for the prompt hook, got %+v", skips)
	}
}

// ---------------------------------------------------------------------------
// LSP — always skipped
// ---------------------------------------------------------------------------

func TestRender_LSP_Skipped(t *testing.T) {
	c := source.Canonical{LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	_, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if hasSkip(skips, "lsp", "gopls", adapter.SkipDropped) == nil {
		t.Fatalf("expected an lsp skip, got %+v", skips)
	}
}

// ---------------------------------------------------------------------------
// Project scope renders only the project overlay
// ---------------------------------------------------------------------------

func TestRender_ProjectScope_OnlyProjectItems(t *testing.T) {
	projSkill := source.Skill{Name: "proj-skill", Frontmatter: map[string]any{"name": "proj-skill"}, Body: "Project.\n"}
	projRoot := t.TempDir()
	projCanon := source.Canonical{Skills: []source.Skill{projSkill}}
	merged := source.Canonical{
		Skills: []source.Skill{
			{Name: "user-skill", Frontmatter: map[string]any{"name": "user-skill"}, Body: "User.\n"},
			projSkill,
		},
		Project: &projCanon,
	}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(merged), adapter.ScopeProject, projRoot)
	if err != nil {
		t.Fatal(err)
	}
	wantUser := filepath.Join(projRoot, ".cursor", "skills", "user-skill", "SKILL.md")
	wantProj := filepath.Join(projRoot, ".cursor", "skills", "proj-skill", "SKILL.md")
	var gotProj bool
	for _, op := range ops {
		if op.Path == wantUser {
			t.Fatalf("user-scope skill must not be written at project scope: %s", op.Path)
		}
		if op.Path == wantProj {
			gotProj = true
		}
	}
	if !gotProj {
		t.Fatalf("project skill not rendered; want %s, ops=%+v", wantProj, ops)
	}
}
