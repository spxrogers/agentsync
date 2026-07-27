package cli

import (
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

// upgradeNotices is the ordered set of notices this binary can show. Append
// only; never renumber an ID.
var upgradeNotices = []upgradeNotice{
	{
		ID:       "0.11.0-cli-surface",
		Since:    "0.11.0",
		Headline: "breaking changes to the canonical layout and the CLI surface",
		Actions: []string{
			"the canonical subagent directory moved: agents/ → subagents/ — run `agentsync migrate subagents` (per tree)",
			"`agentsync verify` is now `agentsync check` (no alias) — doctor checks the MACHINE, check checks the CONFIG",
			"`agentsync plugin install` is now `agentsync plugin add`, and `agentsync secrets` is now `agentsync secret` (no aliases)",
			"`agentsync update` is deprecated — use `agentsync plugin outdated` / `agentsync plugin upgrade --all`",
			"`agentsync explain <plugin>` moved to `agentsync plugin explain <plugin>` (no alias; the old name now explains a FILE)",
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

	home := paths.AgentsyncHome(paths.OSEnv{})
	// A home that does not exist yet is a FRESH INSTALL, not an upgrade: nothing
	// can have broken under a user who has no config. Return WITHOUT writing —
	// creating .state/ here would materialize the home and make the user's first
	// `agentsync init` refuse ("already contains files"). `init` seeds the record
	// itself (seedUpgradeNoticeRecord), so "home exists + no record" means
	// exactly one thing: a home created by a version that predates the record.
	if !homeExists(home) {
		return
	}
	recordPath := filepath.Join(home, ".state", state.LastRunFile)

	rec, err := state.LoadLastRun(recordPath)
	if err != nil {
		return // corrupt record: say nothing rather than guess
	}

	if rec == nil {
		rec = &state.LastRun{}
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
	_ = state.SaveLastRun(recordPath, rec)
}

// homeExists reports whether the agentsync home is present as a directory —
// the signal that separates an upgrade from a first install.
func homeExists(home string) bool {
	fi, err := os.Stat(home)
	return err == nil && fi.IsDir()
}

// printUpgradeNotices renders the banner to stderr.
func printUpgradeNotices(p *ui.Printer, notices []upgradeNotice) {
	w := p.Err
	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "%s %s\n",
		p.Yellow(ui.GlyphWarn+" agentsync "+Version+":"),
		p.Bold("you upgraded across a change that needs your attention"))
	for _, n := range notices {
		fmt.Fprintf(w, "  %s %s %s\n",
			p.Yellow(ui.GlyphArrow), p.Bold("since "+n.Since+" —"), n.Headline)
		for _, a := range n.Actions {
			fmt.Fprintf(w, "      %s %s\n", p.Faint(ui.GlyphInfo), a)
		}
		fmt.Fprintf(w, "      %s %s\n", p.Faint("read more:"), p.Cyan(docsBaseURL+n.Path))
	}
	fmt.Fprintf(w, "  %s\n", p.Faint("(shown once; silence it with "+NoUpgradeNoticeEnv+"=1)"))
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
	_ = state.SaveLastRun(filepath.Join(home, ".state", state.LastRunFile), rec)
}
