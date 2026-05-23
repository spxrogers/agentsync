package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatus_DriftAfterDirectEdit(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}

	// Write an MCP server so apply produces a merge-json-keys op.
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Modify destination directly to introduce drift.
	dst := filepath.Join(tmp, ".claude.json")
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	modified := strings.ReplaceAll(string(body), `"npx"`, `"npm"`)
	if err := os.WriteFile(dst, []byte(modified), 0o644); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}

	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "drift") {
		t.Fatalf("status didn't report drift: %s", out)
	}
}

// TestStatus_DriftOnReplaceFile guards against the regression where status
// only classified merge-json-keys ops (MCP/hooks/lsp) and silently dropped
// every "replace"-strategy file (skills, subagents, commands, memory) — so a
// hand-edited SKILL.md / CLAUDE.md / subagent showed no drift at all.
func TestStatus_DriftOnReplaceFile(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}
	sk := filepath.Join(tmp, ".agentsync", "skills", "greet", "SKILL.md")
	_ = os.MkdirAll(filepath.Dir(sk), 0o755)
	_ = os.WriteFile(sk, []byte("---\nname: greet\ndescription: d\n---\nhi\n"), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	dst := filepath.Join(tmp, ".claude", "skills", "greet", "SKILL.md")
	if err := os.WriteFile(dst, []byte("---\nname: greet\ndescription: d\n---\nHAND EDITED\n"), 0o644); err != nil {
		t.Fatalf("edit dst: %v", err)
	}
	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "drift") {
		t.Fatalf("status did not report drift on a replace-strategy skill file:\n%s", out)
	}
}

func TestStatus_CleanAfterApply(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}

	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	// After clean apply, should report clean or new (state recorded).
	if strings.Contains(out, "drift") || strings.Contains(out, "conflict") {
		t.Fatalf("status reported unexpected drift after clean apply: %s", out)
	}
}
