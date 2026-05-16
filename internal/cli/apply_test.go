package cli_test

import (
	"os"
	"strings"
	"testing"
)

func TestApply_DryRunEmptyHome(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "agent", "add", "claude")

	out, err := runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("apply --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "claude") {
		t.Fatalf("dry-run output missing per-agent breakdown: %s", out)
	}
	if !strings.Contains(out, "0 ops") {
		t.Fatalf("dry-run should report 0 ops on empty canonical: %s", out)
	}
}

// TestApply_FirstRunBacksUpForeignFile is the regression for the
// HIGH-severity finding: the README promised "first apply on a populated
// machine triggers foreign-collision; the original is backed up to
// ~/.agentsync/.state/backups/<ts>/" — the original code never
// implemented backups. This test runs apply against a populated $HOME
// (a hand-edited ~/.claude.json) and asserts the file is backed up
// under <home>/.state/backups/ before the apply rewrites it.
func TestApply_FirstRunBacksUpForeignFile(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Hand-edited .claude.json with a custom MCP server entry the user
	// will not want clobbered without a backup.
	claudeJSON := tmp + "/.claude.json"
	original := `{
  "mcpServers": {"github": {"command": "/usr/local/bin/my-fork", "args": ["--my-flag"]}},
  "preserveMe": "important"
}`
	if err := os.WriteFile(claudeJSON, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	// Canonical mcp/github.toml that conflicts with the existing key.
	mcpDir := tmp + "/.agentsync/mcp"
	_ = os.MkdirAll(mcpDir, 0o755)
	if err := os.WriteFile(mcpDir+"/github.toml",
		[]byte("[server]\ntype = \"stdio\"\ncommand = \"npx\"\nargs = [\"-y\",\"@m/server-github\"]\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// stderr message must mention the backup.
	if !strings.Contains(out, "backed up") {
		t.Fatalf("apply did not advertise backup; got:\n%s", out)
	}

	// Walk <home>/.state/backups looking for a copy of .claude.json with
	// the original "my-fork" command intact.
	backups, err := filepath_walkBackups(tmp + "/.agentsync/.state/backups")
	if err != nil {
		t.Fatalf("walk backups: %v", err)
	}
	if len(backups) == 0 {
		t.Fatal("no backup files written")
	}
	var found bool
	for _, b := range backups {
		data, _ := os.ReadFile(b)
		if strings.Contains(string(data), "my-fork") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no backup contains the original command; backups: %v", backups)
	}
	// And the user's foreign top-level key must still be in the live file
	// (the merge preserves disjoint keys).
	live, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(live), "preserveMe") {
		t.Fatalf("live .claude.json lost foreign key; got:\n%s", live)
	}
}

// filepath_walkBackups returns absolute paths of every regular file under
// root. Returns (nil, nil) if root does not exist so callers can assert
// "no backups written".
func filepath_walkBackups(root string) ([]string, error) {
	var out []string
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	walkErr := walkAll(root, func(p string, isDir bool) {
		if !isDir {
			out = append(out, p)
		}
	})
	return out, walkErr
}

// walkAll is a tiny recursive walker — using filepath.Walk would pull
// path/filepath into the test imports for one call site.
func walkAll(root string, visit func(string, bool)) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := root + "/" + e.Name()
		if e.IsDir() {
			visit(p, true)
			if err := walkAll(p, visit); err != nil {
				return err
			}
		} else {
			visit(p, false)
		}
	}
	return nil
}

// TestApply_DryRunPreviewsForeignCollisions is the regression test for the
// finding that `apply --dry-run` only printed op counts and silently hid
// the foreign-collision events the real apply would generate. The README
// promised dry-run was a safe preview; without collision reporting the
// user could not see which existing files were about to be backed up and
// overwritten until the real apply ran.
func TestApply_DryRunPreviewsForeignCollisions(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Populate ~/.claude.json with a server entry that conflicts.
	claudeJSON := tmp + "/.claude.json"
	original := `{"mcpServers": {"github": {"command": "/my/fork", "args": ["--x"]}}}`
	if err := os.WriteFile(claudeJSON, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	mcpDir := tmp + "/.agentsync/mcp"
	_ = os.MkdirAll(mcpDir, 0o755)
	_ = os.WriteFile(mcpDir+"/github.toml",
		[]byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\nargs=[\"-y\"]\n"),
		0o644)

	out, err := runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("apply --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Foreign collisions") {
		t.Fatalf("dry-run did not preview foreign collisions; got:\n%s", out)
	}
	if !strings.Contains(out, ".claude.json") {
		t.Fatalf("dry-run did not name the affected destination path; got:\n%s", out)
	}
	// And dry-run must not have written anything.
	bytes, _ := os.ReadFile(claudeJSON)
	if string(bytes) != original {
		t.Fatalf("dry-run mutated the live destination; got:\n%s", bytes)
	}
}

// TestApply_DryRunListsDestinations is the regression for the finding that
// dry-run reported "claude N ops" with no indication of which files would
// be touched. Users could not safely run `apply --dry-run` to learn what
// the real run would do without diff-ing every possible destination.
func TestApply_DryRunListsDestinations(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	mcpDir := tmp + "/.agentsync/mcp"
	_ = os.MkdirAll(mcpDir, 0o755)
	_ = os.WriteFile(mcpDir+"/github.toml",
		[]byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"),
		0o644)

	out, err := runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("apply --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, ".claude.json") {
		t.Fatalf("dry-run did not list destination paths; got:\n%s", out)
	}
}

func TestApply_NoAgentsEnabled_WarnsAndExitsZero(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	// Deliberately do NOT call `agent add`. apply must hint at the fix
	// instead of silently printing "applied: 0 ops".
	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply with no agents should succeed; got: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no agents") || !strings.Contains(out, "agent add") {
		t.Fatalf("apply with no agents did not hint at remediation; got: %s", out)
	}
}

func TestApply_NoFlag_WritesDestinations_M1(t *testing.T) {
	// M1+: apply (no flag) should succeed (real adapters are wired).
	// With an empty canonical there are 0 ops, so no files are written, but
	// the command must not error.
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "agent", "add", "claude")

	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("apply in M1 should succeed; got err: %v\n%s", err, out)
	}
}
