package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
)

// docsBaseURL is the published documentation site every notice links into.
const docsBaseURL = "https://agentsync.cc"

// NoUpgradeNoticeEnv opts a machine out of the first-run-after-upgrade notice
// entirely: nothing is printed and nothing is recorded, so unsetting it shows
// any still-unseen notice rather than swallowing it permanently.
const NoUpgradeNoticeEnv = "AGENTSYNC_NO_UPGRADE_NOTICE"

// upgradeNotice is one "this changed under you" message, shown at most once per
// machine. agentsync is distributed through channels with no usable post-install
// hook — `go install` has none at all, a Homebrew cask's caveats print only at
// install time, Scoop has nothing, and only deb/rpm have real scripts — so the
// binary itself is the ONLY place that reliably reaches an upgrading user.
type upgradeNotice struct {
	// ID is the stable key recorded in .state/last-run.json. It must never be
	// reused or renamed: a rename re-shows the notice to everyone.
	ID string
	// Since is the version that introduced the change, for display only. The
	// show/hide decision is made by ID, not by version comparison — a user who
	// jumps several releases must still see every notice they missed, and a
	// pre-release / non-semver build must not silently swallow one.
	Since string
	// Headline is one line: what changed.
	Headline string
	// Actions are the concrete "do this" lines. Keep them short and literal —
	// this is the last thing a user reads before their cron job breaks.
	Actions []string
	// Path is the docs page, appended to docsBaseURL.
	Path string
}

// upgradeNotices is the ordered set of notices this binary can show.
//
// APPEND ONLY; never renumber or rename an ID — the ID is the key recorded in
// .state/last-run.json, so a rename re-shows the notice to every user who
// already dismissed it, and a duplicate makes the second entry unreachable.
// Never DELETE an entry either: TestUpgradeNoticeNamesEveryRetiredCommand
// requires every retirement to be named somewhere in this table, so removing
// an old notice fails that guard — and the cheapest-looking fix, dropping the
// retirement from the shared `retirements` table, silently un-guards the
// resurrection check too. Old notices cost one struct literal; leave them.
// TestUpgradeNoticeTableIsWellFormed enforces the shape.
//
// Every entry here needs a matching section in
// website/src/content/docs/reference/upgrading.mdx — the page Path points at.
// The two are maintained by hand in parallel; adding a notice without the
// section ships a banner whose "read more" link lands on nothing.
var upgradeNotices = []upgradeNotice{
	{
		ID:       "0.11.0-cli-surface",
		Since:    "0.11.0",
		Headline: "breaking changes to the canonical layout and the CLI surface",
		Actions: []string{
			"the canonical subagent directory moved: agents/ → subagents/ — run `agentsync migrate subagents` (per tree)",
			"`agentsync verify` is now `agentsync check` (no alias) — doctor checks the MACHINE, check checks the CONFIG",
			"`agentsync plugin install` is now `agentsync plugin add`, and `agentsync secrets` is now `agentsync secret` (no aliases)",
			"`agentsync update` was REMOVED (no alias) — use `agentsync plugin outdated` / `agentsync plugin upgrade --all`",
			"`agentsync explain <plugin>` moved to `agentsync plugin explain <plugin>` (no alias; the old name now explains a FILE)",
		},
		Path: "/reference/upgrading/",
	},
	{
		ID:       "0.11.0-plugin-component-namespacing",
		Since:    "0.11.0",
		Headline: "plugin-provided subagents, skills, and commands are now namespaced by their plugin",
		Actions: []string{
			"a plugin's component renders as <plugin>-<name>: feature-dev's code-reviewer is now feature-dev-code-reviewer",
			"invoke plugin agents and commands by the new name (@agent-feature-dev-code-reviewer, /feature-dev-review)",
			"your own ~/.agentsync/ components are NOT renamed; a derived name that clashes with one is reported, not silently resolved",
			"the next `agentsync apply` removes the old un-namespaced files it wrote; a file you hand-edited is backed up first",
		},
		Path: "/reference/upgrading/",
	},
	{
		ID:       "0.11.0-plugin-native-agents",
		Since:    "0.11.0",
		Headline: "a plugin an agent installs itself is no longer projected into that agent",
		Actions: []string{
			"plugins/<id>.toml gains `native_agents` — agents whose own plugin manager installs the plugin, so apply skips them",
			"`agents` in plugins/<id>.toml now actually narrows fan-out; it was documented but never read, so a narrowed list had no effect",
			"EXISTING installs are unchanged until you act: re-run `agentsync import <agent>:plugin` to record the deferral, or add `native_agents` by hand",
			"`status` / `doctor` now warn when a plugin is installed in an agent AND projected there — that pair is the duplicate",
		},
		Path: "/reference/upgrading/",
	},
}

// maybePrintUpgradeNotice shows, at most once per machine, the notices a user
// upgrading into this binary has not seen. It is wired as the root command's
// PersistentPreRunE so every subcommand inherits it.
//
// It is BEST-EFFORT by construction: every read/write error degrades to
// "say nothing". A UX marker must never fail a user's command, so this function
// returns no error at all.
//
// Output goes to STDERR, always. Several commands emit a machine-readable
// payload on stdout (`status --json`, `diff --json`, `explain --json`), and a
// banner there would corrupt what a caller is piping.
//
// Two accepted residuals, both deliberate:
//
//   - The silent returns are undiagnosable. Wiring internal/log through here
//     would mean threading a writer + verbosity into a function whose entire
//     contract is "never get in the way"; the failure modes are also
//     self-announcing (the notice either shows again next run, or the user
//     never sees it and reads the upgrade page instead). A corrupt record no
//     longer hides the notice, which was the one silent failure that mattered.
//
//   - "Once per machine" is lock-free, so N truly simultaneous invocations can
//     each print. Taking the global lock for a banner would serialize every
//     command in the tool behind a UX marker — a far worse trade than a
//     duplicated banner in the rare concurrent case.
//
//     The record write inherits that: state.SaveLastRun goes through
//     iox.AtomicWrite, whose temp file has a FIXED sibling name, so concurrent
//     writers share one inode and can tear the file — the same property that
//     disqualifies AtomicWrite for the ensureStateGitignore call this function
//     makes one line earlier (see TestEnsureStateGitignoreDoesNotUseATruncatingWrite,
//     which bans it there by name). Do NOT read this as "AtomicWrite is safe here". It is
//     TOLERATED here and not there because the two failures are not
//     comparable: a torn .gitignore destroys rules the user wrote and cannot
//     get back, while a torn last-run.json is a UX marker that LoadLastRun
//     reads as ErrCorruptLastRun, i.e. "this machine has shown nothing" — so
//     it self-repairs on the next run at the cost of one duplicate banner.
//     That is the whole reason the corrupt-record path is forgiving. If a
//     future field ever makes this record authoritative for anything, it needs
//     a unique temp name (os.CreateTemp in the parent) or the lock.
func maybePrintUpgradeNotice(cmd *cobra.Command) {
	// A build with no version stamped is a local `go build` or a test binary.
	// Showing (and recording) a notice there would fire in every test run and
	// tell a developer nothing, so `dev` opts out of the whole mechanism.
	if Version == "" || Version == "dev" {
		return
	}
	if os.Getenv(NoUpgradeNoticeEnv) == "1" {
		return
	}
	// A shell-completion request is not a user reading output. Cobra runs this
	// hook for `__complete` too, and the generated bash/zsh scripts invoke it as
	// `out=$(eval "${requestComp}" 2>/dev/null)` — stderr DISCARDED. So a single
	// TAB after `agentsync ` would print the banner into /dev/null and then
	// record it as seen, and the user's next real command would say nothing.
	// That is the one failure direction this feature must not have: silently
	// never telling them their config layout moved. Anyone with completions
	// installed would have hit it before ever seeing the notice.
	if isCompletionRequest(cmd) {
		return
	}

	home := paths.AgentsyncHome(paths.OSEnv{})
	// A home that does not exist yet is a FRESH INSTALL, not an upgrade: nothing
	// can have broken under a user who has no config. Return WITHOUT writing —
	// creating .state/ here would materialize the home and make the user's first
	// `agentsync init` refuse ("already contains files"). `init` seeds the record
	// itself (seedUpgradeNoticeRecord).
	if !homeExists(home) {
		return
	}
	recordPath := filepath.Join(home, ".state", state.LastRunFile)

	// The home EXISTING is not enough to call this an upgrade. A project-scope
	// user never runs `agentsync init` at user scope, but apply records state
	// CENTRALLY under ~/.agentsync/.state/ keyed by project root — so the first
	// project-scope command that touches state materializes a user home that
	// `init` never seeded. Read literally, "home exists + no record" then
	// misfires on a brand-new install: a 0.11.0-native user was told to run
	// `agentsync migrate subagents` against a tree that never had an agents/
	// directory.
	//
	// agentsync.toml is the marker that distinguishes the two: `init` writes it,
	// central state never does. Its absence means this home was materialized for
	// state-keeping, not by a user config that a release could have broken — so
	// stay quiet.
	//
	// Return WITHOUT seeding. Seeding here would mark every notice as seen for a
	// user who has not seen them, and this check runs BEFORE the record is ever
	// read — so it costs nothing to skip. It is not merely redundant but
	// harmful: a home that later gains an agentsync.toml (a dotfiles restore
	// landing after the first run, `agentsync init` run tomorrow) would be
	// permanently silenced about changes it never heard.
	//
	// A project-scope-only user upgrading from an older release therefore does
	// not get the banner. They are not left stranded: the retired layout is
	// fail-closed at load, so their next command refuses with an error naming
	// `agentsync migrate subagents` for that tree — a stronger mechanism than a
	// one-time banner, and one that fires per tree rather than per machine.
	if !hasUserConfig(home) {
		return
	}

	rec, err := state.LoadLastRun(recordPath)
	switch {
	case errors.Is(err, state.ErrCorruptLastRun):
		// A record that cannot be parsed carries no information, so the honest
		// reading is "this machine has shown nothing" — show the notice and
		// overwrite the file. Bailing here instead meant one truncated or empty
		// last-run.json (a crash mid-write, a full disk) suppressed the notice
		// PERMANENTLY, which is the one direction this feature must not fail in:
		// the user silently never learns their config layout moved. LoadLastRun
		// hands back a usable zero record for exactly this.
	case err != nil:
		return // real I/O trouble: say nothing rather than guess
	}

	var pending []upgradeNotice
	for _, n := range upgradeNotices {
		if rec.Seen(n.ID) {
			continue
		}
		pending = append(pending, n)
	}
	if len(pending) == 0 && rec.Version == Version {
		return // nothing to say and nothing to update
	}

	if len(pending) > 0 {
		p, perr := newPrinter(cmd)
		if perr != nil {
			return
		}
		printUpgradeNotices(p, pending)
	}

	// Record AFTER printing, and only mark what we actually resolved: a write
	// failure (read-only home, full disk) just means the notice shows again
	// next time, which is the harmless direction to fail in.
	for _, n := range pending {
		rec.MarkSeen(n.ID)
	}
	rec.Version = Version
	if mkErr := os.MkdirAll(filepath.Dir(recordPath), 0o755); mkErr != nil {
		return
	}
	// This is a second path that can CREATE .state/ (init is the other), and
	// ~/.agentsync is routinely a committed dotfiles repo — so the record must
	// be ignored here too, or `git add -A` sweeps one machine's run marker into
	// the repo and carries it to every other machine. A home created by a
	// version that predates the .gitignore scaffolding is exactly the population
	// this notice fires for.
	_ = ensureStateGitignore(home)
	_ = state.SaveLastRun(recordPath, rec)
}

// isCompletionRequest reports whether cmd is (or is under) a shell-completion
// command: cobra's hidden `__complete` / `__completeNoDesc` request commands, or
// the user-facing `completion` generator.
//
// Their output is consumed by a shell, not read by a person — and the generated
// scripts discard stderr — so anything "shown" during one is shown to nobody.
func isCompletionRequest(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd, "completion":
			return true
		}
	}
	return false
}

// homeExists reports whether the agentsync home is present as a directory.
func homeExists(home string) bool {
	fi, err := os.Stat(home)
	return err == nil && fi.IsDir()
}

// hasUserConfig reports whether the home holds a USER-scope config, as opposed
// to having been materialized purely to keep central state for a project tree.
//
// agentsync.toml is the discriminator: `agentsync init` writes it (and seeds the
// run record alongside), while the central-state path under .state/ never does.
// Without this check, the first project-scope command to record state creates
// ~/.agentsync/ with no record, and "home exists + no record" reads as an
// upgrade — firing a breaking-change banner at a brand-new user.
func hasUserConfig(home string) bool {
	fi, err := os.Stat(filepath.Join(home, "agentsync.toml"))
	return err == nil && fi.Mode().IsRegular()
}

// printUpgradeNotices renders the banner to stderr.
func printUpgradeNotices(p *ui.Printer, notices []upgradeNotice) {
	w := p.Err
	fmt.Fprintln(w, "")
	// The banner leads with the shared WARN label — it IS a warning about this
	// run — and hangs its body off the label's message column via Detailf, so
	// it reads as one block rather than as a second, competing layout language.
	p.Warnf("agentsync %s: %s", Version, p.Bold("you upgraded across a change that needs your attention"))
	for _, n := range notices {
		p.Detailf("%s %s %s", p.Yellow(ui.GlyphArrow), p.Bold("since "+n.Since+" —"), n.Headline)
		for _, a := range n.Actions {
			p.Detailf("    %s %s", p.Faint(ui.GlyphInfo), a)
		}
		p.Detailf("    %s %s", p.Faint("read more:"), p.Cyan(docsBaseURL+n.Path))
	}
	p.Detailf("%s", p.Faint("(shown once; silence it with "+NoUpgradeNoticeEnv+"=1)"))
	fmt.Fprintln(w, "")
}

// seedUpgradeNoticeRecord marks every notice this binary knows as already seen,
// for a home being created RIGHT NOW. `init` calls it so a brand-new user never
// gets an upgrade banner about changes that predate their install — and so the
// notice path can treat "home exists, no record" as an unambiguous upgrade.
//
// Best-effort like the notice itself: a failure only means the new user may see
// one stale banner, which is far better than a failed `init`.
func seedUpgradeNoticeRecord(home string) {
	if Version == "" || Version == "dev" {
		return
	}
	rec := &state.LastRun{Version: Version}
	for _, n := range upgradeNotices {
		rec.MarkSeen(n.ID)
	}
	_ = ensureStateGitignore(home)
	_ = state.SaveLastRun(filepath.Join(home, ".state", state.LastRunFile), rec)
}
