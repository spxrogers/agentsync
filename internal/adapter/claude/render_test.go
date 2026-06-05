package claude_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestRender_MCP_UserScope(t *testing.T) {
	enabled := true
	c := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "github",
			Server: source.MCPServerSpec{
				Type:    "stdio",
				Command: "npx",
				Args:    []string{"-y", "@modelcontextprotocol/server-github"},
				Env:     map[string]string{"GITHUB_TOKEN": "xyz"},
				Agents:  []string{"*"},
				Enabled: &enabled,
			},
		}},
	}
	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	ops, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %+v", skips)
	}
	// The MCP write goes into .claude.json under user scope.
	var found bool
	for _, op := range ops {
		if strings.HasSuffix(op.Path, ".claude.json") {
			found = true
			if op.MergeStrategy != "merge-json-keys" {
				t.Fatalf("expected MergeStrategy=merge-json-keys, got %q", op.MergeStrategy)
			}
			var got map[string]any
			if err := json.Unmarshal(op.Content, &got); err != nil {
				t.Fatalf("not valid json: %v", err)
			}
			srv := got["mcpServers"].(map[string]any)["github"].(map[string]any)
			if srv["command"] != "npx" {
				t.Fatalf("command = %v", srv["command"])
			}
		}
	}
	if !found {
		t.Fatalf(".claude.json op not produced: %+v", ops)
	}
}

func TestRender_MCP_AgentsAllowlist(t *testing.T) {
	enabled := true
	c := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "private",
			Server: source.MCPServerSpec{
				Type:    "stdio",
				Command: "x",
				Agents:  []string{"opencode"}, // claude not in list
				Enabled: &enabled,
			},
		}},
	}
	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	// No .claude.json write because the server isn't targeted at claude.
	for _, op := range ops {
		if strings.HasSuffix(op.Path, ".claude.json") {
			t.Fatalf("expected no .claude.json op when allowlist excludes claude")
		}
	}
}

func TestRender_Memory(t *testing.T) {
	c := source.Canonical{
		Memory: source.Memory{
			Body: "# Personal style\n\n@import ./fragments/style.md\n\nMore.\n",
			Fragments: map[string]string{
				"style.md": "Use semicolons.\n",
			},
		},
	}
	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	var memOp *adapter.FileOp
	for i, op := range ops {
		if strings.HasSuffix(op.Path, "/CLAUDE.md") {
			memOp = &ops[i]
		}
	}
	if memOp == nil {
		t.Fatalf("no CLAUDE.md op")
	}
	if !strings.Contains(string(memOp.Content), "Use semicolons.") {
		t.Fatalf("fragment not inlined: %s", memOp.Content)
	}
	if strings.Contains(string(memOp.Content), "@import") {
		t.Fatalf("@import directive leaked: %s", memOp.Content)
	}
}

func TestRender_Skills(t *testing.T) {
	c := source.Canonical{
		Skills: []source.Skill{{
			Name:        "review",
			Frontmatter: map[string]any{"name": "review", "description": "Review code"},
			Body:        "Do a code review.\n",
		}},
	}
	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	var found *adapter.FileOp
	for i, op := range ops {
		if strings.Contains(op.Path, "/skills/review/SKILL.md") {
			found = &ops[i]
		}
	}
	if found == nil {
		t.Fatalf("no SKILL.md op")
	}
	if !strings.HasPrefix(string(found.Content), "---\n") {
		t.Fatalf("missing frontmatter delimiter: %s", found.Content)
	}
}

func TestRender_LSP_WritesSettingsJSON(t *testing.T) {
	c := source.Canonical{
		LSPServers: []source.LSPServer{{
			ID: "gopls",
			Spec: source.LSPServerSpec{
				Command: "gopls",
				Args:    []string{"-mode=stdio"},
			},
		}},
	}
	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	var found *adapter.FileOp
	for i, op := range ops {
		if strings.HasSuffix(op.Path, "settings.json") && strings.Contains(string(op.Content), "lspServers") {
			found = &ops[i]
		}
	}
	if found == nil {
		t.Fatalf("no settings.json lspServers op: %+v", ops)
	}
	if found.MergeStrategy != "merge-json-keys" {
		t.Fatalf("MergeStrategy = %q, want merge-json-keys", found.MergeStrategy)
	}
	// OwnedKeys should include /lspServers/gopls
	hasOwned := false
	for _, k := range found.OwnedKeys {
		if k == "/lspServers/gopls" {
			hasOwned = true
		}
	}
	if !hasOwned {
		t.Fatalf("OwnedKeys missing /lspServers/gopls: %v", found.OwnedKeys)
	}
	// Content should be valid JSON with lspServers.gopls.command
	var got map[string]any
	if err := json.Unmarshal(found.Content, &got); err != nil {
		t.Fatalf("not valid json: %v", err)
	}
	servers, ok := got["lspServers"].(map[string]any)
	if !ok {
		t.Fatalf("lspServers key missing or wrong type: %v", got)
	}
	gopls, ok := servers["gopls"].(map[string]any)
	if !ok {
		t.Fatalf("gopls key missing or wrong type: %v", servers)
	}
	if gopls["command"] != "gopls" {
		t.Fatalf("command = %v", gopls["command"])
	}
}

func TestRender_LSP_EmptyProducesNoOp(t *testing.T) {
	c := source.Canonical{}
	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if strings.HasSuffix(op.Path, "settings.json") && strings.Contains(string(op.Content), "\"lspServers\"") {
			t.Fatalf("unexpected lspServers op when no LSP in canonical: %+v", op)
		}
	}
}

// TestRender_ProjectScope_OnlyProjectItems verifies that apply --scope project
// writes only the project-overlay items (c.Project) to the project directory,
// not the merged canonical that also includes user-scope items.
func TestRender_ProjectScope_OnlyProjectItems(t *testing.T) {
	projSkill := source.Skill{
		Name:        "proj-review",
		Frontmatter: map[string]any{"name": "proj-review", "description": "project skill"},
		Body:        "Project review.\n",
	}
	projRoot := t.TempDir()
	projCanon := source.Canonical{
		Skills: []source.Skill{projSkill},
	}
	// Merged canonical has both a user skill and the project skill, with
	// Project set to the project-only canonical — exactly what project.Merge
	// produces after the fix.
	merged := source.Canonical{
		Skills: []source.Skill{
			{Name: "user-skill", Frontmatter: map[string]any{"name": "user-skill"}, Body: "User skill.\n"},
			projSkill,
		},
		Project: &projCanon,
	}

	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(merged), adapter.ScopeProject, projRoot)
	if err != nil {
		t.Fatal(err)
	}

	skillPaths := map[string]bool{}
	for _, op := range ops {
		if strings.Contains(op.Path, "/skills/") {
			skillPaths[op.Path] = true
		}
	}
	if skillPaths[strings.Join([]string{projRoot, ".claude", "skills", "user-skill", "SKILL.md"}, "/")] {
		t.Fatalf("user-scope skill must not be written at project scope: %v", skillPaths)
	}
	found := false
	for p := range skillPaths {
		if strings.Contains(p, "proj-review") {
			found = true
		}
	}
	if !found {
		t.Fatalf("project skill not rendered at project scope: ops=%+v", ops)
	}
}

func TestRender_Hooks_WritesSettingsJSON(t *testing.T) {
	c := source.Canonical{
		Hooks: []source.Hook{
			{Event: "PreToolUse", Matcher: "Write|Edit", Type: "command", Command: "echo intercepting"},
			{Event: "PreToolUse", Matcher: "Bash", Type: "command", Command: "echo bash hook"},
		},
	}
	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	var found *adapter.FileOp
	for i, op := range ops {
		if strings.HasSuffix(op.Path, "settings.json") && strings.Contains(string(op.Content), "hooks") {
			found = &ops[i]
		}
	}
	if found == nil {
		t.Fatalf("no settings.json hooks op: %+v", ops)
	}
	if found.MergeStrategy != "merge-json-keys" {
		t.Fatalf("MergeStrategy = %q, want merge-json-keys", found.MergeStrategy)
	}
	// Verify OwnedKeys contains the event path.
	hasOwned := false
	for _, k := range found.OwnedKeys {
		if k == "/hooks/PreToolUse" {
			hasOwned = true
		}
	}
	if !hasOwned {
		t.Fatalf("OwnedKeys missing /hooks/PreToolUse: %v", found.OwnedKeys)
	}
	// Verify content is valid JSON with hooks structure.
	var got map[string]any
	if err := json.Unmarshal(found.Content, &got); err != nil {
		t.Fatalf("not valid json: %v", err)
	}
	hooks, ok := got["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks key missing or wrong type: %v", got)
	}
	preToolUse, ok := hooks["PreToolUse"].([]any)
	if !ok {
		t.Fatalf("PreToolUse key missing or wrong type: %v", hooks)
	}
	if len(preToolUse) != 2 {
		t.Fatalf("expected 2 PreToolUse entries, got %d", len(preToolUse))
	}
}

func TestRender_Hooks_EmptyProducesNoOp(t *testing.T) {
	c := source.Canonical{}
	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if strings.HasSuffix(op.Path, "settings.json") && strings.Contains(string(op.Content), "\"hooks\"") {
			t.Fatalf("unexpected hooks op when no hooks in canonical: %+v", op)
		}
	}
}

func TestRender_Commands(t *testing.T) {
	c := source.Canonical{
		Commands: []source.Command{{
			Name:        "review",
			Frontmatter: map[string]any{"description": "Run a code review"},
			Body:        "Please review the current changes.\n",
		}},
	}
	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	var found *adapter.FileOp
	for i, op := range ops {
		if strings.HasSuffix(op.Path, "/commands/review.md") {
			found = &ops[i]
		}
	}
	if found == nil {
		t.Fatalf("no commands/review.md op: %+v", ops)
	}
	if !strings.HasPrefix(string(found.Content), "---\n") {
		t.Fatalf("missing frontmatter delimiter: %s", found.Content)
	}
	if found.MergeStrategy != "replace" {
		t.Fatalf("MergeStrategy = %q, want replace", found.MergeStrategy)
	}
}

func TestRender_Subagents(t *testing.T) {
	c := source.Canonical{
		Subagents: []source.Subagent{{
			Name:        "reviewer",
			Frontmatter: map[string]any{"description": "Code reviewer", "model": "claude-opus-4-5"},
			Body:        "You are a code reviewer.\n",
		}},
	}
	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	var found *adapter.FileOp
	for i, op := range ops {
		if strings.HasSuffix(op.Path, "/agents/reviewer.md") {
			found = &ops[i]
		}
	}
	if found == nil {
		t.Fatalf("no agents/reviewer.md op: %+v", ops)
	}
	if !strings.HasPrefix(string(found.Content), "---\n") {
		t.Fatalf("missing frontmatter delimiter: %s", found.Content)
	}
	if found.MergeStrategy != "replace" {
		t.Fatalf("MergeStrategy = %q, want replace", found.MergeStrategy)
	}
}
