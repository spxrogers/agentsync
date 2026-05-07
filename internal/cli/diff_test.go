package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiff_NoDriftIsEmpty(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "diff")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no diff") {
		t.Fatalf("expected 'no diff' after clean apply; got: %s", out)
	}
}

func TestDiff_ShowsDriftedKey(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	// Drift the destination.
	dst := filepath.Join(tmp, ".claude.json")
	body, _ := os.ReadFile(dst)
	_ = os.WriteFile(dst, []byte(strings.ReplaceAll(string(body), `"npx"`, `"npm"`)), 0o644)

	out, err := runCLI(t, env, "diff")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	if strings.Contains(out, "no diff") {
		t.Fatalf("expected diff output but got 'no diff': %s", out)
	}
	// Should show something about the path.
	if !strings.Contains(out, ".claude.json") {
		t.Fatalf("expected .claude.json in diff output; got: %s", out)
	}
}

func TestDiff_PathFilter(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	// Drift destination.
	dst := filepath.Join(tmp, ".claude.json")
	body, _ := os.ReadFile(dst)
	_ = os.WriteFile(dst, []byte(strings.ReplaceAll(string(body), `"npx"`, `"npm"`)), 0o644)

	// Filter to a non-existent path: should report "no diff".
	out, err := runCLI(t, env, "diff", "/nonexistent/path.json")
	if err != nil {
		t.Fatalf("diff with filter: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no diff") {
		t.Fatalf("filtered diff for non-matching path should report 'no diff'; got: %s", out)
	}
}
