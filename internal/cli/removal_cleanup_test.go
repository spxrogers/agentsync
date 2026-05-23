package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApply_RemovingLastMCPServerCleansDest is the headline regression: when
// the user removes their ONLY MCP server from the source, the next apply must
// delete it from the destination (no merge op renders for an empty section,
// so a synthesized cleanup op carries the removal). Foreign keys are kept.
func TestApply_RemovingLastMCPServerCleansDest(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	if err := os.MkdirAll(filepath.Dir(mcp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply")

	dst := filepath.Join(tmp, ".claude.json")
	if b, _ := os.ReadFile(dst); !strings.Contains(string(b), "github") {
		t.Fatalf("setup: github not applied: %s", b)
	}
	// User adds a foreign top-level key out of band.
	body, _ := os.ReadFile(dst)
	withForeign := strings.Replace(string(body), "{", `{"foo":"bar",`, 1)
	if err := os.WriteFile(dst, []byte(withForeign), 0o644); err != nil {
		t.Fatal(err)
	}

	// Remove the only MCP server from source, then apply.
	if err := os.Remove(mcp); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply")

	out, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if strings.Contains(string(out), "github") {
		t.Fatalf("removed MCP server still live in dest: %s", out)
	}
	if !strings.Contains(string(out), "foo") {
		t.Fatalf("foreign key was not preserved: %s", out)
	}
}

// TestApply_RemovingLastOpenCodeMCPDoesNotClobberJSONC is the critical
// data-loss guard: the cleanup op for opencode.json MUST use the JSONC merge
// strategy. If it used claude's strict-JSON strategy, a opencode.json with
// comments would parse as empty and be clobbered to {} — destroying all
// foreign content.
func TestApply_RemovingLastOpenCodeMCPDoesNotClobberJSONC(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "opencode")

	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	if err := os.MkdirAll(filepath.Dir(mcp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply")

	dst := filepath.Join(tmp, ".config", "opencode", "opencode.json")
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("opencode.json not applied: %v", err)
	}
	// User adds a JSONC comment + a foreign key.
	withForeign := "// my opencode config\n" + strings.Replace(string(body), "{", `{"theme":"dark",`, 1)
	if err := os.WriteFile(dst, []byte(withForeign), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(mcp); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply")

	out, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}
	if strings.Contains(string(out), "github") {
		t.Fatalf("removed MCP server still live: %s", out)
	}
	if !strings.Contains(string(out), "theme") {
		t.Fatalf("DATA LOSS: opencode.json clobbered, foreign key gone: %s", out)
	}
}

// TestApply_OrphanCleanupSkipsMissingDest ensures a cleanup op is NOT
// synthesized (and no empty {} file created) when the orphaned dest no longer
// exists on disk.
func TestApply_OrphanCleanupSkipsMissingDest(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)
	mustRun(t, env, "apply")

	dst := filepath.Join(tmp, ".claude.json")
	if err := os.Remove(dst); err != nil { // user deleted the dest entirely
		t.Fatal(err)
	}
	if err := os.Remove(mcp); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply")

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		b, _ := os.ReadFile(dst)
		t.Fatalf("apply recreated a deleted dest as empty file: %q", b)
	}
}
