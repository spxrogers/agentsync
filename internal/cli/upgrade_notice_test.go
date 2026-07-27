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
	if rec.Version != "0.11.0" || len(rec.NoticesSeen) == 0 {
		t.Fatalf("unexpected record: %+v", rec)
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

	// It must still record the notices as seen, so that the user's first real
	// `init` — which creates the home — doesn't retroactively trigger them.
	if rec := readLastRun(t, tmp); rec != nil && len(rec.NoticesSeen) == 0 {
		t.Errorf("fresh install recorded no seen notices, so they will fire later: %+v", rec)
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

	stdout, stderr, err := runCLISplit(t, env, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "migrate subagents") {
		t.Fatalf("notice should have fired on this run:\n%s", stderr)
	}
	var payload any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("the upgrade notice corrupted the --json payload: %v\n%s", err, stdout)
	}
}

// TestUpgradeNotice_UnwritableStateDoesNotFailCommand pins the best-effort
// contract: a UX marker must never take a user's command down with it.
func TestUpgradeNotice_UnwritableStateDoesNotFailCommand(t *testing.T) {
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
		t.Fatalf("an unwritable run record must not fail the command: %v\n%s", err, stderr)
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
