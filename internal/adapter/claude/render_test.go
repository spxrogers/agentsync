package claude_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
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
	ops, skips, err := a.Render(c, adapter.ScopeUser, "")
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
	ops, _, err := a.Render(c, adapter.ScopeUser, "")
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
	ops, _, err := a.Render(c, adapter.ScopeUser, "")
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
	ops, _, _ := a.Render(c, adapter.ScopeUser, "")
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
