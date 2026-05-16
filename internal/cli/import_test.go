package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImport_InvalidSelector verifies that malformed selectors are rejected.
func TestImport_InvalidSelector(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "import", "badformat")
	if err == nil {
		t.Fatal("expected error for malformed selector; got nil")
	}
}

// TestImport_UnknownAgent verifies that an unknown agent returns an error.
func TestImport_UnknownAgent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "import", "alienware:mcp:github")
	if err == nil {
		t.Fatal("expected error for unknown agent; got nil")
	}
}

// TestImport_MCPFromClaude verifies that import claude:mcp:<id> reads the MCP
// server from .claude.json and writes it to the canonical source.
func TestImport_MCPFromClaude(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Write a .claude.json with an MCP server entry (simulating native config).
	claudeJSON := filepath.Join(tmp, ".claude.json")
	if err := os.WriteFile(claudeJSON, []byte(`{
		"mcpServers": {
			"github": {
				"type": "stdio",
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-github"]
			}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:mcp:github")
	if err != nil {
		t.Fatalf("import claude:mcp:github: %v\n%s", err, out)
	}
	if !strings.Contains(out, "mcp/github.toml") {
		t.Fatalf("import output missing confirmation; got: %s", out)
	}

	// Verify the canonical source was written.
	tomlPath := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("canonical mcp/github.toml not written: %v", err)
	}
	if !strings.Contains(string(data), "npx") {
		t.Fatalf("canonical mcp/github.toml missing command; got:\n%s", data)
	}
}

// TestImport_RoundTripDoesNotClobberDest is the regression for the
// HIGH-severity finding: previously, \`import claude:mcp:github\` followed
// by \`apply\` saw the existing .claude.json as ForeignCollision and the
// merge silently overwrote any keys the user had hand-edited between the
// import and the apply.
//
// After the fix, import seeds state with the destination's current hash,
// so the next apply classifies the file as Clean (or Pending if the user
// has edited the canonical since) — never ForeignCollision.
func TestImport_RoundTripDoesNotClobberDest(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// User has a hand-managed .claude.json with one MCP server.
	claudeJSON := filepath.Join(tmp, ".claude.json")
	original := `{
  "mcpServers": {
    "github": {
      "type": "stdio",
      "command": "/usr/local/bin/my-github-fork",
      "args": ["--my-flag"]
    }
  },
  "preserveMe": "do not touch"
}`
	if err := os.WriteFile(claudeJSON, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "import", "claude:mcp:github"); err != nil {
		t.Fatalf("import: %v", err)
	}
	// State must now contain a key entry for /mcpServers/github so that
	// the next apply sees Clean, not ForeignCollision.
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	stData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	if !strings.Contains(string(stData), "/mcpServers/github") {
		t.Fatalf("state missing /mcpServers/github after import; have:\n%s", stData)
	}
	// And the user's foreign top-level key must still be there.
	after, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "preserveMe") {
		t.Fatalf("import clobbered foreign key; .claude.json now:\n%s", after)
	}
}

// TestImport_MCPNotFound verifies that importing a non-existent MCP server errors.
func TestImport_MCPNotFound(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// .claude.json exists but has no MCP servers.
	claudeJSON := filepath.Join(tmp, ".claude.json")
	if err := os.WriteFile(claudeJSON, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "import", "claude:mcp:nonexistent")
	if err == nil {
		t.Fatal("expected error for missing MCP server; got nil")
	}
}

// TestImport_SubagentFromClaude verifies that import claude:agent:<name> reads
// a subagent .md file and writes it into the canonical agents/ directory.
func TestImport_SubagentFromClaude(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Write a subagent file in Claude's native location.
	agentsDir := filepath.Join(tmp, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"),
		[]byte("---\ndescription: \"Code reviewer\"\n---\nReview this code.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:agent:reviewer")
	if err != nil {
		t.Fatalf("import claude:agent:reviewer: %v\n%s", err, out)
	}
	if !strings.Contains(out, "agents/reviewer.md") {
		t.Fatalf("import output missing confirmation; got: %s", out)
	}

	// Verify canonical source was written.
	canonicalPath := filepath.Join(tmp, ".agentsync", "agents", "reviewer.md")
	data, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("canonical agents/reviewer.md not written: %v", err)
	}
	if !strings.Contains(string(data), "reviewer") && !strings.Contains(string(data), "Review") {
		t.Fatalf("canonical agents/reviewer.md missing content; got:\n%s", data)
	}
}

// TestImport_CommandFromClaude verifies import claude:command:<name>.
func TestImport_CommandFromClaude(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Write a command file in Claude's native location.
	commandsDir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "review.md"),
		[]byte("Do a comprehensive code review."), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:command:review")
	if err != nil {
		t.Fatalf("import claude:command:review: %v\n%s", err, out)
	}
	if !strings.Contains(out, "commands/review.md") {
		t.Fatalf("import output missing confirmation; got: %s", out)
	}

	canonicalPath := filepath.Join(tmp, ".agentsync", "commands", "review.md")
	if _, err := os.Stat(canonicalPath); err != nil {
		t.Fatalf("canonical commands/review.md not written: %v", err)
	}
}

// TestImport_UnknownComponent verifies that an unknown component errors.
func TestImport_UnknownComponent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "import", "claude:plugin:foo")
	if err == nil {
		t.Fatal("expected error for unknown component 'plugin'; got nil")
	}
}
