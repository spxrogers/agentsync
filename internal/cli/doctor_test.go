package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	agit "github.com/spxrogers/agentsync/internal/git"
)

// TestDoctor_ReportsPerVersionRootGitState drives the checkDestinationGitBackup
// per-version-root loop end-to-end (issue #174): it plants three real on-disk dirs
// at three distinct deep-agent version roots — an agentsync-owned repo, a foreign
// .git, and a plain untracked dir — and asserts doctor reports each root's git
// state. The oracle is doctor's actual output produced from real dirs, not an
// agit.Detect unit call (that has its own coverage).
func TestDoctor_ReportsPerVersionRootGitState(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	claudeRoot := filepath.Join(tmp, ".claude") // agentsync-owned
	rooRoot := filepath.Join(tmp, ".roo")       // foreign source control
	clineRoot := filepath.Join(tmp, ".cline")   // untracked

	// agentsync-owned: a real repo carrying the managed marker.
	if err := os.MkdirAll(claudeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := agit.Init(claudeRoot); err != nil {
		t.Fatalf("agit.Init: %v", err)
	}
	// foreign: a genuine git repo the user owns — no agentsync managed marker.
	if err := os.MkdirAll(rooRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := gogit.PlainInit(rooRoot, false); err != nil {
		t.Fatalf("PlainInit foreign repo: %v", err)
	}
	// untracked: a plain dir with no repo at or above it.
	if err := os.MkdirAll(clineRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Preconditions: confirm the planted states are what we expect, so a doctor
	// output miss is unambiguously a reporting bug, not a bad fixture.
	for _, tc := range []struct {
		dir  string
		want agit.State
	}{
		{claudeRoot, agit.StateAgentsyncOwned},
		{rooRoot, agit.StateForeign},
		{clineRoot, agit.StateUntracked},
	} {
		if st, err := agit.Detect(tc.dir); err != nil || st != tc.want {
			t.Fatalf("precondition: Detect(%s) = (%v, %v), want %v", tc.dir, st, err, tc.want)
		}
	}

	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for _, want := range []struct {
		name, substr string
	}{
		{"owned", claudeRoot + " — agentsync-versioned"},
		{"foreign", rooRoot + " — foreign source control"},
		{"untracked", clineRoot + " — untracked"},
	} {
		t.Run(want.name, func(t *testing.T) {
			if !strings.Contains(out, want.substr) {
				t.Fatalf("doctor output missing %q. Got:\n%s", want.substr, out)
			}
		})
	}
}

func TestDoctor_PrintsEnvAndAgents(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")

	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for _, want := range []string{
		"AGENTSYNC_HOME", "Go version", "OS",
		"home dir   ok", ".state/    ok", "schema     ok",
		"claude", "opencode",
		"Destination git backup", "mode       prompt",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q. Got:\n%s", want, out)
		}
	}
}

// detectionLineSays scans doctor's output for the "Adapter detection" line of
// the named agent and reports whether it carries the expected status. A
// detection line is the one containing both the agent name and "detected"; the
// git-backup section also prints the agent's config path but never "detected",
// so it is skipped. "detected" is a substring of "not detected", so the
// "detected" case additionally requires the negative form to be absent.
func detectionLineSays(out, agent, status string) bool {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, agent) || !strings.Contains(line, "detected") {
			continue
		}
		hasNot := strings.Contains(line, "not detected")
		switch status {
		case "not detected":
			return hasNot
		case "detected":
			return !hasNot
		}
	}
	return false
}

// TestDoctor_ReportsAdapterDetection pins that the "Adapter detection" section
// reflects each adapter's Detect(): an agent whose config dir exists under the
// target root reports "detected", a never-installed agent reports "not
// detected", and — because detection is purely informational — it never makes
// doctor exit non-zero.
func TestDoctor_ReportsAdapterDetection(t *testing.T) {
	tmp := t.TempDir()
	// An empty PATH guarantees no agent binary is ever found, so detection is
	// driven solely by the config-dir stat under the target root — deterministic
	// regardless of what happens to be installed in the test container.
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmp,
		"PATH":                  t.TempDir(),
	}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	// claude's Detect() stats <target>/.claude — create it so claude is detected.
	if err := os.MkdirAll(filepath.Join(tmp, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("detection is informational and must not fail doctor; got err=%v\n%s", err, out)
	}
	// The heading dropped the "(PATH-only)" qualifier now that Detect() also
	// stats the config dir.
	if !strings.Contains(out, "Adapter detection") || strings.Contains(out, "PATH-only") {
		t.Fatalf("want an \"Adapter detection\" heading without \"PATH-only\"; got:\n%s", out)
	}
	if !detectionLineSays(out, "claude", "detected") {
		t.Fatalf("claude should report detected (its ~/.claude config dir exists); got:\n%s", out)
	}
	// windsurf is never installed here: no ~/.codeium/windsurf dir, empty PATH.
	if !detectionLineSays(out, "windsurf", "not detected") {
		t.Fatalf("windsurf should report not detected; got:\n%s", out)
	}
}

// TestDoctor_WarnsNestedRepoUnderUntrackedRoot is the regression for issue #149's D4:
// a StateUntracked destination root that CONTAINS a nested `.git` must produce a warn
// line (apply's initGuarded will refuse to init a repo-inside-a-repo there), not a clean
// "untracked" line — so doctor's report matches what apply actually does.
func TestDoctor_WarnsNestedRepoUnderUntrackedRoot(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	// ~/.claude is untracked (no apply ran), but a foreign repo lives below it.
	nested := filepath.Join(tmp, ".claude", "plugin", ".git")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	out, _ := runCLI(t, env, "doctor")
	if !strings.Contains(out, "nested git repo") {
		t.Fatalf("doctor should warn that untracked ~/.claude contains a nested repo; got:\n%s", out)
	}
}

// TestDoctor_FailsOnMissingHome asserts that doctor exits non-zero when
// the user runs it before `agentsync init`. The old PATH-only doctor
// returned ok no matter what.
func TestDoctor_FailsOnMissingHome(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	out, err := runCLI(t, env, "doctor")
	if err == nil {
		t.Fatalf("doctor should fail on missing home; got:\n%s", out)
	}
	if !strings.Contains(out, "agentsync init") {
		t.Fatalf("doctor should suggest `agentsync init` on missing home; got:\n%s", out)
	}
}

// TestDoctor_FailsOnBadIdentityPerms asserts that a too-permissive age
// identity file fails the readiness check.
// TestDoctor_HonorsSkipPermCheck is the regression for doctor's inline 0o077
// identity-perm check ignoring AGENTSYNC_AGE_SKIP_PERM_CHECK=1, which apply and
// verify both honor (via secrets.CheckIdentityPermissions). doctor would falsely
// fail a 0644 identity even when the user opted out of the perm gate.
func TestDoctor_HonorsSkipPermCheck(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT":         tmp,
		"HOME":                          tmp,
		"AGENTSYNC_AGE_SKIP_PERM_CHECK": "1",
	}
	_, _ = runCLI(t, env, "init")

	identity := filepath.Join(tmp, "age.key")
	if err := os.WriteFile(identity, []byte("# fake key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body := `[agents]
[secrets]
backend       = "age"
recipient     = "age1qqqq"
identity_file = "` + identity + `"
file          = "secrets/secrets.age"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "doctor")
	if err != nil {
		t.Fatalf("doctor should pass with AGENTSYNC_AGE_SKIP_PERM_CHECK=1; got err=%v\n%s", err, out)
	}
	if strings.Contains(out, "too permissive") {
		t.Fatalf("doctor must honor the perm-skip override; got:\n%s", out)
	}
}

func TestDoctor_FailsOnBadIdentityPerms(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp}
	_, _ = runCLI(t, env, "init")

	identity := filepath.Join(tmp, "age.key")
	if err := os.WriteFile(identity, []byte("# fake key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body := `[agents]
[secrets]
backend       = "age"
recipient     = "age1qqqq"
identity_file = "` + identity + `"
file          = "secrets/secrets.age"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "doctor")
	if err == nil {
		t.Fatalf("doctor should fail on 0644 identity; got:\n%s", out)
	}
	if !strings.Contains(out, "too permissive") {
		t.Fatalf("doctor should warn about identity perms; got:\n%s", out)
	}
}
