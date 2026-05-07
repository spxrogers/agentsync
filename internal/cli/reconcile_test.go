package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReconcile_NoDrift(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "reconcile", "--auto-safe"); err != nil {
		t.Fatal(err)
	}
}

func TestReconcile_AutoOverride(t *testing.T) {
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

	// Manually mutate destination to create drift.
	dst := filepath.Join(tmp, ".claude.json")
	body, _ := os.ReadFile(dst)
	drifted := strings.Replace(string(body), `"npx"`, `"npm"`, 1)
	_ = os.WriteFile(dst, []byte(drifted), 0o644)

	// reconcile --auto-override should re-apply source value.
	if _, err := runCLI(t, env, "reconcile", "--auto-override"); err != nil {
		t.Fatal(err)
	}
	final, _ := os.ReadFile(dst)
	if !strings.Contains(string(final), `"npx"`) {
		t.Fatalf("override didn't restore source value: %s", final)
	}
}

func TestReconcile_AutoSafe_NoDriftItems(t *testing.T) {
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

	// No drift: auto-safe should exit 0 and say "nothing to reconcile".
	out, err := runCLI(t, env, "reconcile", "--auto-safe")
	if err != nil {
		t.Fatalf("reconcile --auto-safe: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to reconcile") {
		t.Fatalf("expected 'nothing to reconcile'; got: %s", out)
	}
}

func TestReconcile_InteractiveSkip(t *testing.T) {
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
	_ = os.WriteFile(dst, []byte(strings.Replace(string(body), `"npx"`, `"npm"`, 1)), 0o644)

	// Run with scripted "s\n" input (skip) via runCLIWithStdin.
	out, err := runCLIWithStdin(t, env, "s\n", "reconcile")
	if err != nil {
		t.Fatalf("reconcile interactive skip: %v\n%s", err, out)
	}
	// Destination should be unchanged (still npm).
	final, _ := os.ReadFile(dst)
	if !strings.Contains(string(final), `"npm"`) {
		t.Fatalf("skip should leave dest unchanged; got: %s", final)
	}
}

func TestReconcile_InteractiveQuit(t *testing.T) {
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
	_ = os.WriteFile(dst, []byte(strings.Replace(string(body), `"npx"`, `"npm"`, 1)), 0o644)

	// Quit immediately.
	out, err := runCLIWithStdin(t, env, "q\n", "reconcile")
	if err != nil {
		t.Fatalf("reconcile quit: %v\n%s", err, out)
	}
	if !strings.Contains(out, "quit") {
		t.Fatalf("expected 'quit' in output; got: %s", out)
	}
}
