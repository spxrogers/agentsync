package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/cli"
)

// exitCodeOf mirrors main()'s error→exit-code mapping: 0 for nil, the ExitCoder
// code for the quiet --exit-code sentinel, else 1 for a generic error.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ec cli.ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

// writeMCP writes a stdio MCP server toml under the canonical source.
func writeMCP(t *testing.T, tmp, id, body string) {
	t.Helper()
	p := filepath.Join(tmp, ".agentsync", "mcp", id+".toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir mcp: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write mcp/%s.toml: %v", id, err)
	}
}

// ---- item 3: status/diff --exit-code -----------------------------------------

func TestStatusExitCode(t *testing.T) {
	setup := func(t *testing.T, drift bool) map[string]string {
		t.Helper()
		tmp := t.TempDir()
		env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
		mustRun(t, env, "init")
		mustRun(t, env, "agent", "add", "claude")
		writeMCP(t, tmp, "github", "[server]\ntype=\"stdio\"\ncommand=\"npx\"\n")
		mustRun(t, env, "apply")
		if drift {
			dst := filepath.Join(tmp, ".claude.json")
			b, _ := os.ReadFile(dst)
			_ = os.WriteFile(dst, []byte(strings.Replace(string(b), `"npx"`, `"npm"`, 1)), 0o644)
		}
		return env
	}
	tests := []struct {
		name     string
		drift    bool
		flag     bool
		wantExit int
	}{
		{"clean with flag exits 0", false, true, 0},
		{"drift with flag exits nonzero", true, true, 2},
		{"drift without flag exits 0", true, false, 0},
		{"clean without flag exits 0", false, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := setup(t, tc.drift)
			args := []string{"status"}
			if tc.flag {
				args = append(args, "--exit-code")
			}
			stdout, _, err := runCLISplit(t, env, args...)
			if got := exitCodeOf(err); got != tc.wantExit {
				t.Fatalf("exit code = %d, want %d (err=%v)\nstdout:\n%s", got, tc.wantExit, err, stdout)
			}
			// The sentinel prints nothing itself: stdout must be the status report,
			// never a stray "agentsync:" error line.
			if strings.Contains(stdout, "agentsync:") {
				t.Fatalf("status --exit-code leaked an error line to stdout:\n%s", stdout)
			}
		})
	}
}

func TestDiffExitCode(t *testing.T) {
	setup := func(t *testing.T, drift bool) map[string]string {
		t.Helper()
		tmp := t.TempDir()
		env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
		mustRun(t, env, "init")
		mustRun(t, env, "agent", "add", "claude")
		writeMCP(t, tmp, "github", "[server]\ntype=\"stdio\"\ncommand=\"npx\"\n")
		mustRun(t, env, "apply")
		if drift {
			dst := filepath.Join(tmp, ".claude.json")
			b, _ := os.ReadFile(dst)
			_ = os.WriteFile(dst, []byte(strings.Replace(string(b), `"npx"`, `"npm"`, 1)), 0o644)
		}
		return env
	}
	tests := []struct {
		name     string
		drift    bool
		flag     bool
		wantExit int
	}{
		{"clean with flag exits 0", false, true, 0},
		{"hunks with flag exits nonzero", true, true, 2},
		{"hunks without flag exits 0", true, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := setup(t, tc.drift)
			args := []string{"diff"}
			if tc.flag {
				args = append(args, "--exit-code")
			}
			stdout, _, err := runCLISplit(t, env, args...)
			if got := exitCodeOf(err); got != tc.wantExit {
				t.Fatalf("exit code = %d, want %d (err=%v)\nstdout:\n%s", got, tc.wantExit, err, stdout)
			}
			if strings.Contains(stdout, "agentsync:") {
				t.Fatalf("diff --exit-code leaked an error line to stdout:\n%s", stdout)
			}
		})
	}
}

// ---- item 6 + 14: diff path filter + --agents parity -------------------------

func TestDiff_UnmanagedPathDistinctFromClean(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	writeMCP(t, tmp, "github", "[server]\ntype=\"stdio\"\ncommand=\"npx\"\n")
	mustRun(t, env, "apply")

	// A managed path that is in sync → "no diff".
	managed := filepath.Join(tmp, ".claude.json")
	out, err := runCLI(t, env, "diff", managed)
	if err != nil {
		t.Fatalf("diff <managed>: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no diff") {
		t.Fatalf("clean managed path should report 'no diff'; got:\n%s", out)
	}

	// A typo'd / unmanaged path → a distinct error, not "no diff".
	unmanaged := filepath.Join(tmp, "not-a-managed-file.json")
	out2, err2 := runCLI(t, env, "diff", unmanaged)
	if err2 == nil {
		t.Fatalf("unmanaged path should error, not report 'no diff'; got:\n%s", out2)
	}
	if !strings.Contains(err2.Error(), "not managed by agentsync") {
		t.Fatalf("expected a 'not managed' message; got: %v", err2)
	}
}

func TestDiff_AgentsFlagParity(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "opencode")
	writeMCP(t, tmp, "github", "[server]\ntype=\"stdio\"\ncommand=\"npx\"\nagents=[\"*\"]\n")
	mustRun(t, env, "apply")

	// Unknown agent: diff and status reject it with the SAME message.
	_, derr := runCLI(t, env, "diff", "--agents", "nope")
	_, serr := runCLI(t, env, "status", "--agents", "nope")
	if derr == nil || serr == nil {
		t.Fatalf("both diff and status must reject an unknown --agents; diff=%v status=%v", derr, serr)
	}
	if derr.Error() != serr.Error() {
		t.Fatalf("diff/status --agents messages differ:\n  diff:   %v\n  status: %v", derr, serr)
	}

	// Empty --agents is rejected.
	if _, e := runCLI(t, env, "diff", "--agents", ""); e == nil {
		t.Fatalf(`diff --agents "" must be rejected`)
	}

	// A valid subset narrows the diff: drift both dests, filter to claude, and the
	// opencode dest must not appear.
	for _, dst := range []string{filepath.Join(tmp, ".claude.json"), filepath.Join(tmp, ".config", "opencode", "opencode.json")} {
		b, _ := os.ReadFile(dst)
		_ = os.WriteFile(dst, []byte(strings.Replace(string(b), `"npx"`, `"npm"`, 1)), 0o644)
	}
	out, err := runCLI(t, env, "diff", "--agents", "claude")
	if err != nil {
		t.Fatalf("diff --agents claude: %v\n%s", err, out)
	}
	if !strings.Contains(out, ".claude.json") {
		t.Fatalf("diff --agents claude should show the claude dest; got:\n%s", out)
	}
	if strings.Contains(out, "opencode.json") {
		t.Fatalf("diff --agents claude must not show the opencode dest; got:\n%s", out)
	}
}

// ---- item 2: apply delete-only summary ---------------------------------------

func TestApplySummary_DeleteOnly(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	skill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply")
	dest := filepath.Join(tmp, ".claude", "skills", "demo", "SKILL.md")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("apply should have written the skill dest: %v", err)
	}

	// Remove the source skill and re-apply → a delete-only run.
	if err := os.RemoveAll(filepath.Join(tmp, ".agentsync", "skills", "demo")); err != nil {
		t.Fatal(err)
	}
	// Dry-run parity: the preview must show the same removal the real apply will
	// report, not "Plan: 0 ops … 0 to write" with the removal invisible.
	dry, err := runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run before delete-only apply: %v\n%s", err, dry)
	}
	if !strings.Contains(dry, "Removals:") || !strings.Contains(dry, "1 file(s)") {
		t.Fatalf("delete-only dry-run should preview the removal counts; got:\n%s", dry)
	}
	out, err := runCLI(t, env, "apply")
	if err != nil {
		t.Fatalf("re-apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "removed: 1 file(s)") {
		t.Fatalf("delete-only apply should report the file removal distinctly in the headline; got:\n%s", out)
	}
	if strings.Contains(out, "applied: 0 ops") {
		t.Fatalf("delete-only apply must not read as 'applied: 0 ops'; got:\n%s", out)
	}
	if strings.Contains(out, "up to date") {
		t.Fatalf("delete-only apply must not read as 'up to date'; got:\n%s", out)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("the orphaned skill dest should have been deleted; stat err=%v", err)
	}
}

// ---- item 4: doctor resolves references --------------------------------------

func TestDoctorSecrets_FlagsUnresolvableRef(t *testing.T) {
	tests := []struct {
		name      string
		mcpEnvVal string
		setEnv    map[string]string
		wantFail  bool
		wantWord  string
	}{
		{"unresolvable secret ref fails", "${secret:NOPE}", nil, true, "NOPE"},
		{"resolvable env ref is green", "${env:PRESENT_VAR}", map[string]string{"PRESENT_VAR": "here"}, false, "references"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
			for k, v := range tc.setEnv {
				env[k] = v
			}
			mustRun(t, env, "init")
			writeMCP(t, tmp, "github", "[server]\ntype=\"stdio\"\ncommand=\"npx\"\n[server.env]\nTOK=\""+tc.mcpEnvVal+"\"\n")
			out, err := runCLI(t, env, "doctor")
			if tc.wantFail {
				if err == nil {
					t.Fatalf("doctor should fail on an unresolvable reference; out:\n%s", out)
				}
				if !strings.Contains(out, tc.wantWord) {
					t.Fatalf("doctor should name the bad reference %q; got:\n%s", tc.wantWord, out)
				}
			} else {
				if err != nil {
					t.Fatalf("doctor with a resolvable reference should pass; err=%v\n%s", err, out)
				}
				if !strings.Contains(out, tc.wantWord) {
					t.Fatalf("doctor should confirm references resolve; got:\n%s", out)
				}
			}
		})
	}
}

// ---- item 5 + 9: stream separation -------------------------------------------

func TestCLIStreams_JSONStdoutMessagesStderr(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	writeMCP(t, tmp, "github", "[server]\ntype=\"stdio\"\ncommand=\"npx\"\n")
	mustRun(t, env, "apply")
	// Disable claude (no purge): it still owns tracked state, so status emits an
	// orphaned-agent WARNING — which must land on stderr, never in the --json
	// payload on stdout.
	mustRun(t, env, "agent", "disable", "claude")

	stdout, stderr, err := runCLISplit(t, env, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v\nstderr:\n%s", err, stderr)
	}
	// stdout must be clean JSON, nothing else.
	var model map[string]any
	if jerr := json.Unmarshal([]byte(stdout), &model); jerr != nil {
		t.Fatalf("stdout is not clean JSON: %v\nstdout:\n%s", jerr, stdout)
	}
	if strings.Contains(stdout, "WARN") {
		t.Fatalf("a warning leaked into the --json stdout payload:\n%s", stdout)
	}
	if !strings.Contains(stderr, "WARN") {
		t.Fatalf("expected the orphaned-agent WARN diagnostic on stderr; got:\n%s", stderr)
	}
}

// ---- item 8: mcp add --header ------------------------------------------------

func TestMCPAdd_HTTPHeaders(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")

	if _, err := runCLI(t, env, "mcp", "add", "remote",
		"--type", "http", "--url", "https://api.example.com/mcp",
		"--header", "Authorization: Bearer ${secret:TOK}",
		"--header", "X-Trace: on"); err != nil {
		t.Fatalf("mcp add --header: %v", err)
	}

	// Read the written artifact from disk (spec-complete assertion).
	data, err := os.ReadFile(filepath.Join(tmp, ".agentsync", "mcp", "remote.toml"))
	if err != nil {
		t.Fatalf("read mcp/remote.toml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "headers") {
		t.Fatalf("mcp/remote.toml should carry a headers table; got:\n%s", got)
	}
	// The secret reference is preserved literally, NOT resolved at author time.
	if !strings.Contains(got, "${secret:TOK}") {
		t.Fatalf("the ${secret:TOK} reference should be preserved verbatim; got:\n%s", got)
	}
	if !strings.Contains(got, "X-Trace") {
		t.Fatalf("the second --header should be present; got:\n%s", got)
	}

	// --header with --type stdio is rejected.
	_, serr := runCLI(t, env, "mcp", "add", "local",
		"--type", "stdio", "--command", "npx", "--header", "X: Y")
	if serr == nil {
		t.Fatalf("--header with --type stdio must be rejected")
	}
	if !strings.Contains(serr.Error(), "http/sse") {
		t.Fatalf("stdio+header error should mention http/sse; got: %v", serr)
	}
}

// ---- item 7: reconcile persists dest-removed MCP deletion --------------------

func TestReconcile_PersistsDestRemovedMCPDeletion(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmp,
		"GH_TOKEN":              "ghp_LEAKME_DO_NOT_PERSIST",
	}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	// backend=env so ${secret:GH_TOKEN} resolves from the env var.
	tomlPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	cfg, _ := os.ReadFile(tomlPath)
	_ = os.WriteFile(tomlPath, append(cfg, []byte("\n[secrets]\nbackend = \"env\"\n")...), 0o644)
	writeMCP(t, tmp, "github", "[server]\ntype = \"stdio\"\ncommand = \"npx\"\n[server.env]\nGH_TOKEN = \"${secret:GH_TOKEN}\"\n")
	mustRun(t, env, "apply")

	mcpSrc := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	if _, err := os.Stat(mcpSrc); err != nil {
		t.Fatalf("precondition: source mcp/github.toml should exist: %v", err)
	}

	// Delete the server from the destination (the user's hand edit), then choose
	// [w]rite-back to persist that deletion.
	dst := filepath.Join(tmp, ".claude.json")
	if err := os.WriteFile(dst, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLIWithStdin(t, env, "w\n", "reconcile")
	if err != nil {
		t.Fatalf("reconcile write-back of a dest-removed server: %v\n%s", err, out)
	}

	// The canonical source file is gone.
	if _, err := os.Stat(mcpSrc); !os.IsNotExist(err) {
		t.Fatalf("source mcp/github.toml should be removed; stat err=%v\n%s", err, out)
	}
	// No cleartext secret leaked into the source tree anywhere.
	assertNoCleartextUnder(t, filepath.Join(tmp, ".agentsync"), "ghp_LEAKME_DO_NOT_PERSIST")
}

func assertNoCleartextUnder(t *testing.T, root, secret string) {
	t.Helper()
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, e error) error {
		if e != nil || fi.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr == nil && strings.Contains(string(b), secret) {
			t.Fatalf("cleartext secret leaked into %s", p)
		}
		return nil
	})
}

// ---- item 1: reconcile prompt shows values, masks secrets --------------------

func TestReconcilePrompt_ShowsValuesNotHashes(t *testing.T) {
	t.Run("shows differing values not sha prefixes", func(t *testing.T) {
		tmp := t.TempDir()
		env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
		mustRun(t, env, "init")
		mustRun(t, env, "agent", "add", "claude")
		writeMCP(t, tmp, "github", "[server]\ntype=\"stdio\"\ncommand=\"npx\"\n")
		mustRun(t, env, "apply")
		dst := filepath.Join(tmp, ".claude.json")
		b, _ := os.ReadFile(dst)
		_ = os.WriteFile(dst, []byte(strings.Replace(string(b), `"npx"`, `"npm"`, 1)), 0o644)

		out, err := runCLIWithStdin(t, env, "q\n", "reconcile")
		if err != nil {
			t.Fatalf("reconcile: %v\n%s", err, out)
		}
		// A real value diff, not the old "source: <sha>" hash display: the changed
		// field's actual content is visible (a hash display never shows "command"),
		// with inline {+…+}/[-…-] change markers (colorless mode).
		if !strings.Contains(out, "--- source") {
			t.Fatalf("prompt should show a value diff header; got:\n%s", out)
		}
		if strings.Contains(out, "source:      ") {
			t.Fatalf("prompt still shows the SHA-prefix fallback:\n%s", out)
		}
		if !strings.Contains(out, "command") {
			t.Fatalf("prompt should surface the differing field's value, not a hash; got:\n%s", out)
		}
		if !strings.Contains(out, "{+") && !strings.Contains(out, "[-") {
			t.Fatalf("prompt should show inline diff markers for the change; got:\n%s", out)
		}
	})

	t.Run("masks a resolved secret", func(t *testing.T) {
		tmp := t.TempDir()
		env := map[string]string{
			"AGENTSYNC_TARGET_ROOT": tmp,
			"GH_TOKEN":              "ghp_SUPERSECRET_DONT_SHOW",
		}
		mustRun(t, env, "init")
		mustRun(t, env, "agent", "add", "claude")
		tomlPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
		cfg, _ := os.ReadFile(tomlPath)
		_ = os.WriteFile(tomlPath, append(cfg, []byte("\n[secrets]\nbackend = \"env\"\n")...), 0o644)
		writeMCP(t, tmp, "github", "[server]\ntype = \"stdio\"\ncommand = \"npx\"\n[server.env]\nGH_TOKEN = \"${secret:GH_TOKEN}\"\n")
		mustRun(t, env, "apply")
		// Drift the command so the item classifies as drift and the prompt renders.
		dst := filepath.Join(tmp, ".claude.json")
		b, _ := os.ReadFile(dst)
		_ = os.WriteFile(dst, []byte(strings.Replace(string(b), `"npx"`, `"npm"`, 1)), 0o644)

		out, err := runCLIWithStdin(t, env, "q\n", "reconcile")
		if err != nil {
			t.Fatalf("reconcile: %v\n%s", err, out)
		}
		if strings.Contains(out, "ghp_SUPERSECRET_DONT_SHOW") {
			t.Fatalf("reconcile prompt LEAKED the resolved secret in cleartext:\n%s", out)
		}
		if !strings.Contains(out, "${secret:GH_TOKEN}") {
			t.Fatalf("reconcile prompt should show the masked ${secret:GH_TOKEN} placeholder; got:\n%s", out)
		}
	})
}

// ---- item 13/15: bulk-confirm count = true blast radius ----------------------

func TestReconcileBulkConfirm_CountIsBlastRadius(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	for _, id := range []string{"aaa", "bbb", "ccc"} {
		writeMCP(t, tmp, id, "[server]\ntype=\"stdio\"\ncommand=\"npx\"\n")
	}
	mustRun(t, env, "apply")
	// Drift all three servers.
	dst := filepath.Join(tmp, ".claude.json")
	b, _ := os.ReadFile(dst)
	_ = os.WriteFile(dst, []byte(strings.ReplaceAll(string(b), `"npx"`, `"npm"`)), 0o644)

	// Skip the first item, then press capital-W on the second. The confirm count
	// must be the TRUE blast radius from here forward (2 items), not all 3.
	out, err := runCLIWithStdin(t, env, "sWnq", "reconcile")
	if err != nil {
		t.Fatalf("reconcile: %v\n%s", err, out)
	}
	if !strings.Contains(out, "to all 2 remaining items") {
		t.Fatalf("bulk-confirm count should equal the blast radius (2 remaining), not 3; got:\n%s", out)
	}
}

// ---- item 11: import summary enumerates bundled skill files ------------------

func TestImportSummary_EnumeratesBundledFiles(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	// A spec-complete on-disk skill in the NATIVE (claude) location: SKILL.md plus
	// bundled scripts/ and references/ files.
	skillDir := filepath.Join(tmp, ".claude", "skills", "demo")
	for rel, content := range map[string]string{
		"SKILL.md":            "---\nname: demo\ndescription: d\n---\nbody\n",
		"scripts/run.sh":      "#!/bin/sh\necho hi\n",
		"references/notes.md": "some notes\n",
	} {
		p := filepath.Join(skillDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runCLI(t, env, "import", "claude:skill")
	if err != nil {
		t.Fatalf("import claude:skill: %v\n%s", err, out)
	}
	for _, want := range []string{"scripts/run.sh", "references/notes.md"} {
		if !strings.Contains(out, want) {
			t.Fatalf("import summary should enumerate bundled file %q; got:\n%s", want, out)
		}
	}
}

// TestApplyDryRun_CleanupOpNotCountedToWrite pins the dry-run/real headline
// agreement for KEY removals: an emptied MCP section renders as the synthesized
// "{}"+OwnedKeys cleanup op, which the real apply reports under "removed: N
// key(s)" and subtracts from "applied: X ops" — the dry-run must not count the
// same op in "N to write" (it is previewed under "Removals:" instead).
func TestApplyDryRun_CleanupOpNotCountedToWrite(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "srv.toml")
	if err := os.MkdirAll(filepath.Dir(mcp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcp, []byte("[server]\ntype = \"stdio\"\ncommand = \"npx\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply")

	// Remove the only MCP server → the next apply's only work is the "{}"
	// cleanup op deleting the owned /mcpServers/srv pointer.
	if err := os.Remove(mcp); err != nil {
		t.Fatal(err)
	}
	dry, err := runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, dry)
	}
	if !strings.Contains(dry, "Removals:") || !strings.Contains(dry, "key(s)") {
		t.Fatalf("dry-run should preview the key removal; got:\n%s", dry)
	}
	if !strings.Contains(dry, "0 to write") {
		t.Fatalf("the cleanup op must not be double-counted in 'to write' (it is a removal); got:\n%s", dry)
	}
	// The headline's removal partition and the per-op label are their own
	// behaviors (both were revertible with the suite green): the Plan line must
	// carry the op-count partition so it sums, and the op listing must label
	// the cleanup op "remove" — never "write", and never "synced" even when the
	// dest already converged (the label arm is checked before isSyncedOp).
	if !strings.Contains(dry, "1 removal op(s)") {
		t.Fatalf("Plan headline should carry the removal-op partition; got:\n%s", dry)
	}
	if !strings.Contains(dry, "remove") {
		t.Fatalf("the cleanup op should be listed with the remove label; got:\n%s", dry)
	}
}
