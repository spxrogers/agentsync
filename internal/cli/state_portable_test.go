package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApply_WritesPortableStateKeys is the end-to-end regression for the
// cross-machine portability bug. PR #20 introduced paths.HomeRelative to
// store state keys as "${HOME}/.claude.json" instead of machine-absolute
// paths so a chezmoi-synced ~/.agentsync/.state/targets.json survives a move
// from /Users/alice to /home/alice. But every production call site passed the
// AGENTSYNC home (~/.agentsync) as the normalization base, and dest files
// like ~/.claude.json live OUTSIDE ~/.agentsync, so HomeRelative returned
// them unchanged (absolute) — the normalization never fired in production and
// the only regression test hand-faked the key. This test drives the REAL
// apply path and asserts the persisted keys are portable.
func TestApply_WritesPortableStateKeys(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	mcpPath := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	mustRun(t, env, "apply")

	stateBytes, err := os.ReadFile(filepath.Join(tmp, ".agentsync", ".state", "targets.json"))
	if err != nil {
		t.Fatalf("read targets.json: %v", err)
	}
	state := string(stateBytes)

	// Keys for dest files under the user home must be ${HOME}-relative.
	if !strings.Contains(state, "${HOME}/.claude.json") {
		t.Fatalf("state keys are not portable (no ${HOME}/.claude.json); got:\n%s", state)
	}
	// And must NOT embed the machine-specific absolute destination path,
	// which is exactly what breaks after a chezmoi cross-home sync.
	absDest := filepath.Join(tmp, ".claude.json")
	if strings.Contains(state, absDest) {
		t.Fatalf("state embeds machine-absolute dest %q; not portable:\n%s", absDest, state)
	}
}
