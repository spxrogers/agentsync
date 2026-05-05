package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyThenReconcileAutoSafe exercises the full drift loop end-to-end:
// init → agent add → apply → reconcile --auto-safe (no drift → no-op).
func TestApplyThenReconcileAutoSafe(t *testing.T) {
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
	_ = os.WriteFile(mcp, []byte(`[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
`), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	// No drift: auto-safe must succeed and report nothing to reconcile.
	out, err := runCLI(t, env, "reconcile", "--auto-safe")
	if err != nil {
		t.Fatalf("reconcile --auto-safe: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to reconcile") {
		t.Fatalf("expected 'nothing to reconcile'; got: %s", out)
	}
}

// TestDriftLoop_FullRoundTrip exercises the full M3 drift loop:
//   init → agent add → apply → mutate dest → status (drift) →
//   reconcile --auto-override → verify dest restored.
func TestDriftLoop_FullRoundTrip(t *testing.T) {
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
	_ = os.WriteFile(mcp, []byte(`[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
`), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	// Mutate destination to create drift.
	dst := filepath.Join(tmp, ".claude.json")
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	drifted := strings.Replace(string(body), `"npx"`, `"echo"`, 1)
	if err := os.WriteFile(dst, []byte(drifted), 0o644); err != nil {
		t.Fatalf("write drifted: %v", err)
	}

	// status should report drift.
	statusOut, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, statusOut)
	}
	if !strings.Contains(statusOut, "drift") {
		t.Fatalf("expected 'drift' in status output; got: %s", statusOut)
	}

	// reconcile --auto-override should restore destination.
	if _, err := runCLI(t, env, "reconcile", "--auto-override"); err != nil {
		t.Fatalf("reconcile --auto-override: %v", err)
	}

	final, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if !strings.Contains(string(final), `"npx"`) {
		t.Fatalf("auto-override did not restore source value; got: %s", final)
	}
	if strings.Contains(string(final), `"echo"`) {
		t.Fatalf("drifted value still present after override; got: %s", final)
	}
}

// TestDriftLoop_WriteBack exercises write-back:
//   apply → mutate dest → reconcile --auto-writeback → verify source updated.
func TestDriftLoop_WriteBack(t *testing.T) {
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
	_ = os.WriteFile(mcp, []byte(`[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
`), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	// Mutate destination.
	dst := filepath.Join(tmp, ".claude.json")
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	updated := strings.Replace(string(body), `"npx"`, `"yarn"`, 1)
	if err := os.WriteFile(dst, []byte(updated), 0o644); err != nil {
		t.Fatalf("write updated: %v", err)
	}

	// reconcile --auto-writeback should update the source file.
	out, err := runCLI(t, env, "reconcile", "--auto-writeback")
	if err != nil {
		t.Fatalf("reconcile --auto-writeback: %v\n%s", err, out)
	}

	// The source MCP file should now contain "yarn" (written back from dest).
	srcData, err := os.ReadFile(mcp)
	if err != nil {
		t.Fatalf("read mcp source: %v", err)
	}
	if !strings.Contains(string(srcData), "yarn") {
		t.Fatalf("source was not updated by write-back; got:\n%s", srcData)
	}
}
