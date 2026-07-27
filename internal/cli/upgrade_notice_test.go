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

// TestUpgradeNotice_ProjectScopeUserSeesItAfterApply pins why keying off the
// USER home is right even for someone who only ever works at project scope.
//
// apply records state centrally under ~/.agentsync/.state/ keyed by project
// root, so a project-scope user has a user home from their first apply onward.
// The only window where they miss the notice is before that first apply — when
// agentsync has rendered nothing for them, so nothing has broken under them.
func TestUpgradeNotice_ProjectScopeUserSeesItAfterApply(t *testing.T) {
	tmp := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	withVersion(t, "0.11.0")

	mustRun(t, env, "init", "--project", proj)
	mustRun(t, env, "agent", "add", "claude", "--project", proj)
	mustRun(t, env, "apply", "--project", proj)

	// The first apply materialized the central state home; clear the record so
	// this stands in for an upgrading project-scope user.
	if err := os.Remove(filepath.Join(tmp, ".agentsync", ".state", "last-run.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	_, stderr, err := runCLISplit(t, env, "status", "--project", proj)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("a project-scope user who has applied must see the notice:\n%s", stderr)
	}
}

// TestUpgradeNotice_UnreadableStateDoesNotFailCommand pins the best-effort
// contract: a UX marker must never take a user's command down with it.
//
// Named for what it actually exercises. With .state replaced by a regular file,
// ReadFile fails with ENOTDIR — which is NOT os.ErrNotExist — so LoadLastRun
// returns an I/O error and maybePrintUpgradeNotice bails long before
// MkdirAll/SaveLastRun. It is the unreadable path, not the unwritable one; the
// old name meant the write-failure branch looked covered when it was not.
func TestUpgradeNotice_UnreadableStateDoesNotFailCommand(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	withVersion(t, "0.11.0")

	// Replace .state with a regular FILE so MkdirAll/AtomicWrite cannot succeed
	// — a uid-independent way to make the record unwritable (the container runs
	// as root, where a chmod would not bite).
	stateDir := filepath.Join(tmp, ".agentsync", ".state")
	if err := os.RemoveAll(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateDir, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, stderr, err := runCLISplit(t, env, "version"); err != nil {
		t.Fatalf("an unreadable run record must not fail the command: %v\n%s", err, stderr)
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

// TestUpgradeNoticeTableIsWellFormed guards the append-only rule that the table
// states but nothing enforced. An ID is the durable key: renaming one re-shows
// its notice to every user who already dismissed it, and duplicating one hides
// the second silently.
func TestUpgradeNoticeTableIsWellFormed(t *testing.T) {
	ids := map[string]bool{}
	for _, n := range cli.UpgradeNoticesForTest() {
		switch {
		case n.ID == "":
			t.Errorf("notice with empty ID (%q): the ID is the recorded key", n.Headline)
		case ids[n.ID]:
			t.Errorf("duplicate notice ID %q — the second is silently unreachable", n.ID)
		}
		ids[n.ID] = true
		if n.Since == "" {
			t.Errorf("notice %q has no Since; the banner prints it", n.ID)
		}
		if n.Headline == "" {
			t.Errorf("notice %q has no Headline", n.ID)
		}
		if len(n.Actions) == 0 {
			t.Errorf("notice %q lists no actions, so it tells the user what broke but not what to do", n.ID)
		}
		if !strings.HasPrefix(n.Path, "/") {
			t.Errorf("notice %q Path %q must start with '/' (it is appended to the docs base URL)", n.ID, n.Path)
		}
	}
	if len(ids) == 0 {
		t.Fatal("no notices defined; this guard would vacuously pass")
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
