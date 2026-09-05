package cli_test

import (
	"path/filepath"
	"strings"
	"testing"

	agit "github.com/spxrogers/agentsync/internal/git"
)

// pluginReapplyFixture builds the home the two tests below share: an inited
// agentsync home with the named agents enabled, optionally
// `[destination_directory_git_backup] mode = "on"` (tests have no TTY, so the
// default `prompt` fails closed and never inits a repo), a SOURCE skill, and
// the versioned fixture marketplace with its `demo` plugin installed. It
// deliberately does NOT run `apply`: the first write into the destinations
// must be the upgrade's re-apply, or both tests would pass on what `apply`
// had already done. Returns the fixture's env and its target root.
//
// The skill matters: it renders to ~/.claude/skills/demo/SKILL.md, INSIDE a
// version root. An MCP-only config only touches ~/.claude.json at $HOME, which
// is never versioned (agentsync never inits a repo at $HOME) — a git-backup
// test built on it would pass with the backup entirely absent.
func pluginReapplyFixture(t *testing.T, gitBackup bool, agents ...string) (map[string]string, string) {
	t.Helper()
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()

	mustRun(t, env, "init")
	for _, a := range agents {
		mustRun(t, env, "agent", "add", a)
	}
	if gitBackup {
		enableGitBackupOn(t, tmp)
	}
	writeSkillSource(t, tmp, "demo", "body")
	mpDir := makeVersionedMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "add", "demo@test-mp-v")
	return env, tmp
}

// TestPluginUpgrade_TakesGitBackupBaselineAndCheckpoint is the behavioural pin
// on the bug half of #231. Before it, `plugin upgrade` ended in a second,
// hand-maintained copy of the apply pipeline that had no
// [destination_directory_git_backup] pass at all: the upgrade overwrote a
// destination dir with NO pre-apply baseline and NO checkpoint, so `agentsync
// revert` could not undo it. The re-apply is now the real pipeline, and this
// test mirrors TestApply_GitBackupCheckpoint so the two read as one contract:
// with git backup on, the FIRST write under ~/.claude — here the upgrade's
// re-apply, not an `apply` — initializes the local repo and records a
// pre-apply baseline plus the apply checkpoint (≥2 commits, the oldest being
// the baseline). Without this test, dropping the git-backup pass from the
// plugin path (applyOpts{noGitBackup: true} at the call site) fails nothing.
func TestPluginUpgrade_TakesGitBackupBaselineAndCheckpoint(t *testing.T) {
	env, tmp := pluginReapplyFixture(t, true, "claude")

	out, err := runCLI(t, env, "plugin", "upgrade", "demo")
	if err != nil {
		t.Fatalf("plugin upgrade demo: %v\n%s", err, out)
	}

	claude := filepath.Join(tmp, ".claude")
	st, err := agit.Detect(claude)
	if err != nil {
		t.Fatal(err)
	}
	if st != agit.StateAgentsyncOwned {
		t.Fatalf("~/.claude state after plugin upgrade = %v, want agentsync-owned — the re-apply took no git backup:\n%s", st, out)
	}
	repo, err := agit.Open(claude)
	if err != nil {
		t.Fatal(err)
	}
	cps, err := repo.Log(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cps) < 2 {
		t.Fatalf("want >=2 commits after the upgrade's re-apply (baseline + checkpoint), got %d:\n%s", len(cps), out)
	}
	if oldest := cps[len(cps)-1]; !strings.Contains(oldest.Subject, "pre-apply baseline") {
		t.Fatalf("oldest commit should be the pre-apply baseline, got subject %q", oldest.Subject)
	}
}

// TestPluginUpgrade_RendersEveryEnabledAgent pins what the plugin path must
// NOT gain from sharing the pipeline: `apply --agents`' narrowing. The reason
// it cannot narrow today is structural, not a value: selectAgents returns every
// enabled agent unless the CALLING command reports
// cmd.Flags().Changed("agents"), and no plugin command registers that flag —
// so applyOpts.agentsCSV is inert on this path whatever it holds. What WOULD
// break this test is a pipeline that honoured a non-empty agentsCSV without
// the Changed gate, combined with a plugin call site that passes one — the
// mutation this test was written against. Two agents are enabled; after the
// upgrade's re-apply both must hold the plugin's MCP server.
func TestPluginUpgrade_RendersEveryEnabledAgent(t *testing.T) {
	env, tmp := pluginReapplyFixture(t, false, "claude", "opencode")

	out, err := runCLI(t, env, "plugin", "upgrade", "demo")
	if err != nil {
		t.Fatalf("plugin upgrade demo: %v\n%s", err, out)
	}
	for _, dest := range []string{
		filepath.Join(tmp, ".claude.json"),
		filepath.Join(tmp, ".config", "opencode", "opencode.json"),
	} {
		got, rerr := readFileString(t, dest)
		if rerr != nil {
			t.Fatalf("%s missing after plugin upgrade — the re-apply did not render every enabled agent: %v\n%s", dest, rerr, out)
		}
		if !strings.Contains(got, "demo-mcp") {
			t.Fatalf("%s does not carry the plugin's demo-mcp server after plugin upgrade:\n%s", dest, got)
		}
	}
}
