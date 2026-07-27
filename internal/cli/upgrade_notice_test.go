package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/cli"
)

// The acceptance suite for the first-run-after-upgrade notice. agentsync ships
// through channels with no usable post-install hook (`go install` has none at
// all), so the binary is the only thing that reliably reaches an upgrading
// user — which makes the "exactly once, never in the way" contract below the
// whole feature.

// withVersion stamps a release version for the duration of one test. The
// package default is "dev", which opts the whole mechanism out.
func withVersion(t *testing.T, v string) {
	t.Helper()
	prev := cli.Version
	cli.Version = v
	t.Cleanup(func() { cli.Version = prev })
}

// readLastRun returns the parsed .state/last-run.json, or nil when absent.
func readLastRun(t *testing.T, tmp string) *struct {
	Version     string   `json:"version"`
	NoticesSeen []string `json:"notices_seen"`
} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tmp, ".agentsync", ".state", "last-run.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read last-run.json: %v", err)
	}
	var rec struct {
		Version     string   `json:"version"`
		NoticesSeen []string `json:"notices_seen"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("parse last-run.json: %v\n%s", err, data)
	}
	return &rec
}

// TestUpgradeNotice_ShownOnceOnUpgrade is the core case: an EXISTING home with
// no run record is a user upgrading from a version that predates the record, so
// they see the notice — once.
func TestUpgradeNotice_ShownOnceOnUpgrade(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init") // the home now exists — this is an upgrade, not a first install

	withVersion(t, "0.11.0")

	stdout, stderr, err := runCLISplit(t, env, "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("upgrade notice not shown on stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "agentsync.cc/reference/upgrading/") {
		t.Errorf("notice does not link to the docs page:\n%s", stderr)
	}
	// The notice must never land on stdout — several commands emit a
	// machine-readable payload there.
	if strings.Contains(stdout, "migrate subagents") {
		t.Fatalf("upgrade notice leaked onto stdout:\n%s", stdout)
	}

	rec := readLastRun(t, tmp)
	if rec == nil {
		t.Fatal("last-run.json was not recorded")
	}
	// Pin the literal ID, not just "some id". The ID is the real key — a rename
	// re-shows the notice to everyone who already dismissed it — so a test that
	// only counts entries would not notice the one edit that matters.
	if rec.Version != "0.11.0" {
		t.Errorf("recorded version = %q, want 0.11.0", rec.Version)
	}
	if len(rec.NoticesSeen) != 1 || rec.NoticesSeen[0] != "0.11.0-cli-surface" {
		t.Fatalf("recorded notice ids = %v, want exactly [0.11.0-cli-surface]", rec.NoticesSeen)
	}

	// Second run: silent.
	_, stderr2, err := runCLISplit(t, env, "version")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr2, "migrate subagents") {
		t.Fatalf("upgrade notice repeated on a second run:\n%s", stderr2)
	}
}

// TestUpgradeNotice_SilentOnFreshInstall pins the distinction that matters: a
// user with no config at all has had nothing broken under them, so greeting
// them with a changelog would be noise.
func TestUpgradeNotice_SilentOnFreshInstall(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	withVersion(t, "0.11.0")

	// No `init` — the home does not exist.
	_, stderr, err := runCLISplit(t, env, "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("a fresh install must not see the upgrade notice:\n%s", stderr)
	}

	// It must write NOTHING. Creating .state/ here would materialize the home
	// and make the user's first `agentsync init` refuse ("already contains
	// files") — so the contract is "return without writing", and `init` seeds
	// the record itself.
	//
	// The previous assertion here (`rec != nil && len(rec.NoticesSeen) == 0`)
	// stated the opposite in its comment AND passed vacuously: on the correct
	// behavior rec is nil, so the guard short-circuits and asserts nothing, and
	// on the regression (a full record written) it also passes. Pin rec == nil.
	if rec := readLastRun(t, tmp); rec != nil {
		t.Errorf("a fresh install must write no run record at all (it would materialize the home "+
			"and break `init`); got: %+v", rec)
	}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	_, stderr, err = runCLISplit(t, env, "version")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("notice fired after a fresh install created the home:\n%s", stderr)
	}
}

// TestUpgradeNotice_DevBuildIsSilent pins the opt-out that keeps every test,
// BDD scenario, and local `go build` free of the banner.
func TestUpgradeNotice_DevBuildIsSilent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	withVersion(t, "dev")

	_, stderr, err := runCLISplit(t, env, "version")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("a dev build must not show the upgrade notice:\n%s", stderr)
	}
	if rec := readLastRun(t, tmp); rec != nil {
		t.Errorf("a dev build must not write a run record: %+v", rec)
	}
}

// TestUpgradeNotice_EnvOptOut silences the notice AND declines to record it, so
// unsetting the variable later still surfaces an unseen notice rather than
// swallowing it permanently.
func TestUpgradeNotice_EnvOptOut(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	withVersion(t, "0.11.0")

	optOut := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "AGENTSYNC_NO_UPGRADE_NOTICE": "1"}
	_, stderr, err := runCLISplit(t, optOut, "version")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("AGENTSYNC_NO_UPGRADE_NOTICE=1 did not silence the notice:\n%s", stderr)
	}
	if rec := readLastRun(t, tmp); rec != nil {
		t.Errorf("opting out must not record the notices as seen: %+v", rec)
	}

	// Unsetting it surfaces the still-unseen notice. (t.Setenv persists for the
	// rest of the test, so the opt-out must be cleared explicitly — omitting the
	// key would leave the previous run's value in place.)
	cleared := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "AGENTSYNC_NO_UPGRADE_NOTICE": ""}
	_, stderr, err = runCLISplit(t, cleared, "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("notice was swallowed permanently by a one-off opt-out:\n%s", stderr)
	}
}

// TestUpgradeNotice_JSONPayloadStaysClean is the reason the banner goes to
// stderr: `status --json` is piped into other tools.
func TestUpgradeNotice_JSONPayloadStaysClean(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	withVersion(t, "0.11.0")

	// Every --json surface, not just one: the banner fires on the FIRST run, so
	// whichever machine-readable command a user happens to run first is the one
	// that would get a corrupted payload.
	for _, args := range [][]string{
		{"status", "--json"},
		{"diff", "--json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			// Reset the record so the notice fires on THIS run.
			_ = os.Remove(filepath.Join(tmp, ".agentsync", ".state", "last-run.json"))

			stdout, stderr, err := runCLISplit(t, env, args...)
			if err != nil {
				t.Fatalf("%v: %v\n%s", args, err, stderr)
			}
			if !strings.Contains(stderr, "migrate subagents") {
				t.Fatalf("notice should have fired on this run:\n%s", stderr)
			}
			var payload any
			if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
				t.Fatalf("the upgrade notice corrupted the --json payload: %v\n%s", err, stdout)
			}
		})
	}
}

// TestUpgradeNotice_BrandNewProjectScopeUserIsNotToldToMigrate is the
// regression for a misfire that told a 0.11.0-native user to run `agentsync
// migrate subagents` against a tree that never had an agents/ directory.
//
// A project-scope user never runs `agentsync init` at user scope — but apply
// records state CENTRALLY under ~/.agentsync/.state/ keyed by project root, so
// the first project-scope command that touches state materializes a user home
// that `init` never seeded. "Home exists + no record" then reads as an upgrade.
//
// Note the shape of this test: it runs the real sequence and asserts SILENCE
// throughout. The previous version deleted the record and asserted the notice
// SHOWED — which is the same observable as the bug, so it could never have
// caught it.
func TestUpgradeNotice_BrandNewProjectScopeUserIsNotToldToMigrate(t *testing.T) {
	tmp := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	withVersion(t, "0.11.0")

	// Never any user-scope `init` — this user only ever works in a project.
	for _, args := range [][]string{
		{"init", "--project", proj},
		{"agent", "add", "claude", "--project", proj}, // materializes ~/.agentsync for central state
		{"apply", "--project", proj},
		{"status", "--project", proj},
	} {
		_, stderr, err := runCLISplit(t, env, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stderr)
		}
		if strings.Contains(stderr, "migrate subagents") {
			t.Fatalf("`%s` showed the upgrade banner to a brand-new project-scope user — "+
				"their tree never had an agents/ directory:\n%s", strings.Join(args, " "), stderr)
		}
	}
}

// TestUpgradeNotice_UserConfigStillGetsTheNotice is the negative control for the
// fix above: narrowing the trigger to "the home holds an agentsync.toml" must
// not silence the genuine upgrader it exists for.
func TestUpgradeNotice_UserConfigStillGetsTheNotice(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	withVersion(t, "0.11.0")

	// A home created by a version that predates the run record.
	if err := os.Remove(filepath.Join(tmp, ".agentsync", ".state", "last-run.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	_, stderr, err := runCLISplit(t, env, "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("a real upgrader with a user config must still see the notice:\n%s", stderr)
	}
}

// TestUpgradeNotice_WriteFailureStillPrints covers the last uncovered branch:
// the notice printed, then recording it failed. It must not fail the command,
// and the notice must show again next run rather than being lost.
//
// A .state symlinked to a nonexistent target is the uid-independent way in (the
// container runs as root, where chmod would not bite): ReadFile returns ENOENT
// so the load succeeds, then MkdirAll fails on the dangling link.
func TestUpgradeNotice_WriteFailureStillPrints(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	withVersion(t, "0.11.0")

	stateDir := filepath.Join(tmp, ".agentsync", ".state")
	if err := os.RemoveAll(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "nonexistent-target"), stateDir); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := runCLISplit(t, env, "version")
	if err != nil {
		t.Fatalf("a failed record write must not fail the command: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("the notice must still print when only the RECORD write fails:\n%s", stderr)
	}
	// Unrecorded means it shows again — the harmless direction to fail in.
	_, stderr2, err := runCLISplit(t, env, "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr2, "migrate subagents") {
		t.Errorf("an unrecordable notice must repeat, not vanish:\n%s", stderr2)
	}
}

// TestNoSubcommandOverridesPersistentPreRun is the structural guard the notice
// depends on: cobra runs only the CLOSEST PersistentPreRunE in the chain, so a
// subcommand growing its own would silently disable the banner for that command
// — with nothing failing. Fail loudly here instead.
func TestNoSubcommandOverridesPersistentPreRun(t *testing.T) {
	root := cli.NewRoot()
	if root.PersistentPreRunE == nil {
		t.Fatal("root has no PersistentPreRunE; the upgrade notice is not wired")
	}
	var offenders []string
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		for _, sub := range c.Commands() {
			name := strings.TrimSpace(path + " " + sub.Name())
			if sub.PersistentPreRunE != nil || sub.PersistentPreRun != nil {
				offenders = append(offenders, name)
			}
			walk(sub, name)
		}
	}
	walk(root, "")
	if len(offenders) > 0 {
		t.Fatalf("these subcommands define their own PersistentPreRun(E), which SHADOWS the root's "+
			"and silently disables the upgrade notice for them: %s\n"+
			"Call maybePrintUpgradeNotice from the subcommand hook, or move the logic into RunE.",
			strings.Join(offenders, ", "))
	}
}

// TestUpgradeNotice_RecordIsGitignored pins the docs' flat claim that the run
// record "never travels with a dotfiles repo".
//
// The notice path is a SECOND place that can create `.state/` — `init` is the
// other — and it fires precisely for homes that already existed, which is to
// say homes created by a version that may predate the .gitignore scaffolding.
// Without the rule, one `git add -A` in a dotfiles repo commits one machine's
// run marker and carries it to every other machine, where it silently
// suppresses the notice.
func TestUpgradeNotice_RecordIsGitignored(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	withVersion(t, "0.11.0")

	home := filepath.Join(tmp, ".agentsync")
	// Simulate the target population: a pre-existing dotfiles home whose
	// .gitignore predates the .state rule (or never had one).
	if err := os.Remove(filepath.Join(home, ".gitignore")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".state")); err != nil {
		t.Fatal(err)
	}

	if _, stderr, err := runCLISplit(t, env, "version"); err != nil {
		t.Fatalf("version: %v\n%s", err, stderr)
	}
	if readLastRun(t, tmp) == nil {
		t.Fatal("no record was written, so this test proves nothing about ignoring it")
	}

	data, err := os.ReadFile(filepath.Join(home, ".gitignore"))
	if err != nil {
		t.Fatalf("the notice created .state/ without restoring the .gitignore rule: %v", err)
	}
	var ignored bool
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "/.state/" {
			ignored = true
		}
	}
	if !ignored {
		t.Fatalf("`.state/` is not gitignored, so the run record would be committed:\n%s", data)
	}
}

// TestUpgradeNotice_CorruptRecordStillShowsAndRepairs is the case that made the
// difference between "the notice shows again later" and "the user never learns
// their config layout moved".
//
// A truncated / empty / wrong-shaped last-run.json is the classic crash- or
// full-disk artifact. Treating that like an I/O failure suppressed the notice
// PERMANENTLY — the file was never rewritten, so every subsequent run hit the
// same parse error. A record that cannot be parsed carries no information, so
// the honest reading is "nothing has been shown here": print, then overwrite.
func TestUpgradeNotice_CorruptRecordStillShowsAndRepairs(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty file (crash mid-write)", ""},
		{"truncated json", `{"version":"0.10.1","notices_`},
		{"wrong shape: array", `[]`},
		{"wrong shape: string", `"nope"`},
		{"wrong field types", `{"version":42,"notices_seen":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
			mustRun(t, env, "init")
			withVersion(t, "0.11.0")

			rec := filepath.Join(tmp, ".agentsync", ".state", "last-run.json")
			if err := os.WriteFile(rec, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}

			_, stderr, err := runCLISplit(t, env, "version")
			if err != nil {
				t.Fatalf("version: %v\n%s", err, stderr)
			}
			if !strings.Contains(stderr, "migrate subagents") {
				t.Fatalf("a corrupt record must not suppress the notice:\n%s", stderr)
			}
			// And it must be REPAIRED, or the notice repeats forever instead.
			got := readLastRun(t, tmp)
			if got == nil || len(got.NoticesSeen) == 0 {
				t.Fatalf("corrupt record was not repaired: %+v", got)
			}
			_, stderr2, err := runCLISplit(t, env, "version")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stderr2, "migrate subagents") {
				t.Errorf("notice repeated after the record was repaired:\n%s", stderr2)
			}
		})
	}
}

// TestUpgradeNotice_KeyedByIDNotVersion pins the central design decision, which
// nothing exercised: the show/hide choice is made by notice ID, never by
// comparing versions.
//
// It matters in both directions. A user who jumps 0.9 → 0.12 must still see a
// notice introduced in 0.11 (version comparison would be tempting and wrong),
// and a machine that has already seen an ID must stay silent even when the
// recorded version looks old.
func TestUpgradeNotice_KeyedByIDNotVersion(t *testing.T) {
	t.Run("unseen id shows even though the recorded version is current", func(t *testing.T) {
		tmp := t.TempDir()
		env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
		mustRun(t, env, "init")
		withVersion(t, "0.11.0")
		writeLastRun(t, tmp, `{"version":"0.11.0","notices_seen":["some-other-id"]}`)

		_, stderr, err := runCLISplit(t, env, "version")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stderr, "migrate subagents") {
			t.Fatalf("an UNSEEN notice id must show regardless of the recorded version:\n%s", stderr)
		}
	})

	t.Run("seen id stays silent even though the recorded version is ancient", func(t *testing.T) {
		tmp := t.TempDir()
		env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
		mustRun(t, env, "init")
		withVersion(t, "0.11.0")
		writeLastRun(t, tmp, `{"version":"0.9.0","notices_seen":["0.11.0-cli-surface"]}`)

		_, stderr, err := runCLISplit(t, env, "version")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stderr, "migrate subagents") {
			t.Fatalf("a SEEN notice id must stay silent regardless of the recorded version:\n%s", stderr)
		}
	})
}

// writeLastRun plants a run record verbatim.
func writeLastRun(t *testing.T, tmp, body string) {
	t.Helper()
	p := filepath.Join(tmp, ".agentsync", ".state", "last-run.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeNotice_StateOnlyHomeIsNotSilencedForever pins that skipping the
// notice for a state-only home does NOT record it as seen.
//
// The first version of that check seeded the record before returning. Since the
// check runs before the record is ever read, seeding bought nothing — and it
// permanently silenced anyone whose home gained an agentsync.toml afterwards
// (a dotfiles restore landing after the first run, or simply running `init`
// tomorrow). They would never hear about the breaking changes at all.
func TestUpgradeNotice_StateOnlyHomeIsNotSilencedForever(t *testing.T) {
	tmp := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	withVersion(t, "0.11.0")

	// Project-scope use materializes ~/.agentsync for central state, with no
	// user-scope agentsync.toml. The notice must stay quiet…
	mustRun(t, env, "init", "--project", proj)
	mustRun(t, env, "agent", "add", "claude", "--project", proj)
	_, stderr, err := runCLISplit(t, env, "apply", "--project", proj)
	if err != nil {
		t.Fatalf("apply --project: %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("state-only home must not see the banner:\n%s", stderr)
	}
	// …and must not have recorded the notices as seen.
	if rec := readLastRun(t, tmp); rec != nil && len(rec.NoticesSeen) > 0 {
		t.Fatalf("a state-only home recorded notices as SEEN, so a user who later gains a "+
			"user config is silenced forever: %+v", rec)
	}

	// Now the user config arrives — a dotfiles restore, or `init` tomorrow. They
	// have a pre-0.11 config now, so they must hear about the changes.
	if err := os.WriteFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"),
		[]byte("[agents]\nclaude = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, err = runCLISplit(t, env, "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("a home that gained a user config was permanently silenced:\n%s", stderr)
	}
}

// TestUpgradeNotice_ShellCompletionDoesNotConsumeIt is the regression for the
// way this feature could have failed for almost everyone who has completions
// installed.
//
// Cobra runs the root PersistentPreRunE for its hidden `__complete` request
// too, and the generated bash/zsh scripts invoke it as
// `out=$(eval "${requestComp}" 2>/dev/null)` — stderr DISCARDED. So one TAB
// after `agentsync ` printed the banner into /dev/null and recorded it as seen,
// and the user's next real command said nothing. Silently never telling a user
// their config layout moved is the single outcome this feature exists to
// prevent.
func TestUpgradeNotice_ShellCompletionDoesNotConsumeIt(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	withVersion(t, "0.11.0")

	// A home created by a version that predates the record — a real upgrader.
	if err := os.Remove(filepath.Join(tmp, ".agentsync", ".state", "last-run.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// Every completion entry point, including a bare TAB and a partial word.
	for _, args := range [][]string{
		{"__complete", ""},
		{"__complete", "ap"},
		{"__completeNoDesc", ""},
		{"completion", "bash"},
	} {
		if _, _, err := runCLISplit(t, env, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if rec := readLastRun(t, tmp); rec != nil && len(rec.NoticesSeen) > 0 {
			t.Fatalf("`%v` recorded the notice as seen. Completion output is consumed by a shell "+
				"with stderr discarded, so the user never saw it — and now never will: %+v", args, rec)
		}
	}

	// The user's next real command still gets it.
	_, stderr, err := runCLISplit(t, env, "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("completion consumed the notice; the real command shows nothing:\n%s", stderr)
	}
}
