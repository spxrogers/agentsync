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

// TestDiff_DoesNotLeakResolvedSecrets is the regression test for the bug
// where `agentsync diff` printed the resolved cleartext secret to stdout.
// The dst file was written by a prior apply with the secret substituted,
// so reading it back and printing it via diffmatchpatch leaked the token.
//
// We use a sentinel token string so any leak shows up as a clear failure
// rather than a fuzzy match.
func TestDiff_DoesNotLeakResolvedSecrets(t *testing.T) {
	const sentinel = "ghp_SENTINEL_DO_NOT_LEAK_THIS_TOKEN_123456789"

	tmp := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmp,
		"GITHUB_TOKEN":          sentinel,
	}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	body := `[server]
type = "stdio"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]

[server.env]
GITHUB_TOKEN = "${env:GITHUB_TOKEN}"
`
	_ = os.WriteFile(mcp, []byte(body), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}

	// Sanity check: dest does contain the sentinel (proving the
	// substitution worked and the leak would be possible).
	dst := filepath.Join(tmp, ".claude.json")
	dstBytes, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dstBytes), sentinel) {
		t.Fatalf("setup invariant failed — dest does not contain sentinel; got: %s", dstBytes)
	}

	// Now drift the dest by changing some unrelated key, so diff has
	// something to print.
	driftBody := strings.ReplaceAll(string(dstBytes), `"npx"`, `"npm"`)
	_ = os.WriteFile(dst, []byte(driftBody), 0o644)

	out, err := runCLI(t, env, "diff")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	if strings.Contains(out, sentinel) {
		t.Fatalf("SECURITY: diff output leaked sentinel secret %q\n%s", sentinel, out)
	}
}

// TestDiff_FailsClosedOnUnresolvableSecret is the regression for the residual
// leak: when a ${secret:…} reference cannot be resolved at diff time (age key
// locked/absent, or — as here — no [secrets] backend configured), the cleartext
// value a prior apply wrote into the destination cannot be redacted, so diff
// must refuse rather than risk printing it. It names the offending key so the
// user knows what to unlock.
func TestDiff_FailsClosedOnUnresolvableSecret(t *testing.T) {
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
	// An age-style ${secret:…} ref, but no [secrets] backend is configured, so
	// SelectBackend returns a resolver that cannot resolve github.token.
	body := `[server]
type = "stdio"
command = "npx"

[server.env]
GITHUB_TOKEN = "${secret:github.token}"
`
	_ = os.WriteFile(mcp, []byte(body), 0o644)

	out, err := runCLI(t, env, "diff")
	if err == nil {
		t.Fatalf("diff must fail closed when a secret ref is unresolvable; got nil err, out:\n%s", out)
	}
	if !strings.Contains(err.Error(), "cannot resolve reference") ||
		!strings.Contains(err.Error(), "github.token") {
		t.Fatalf("expected fail-closed message naming the unresolved key; got: %v", err)
	}
}

// TestDiff_FailsClosedOnUnsetEnvSecret is the regression for diff leaking an
// env-backed secret's cleartext: apply substitutes ${env:VAR} into the dest in
// cleartext; if VAR is unset when diff later runs, the redaction map (which
// skips now-unresolvable refs) omitted it and diff printed the cleartext. The
// fail-closed guard must cover ${env:…}, not just ${secret:…}.
func TestDiff_FailsClosedOnUnsetEnvSecret(t *testing.T) {
	tmp := t.TempDir()
	const secret = "ZZZ_SUPER_SECRET_TOKEN_ZZZ"
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "MYTOK": secret}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n[server.env]\nTOKEN=\"${env:MYTOK}\"\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err) // dest now holds the resolved cleartext
	}

	// Run diff with MYTOK UNSET — the dest still holds the cleartext.
	_ = os.Unsetenv("MYTOK")
	out, _ := runCLI(t, map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}, "diff")
	if strings.Contains(out, secret) {
		t.Fatalf("diff leaked the env-backed secret cleartext to stdout:\n%s", out)
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
