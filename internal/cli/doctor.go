package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	agit "github.com/spxrogers/agentsync/internal/git"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "diagnose first-run readiness: environment, schema, secrets, adapters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd)
			if err != nil {
				return err
			}
			home := paths.AgentsyncHome(paths.OSEnv{})

			fmt.Fprintln(p.Out, p.Bold("agentsync doctor"))
			fmt.Fprintln(p.Out, "  AGENTSYNC_HOME:", home)
			fmt.Fprintln(p.Out, "  Go version:    ", runtime.Version())
			fmt.Fprintln(p.Out, "  OS / arch:     ", runtime.GOOS, runtime.GOARCH)

			fmt.Fprintln(p.Out, "")
			p.Section("Source repo")
			fails := 0
			fails += checkHomeDir(p, home)
			fails += checkStateDir(p, home)
			fails += checkSubagentLayout(p, home)
			c, schemaOK := checkSchema(p, home)
			if !schemaOK {
				fails++
			}

			fmt.Fprintln(p.Out, "")
			p.Section("Secrets")
			if schemaOK {
				fails += checkSecrets(p, c.Config.Secrets, home)
				// Beyond the [secrets] block's shape, actually try to resolve every
				// ${secret:…}/${env:…} reference the canonical carries — a typo'd or
				// missing key would otherwise pass as healthy and only surface as a
				// broken apply.
				fails += checkSecretReferences(p, c, home)
			} else {
				fmt.Fprintln(p.Out, "  skipped (schema invalid above)")
			}

			fmt.Fprintln(p.Out, "")
			p.Section("Adapter detection")
			// Detection is INFORMATIONAL — each adapter's Detect() stats its config
			// dir under the target root and falls back to a PATH lookup. It never
			// touches the fails counter: a not-detected agent is a normal state (the
			// user may author config for a machine where the agent isn't installed).
			reg := registryFactory()
			for _, name := range allAgentNames() {
				a := reg.Lookup(name)
				if a == nil {
					// #160 guarantees every valid agent has a registered adapter, so a
					// nil lookup should be unreachable — but report it gracefully rather
					// than panic if that invariant ever regresses.
					fmt.Fprintf(p.Out, "  %s %-12s %s\n", p.Faint(ui.GlyphInfo), name, p.Faint("no adapter registered"))
					continue
				}
				detected, derr := a.Detect()
				switch {
				case derr != nil:
					// A Detect error is still informational — surface it, never fail.
					fmt.Fprintf(p.Out, "  %s %-12s %s\n", p.Faint(ui.GlyphInfo), name, p.Faint(fmt.Sprintf("detection error: %v", derr)))
				case detected:
					fmt.Fprintf(p.Out, "  %s %-12s %s\n", p.Green(ui.GlyphOK), name, p.Faint("detected"))
				default:
					fmt.Fprintf(p.Out, "  %s %-12s %s\n", p.Faint(ui.GlyphInfo), name, p.Faint("not detected"))
				}
			}

			fmt.Fprintln(p.Out, "")
			p.Section("Plugins")
			if schemaOK {
				checkPlugins(p, home)
			} else {
				fmt.Fprintln(p.Out, "  skipped (schema invalid above)")
			}

			fmt.Fprintln(p.Out, "")
			p.Section("Destination git backup")
			if schemaOK {
				checkDestinationGitBackup(p, c.Config.DestinationGitBackup)
			} else {
				fmt.Fprintln(p.Out, "  skipped (schema invalid above)")
			}

			fmt.Fprintln(p.Out, "")
			if fails > 0 {
				// The summary is part of doctor's REPORT (stdout, where the
				// per-check lines went), so it keeps the report's ✗ rather than
				// becoming a stderr ERROR diagnostic. The returned error is what
				// reaches the terminal ERROR line, one level up.
				fmt.Fprintf(p.Out, "%s %s\n", p.Red(ui.GlyphErr),
					p.Red(fmt.Sprintf("%d issue(s) detected — fix before running `agentsync apply`", fails)))
				return fmt.Errorf("doctor: %d issue(s) detected", fails)
			}
			p.Successf(ui.EmojiSuccess, "all checks passed")
			return nil
		},
	}
	markScopeUnaware(cmd, "doctor reports on THIS MACHINE (paths, adapters, the secrets backend), which is not scoped; "+
		"to validate a project tree's config run `agentsync check --scope project`")
	return cmd
}

// okCheck / failCheck / warnCheck render one readiness line. The label carries
// its own trailing alignment padding (e.g. "home dir   ") and is printed plain
// immediately before the colored status, so the "<label><status>" substring the
// doctor tests pin stays contiguous when color is off.
func okCheck(p *ui.Printer, label, status string) {
	fmt.Fprintf(p.Out, "  %s %s%s\n", p.Green(ui.GlyphOK), label, p.Green(status))
}

func failCheck(p *ui.Printer, label, status string) {
	fmt.Fprintf(p.Out, "  %s %s%s\n", p.Red(ui.GlyphErr), label, p.Red(status))
}

func warnCheck(p *ui.Printer, label, status string) {
	fmt.Fprintf(p.Out, "  %s %s%s\n", p.Yellow(ui.GlyphWarn), label, p.Yellow(status))
}

// infoCheck renders an informational readiness line — neither a pass nor a
// problem, such as doctor reporting that there is no [secrets] block to
// validate at all. It matches okCheck/failCheck/warnCheck's layout so the
// "<label><status>" substring the doctor tests pin stays contiguous when color
// is off.
func infoCheck(p *ui.Printer, label, status string) {
	fmt.Fprintf(p.Out, "  %s %s%s\n", p.Faint(ui.GlyphInfo), label, p.Faint(status))
}

// checkHomeDir verifies that AGENTSYNC_HOME exists and is a directory.
// Returns 1 if the check fails, 0 otherwise.
func checkHomeDir(p *ui.Printer, home string) int {
	info, err := os.Stat(home)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			failCheck(p, "home dir   ", "missing — run `agentsync init`")
			return 1
		}
		failCheck(p, "home dir   ", fmt.Sprintf("unreadable: %v", err))
		return 1
	}
	if !info.IsDir() {
		failCheck(p, "home dir   ", fmt.Sprintf("exists but is not a directory: %s", home))
		return 1
	}
	// A home dir without agentsync.toml is half-initialized (e.g. an authoring
	// command run before `init`); naming it explicitly, like `verify` does,
	// avoids calling the schema "ok" on a config-less home.
	if _, err := os.Stat(filepath.Join(home, "agentsync.toml")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			failCheck(p, "home dir   ", "missing agentsync.toml (half-initialized) — run `agentsync init`")
			return 1
		}
		failCheck(p, "home dir   ", fmt.Sprintf("agentsync.toml unreadable: %v", err))
		return 1
	}
	okCheck(p, "home dir   ", "ok")
	return 0
}

// checkStateDir verifies that .state/ is writable.
func checkStateDir(p *ui.Printer, home string) int {
	stateDir := filepath.Join(home, ".state")
	if info, err := os.Stat(stateDir); err != nil {
		failCheck(p, ".state/    ", "missing — run `agentsync init`")
		return 1
	} else if !info.IsDir() {
		failCheck(p, ".state/    ", "exists but is not a directory")
		return 1
	}
	probe := filepath.Join(stateDir, ".doctor-write-probe")
	if err := os.WriteFile(probe, []byte{}, 0o600); err != nil { //nolint:forbidigo // .state writability probe under ~/.agentsync, not a native destination
		failCheck(p, ".state/    ", fmt.Sprintf("not writable: %v", err))
		return 1
	}
	_ = os.Remove(probe) //nolint:forbidigo // removes the probe file it just wrote, not a native destination
	okCheck(p, ".state/    ", "ok (writable)")

	// Verify targets.json parses — the same load status/apply/diff/reconcile
	// do. A corrupt state file makes every real command exit 1, so a readiness
	// check that ignores it would falsely report healthy. A missing file is
	// fine (state.Load returns an empty state on a fresh install).
	//
	// sanitizeLines, not a raw %v. A migration refusal interpolates map keys read
	// verbatim from targets.json, which the schema-2 remedy explicitly expects may
	// be hand-edited, so a crafted key could otherwise smuggle a terminal escape
	// into this line and forge a passing check below it (#93/#171). migrate
	// already quotes every key with %q, which escapes the whole strip set
	// (controls, bidi overrides, zero-width) — so this call is a BACKSTOP, not
	// the only defense, and no state.Load error reaching it today carries a raw
	// escape.
	//
	// It stays because doctor prints its diagnostic inline rather than returning
	// it, and the rule is that EVERY state.Load error that can reach a terminal
	// passes through a sanitizer on the way. As of this comment there are 15
	// state.Load call sites and they take one of four routes — this is the
	// fourth, not an exception:
	//
	//   - returned to main (status, diff, apply, reconcile, explain, migrate,
	//     agent disable --purge, plugin's two poll sites): reportErrorTo
	//     sanitizeLines the whole chain;
	//   - warned as a "warning: " sentinel line into ui.WarnWriter, whose emit
	//     sanitizes the body (TestWarnWriterSanitizesAdapterText): import's
	//     hook-disown and state-seed sites via importIO.warnf, and opencode's
	//     Ingest, which cannot call ui.Sanitize itself — an adapter must not
	//     import ui, which is the whole reason that sentinel exists;
	//   - warned inline by `marketplace add`/`remove`, whose p.Warnf and diag
	//     sinks reach fmt.Fprintf WITHOUT sanitizing: sanitizeLines is applied at
	//     those two call sites instead, so the warning is covered whichever sink
	//     the caller passed;
	//   - printed inline here.
	//
	// Adding a 16th site means picking one of those four, not inventing a fifth.
	//
	// sanitizeLines rather than untrusted.Wrap because the refusal is multi-line
	// and ui.Sanitize STRIPS newlines — wrapping would run the remedy paragraphs
	// together.
	if _, err := state.Load(filepath.Join(stateDir, "targets.json")); err != nil {
		failCheck(p, "state file ", fmt.Sprintf("corrupt: %s", sanitizeLines(err.Error())))
		return 1
	}
	okCheck(p, "state file ", "ok")
	return 0
}

// checkSubagentLayout reports a pending agents/ → subagents/ migration as a
// failing check with the fix. doctor is the ONE command that surfaces the
// condition instead of dying at load: every other source-loading command is
// fail-closed through source.Load (see source.CheckSubagentLayout), which is
// why the checks below use source.LoadTolerant.
//
// User scope only, as doctor stands: doctor has no --scope/--project, so a
// project tree's pending migration is surfaced by the loading-command gate
// (`apply`/`status --scope project` hit it naturally).
func checkSubagentLayout(p *ui.Printer, home string) int {
	files, err := source.LegacySubagentFiles(afero.NewOsFs(), home)
	if err != nil {
		failCheck(p, "subagents  ", fmt.Sprintf("unreadable: %s", untrusted.Wrap(err.Error())))
		return 1
	}
	if len(files) == 0 {
		return 0
	}
	failCheck(p, "subagents  ", fmt.Sprintf(
		"%d file(s) still in the retired %s directory — run `agentsync migrate subagents` to move them to %s",
		len(files), filepath.Join(home, source.LegacySubagentsDir), filepath.Join(home, source.SubagentsDir),
	))
	return 1
}

// checkSchema validates agentsync.toml. Returns the parsed Canonical plus a
// success flag so the secrets checks can reuse it (the reference-resolution
// check needs the whole canonical, not just [secrets]).
//
// Tolerant load: a pending subagent-directory migration is already reported by
// checkSubagentLayout above, and reporting it a second time as "schema invalid"
// would bury the real fix.
func checkSchema(p *ui.Printer, home string) (source.Canonical, bool) {
	c, err := source.LoadTolerant(afero.NewOsFs(), home)
	if err != nil {
		// A strict-decode error embeds the raw offending config source line
		// (go-toml's StrictMissingError.String()), which can carry ESC/bidi bytes
		// from a shared config; sanitize the whole rendering (issue #93/#171).
		failCheck(p, "schema     ", fmt.Sprintf("invalid: %s", untrusted.Wrap(err.Error())))
		return source.Canonical{}, false
	}
	okCheck(p, "schema     ", fmt.Sprintf("ok (%d mcp, %d plugin(s), %d marketplace(s))",
		len(c.MCPServers), len(c.Plugins), len(c.Marketplaces)))
	return c, true
}

// checkSecretReferences resolves every ${secret:…}/${env:…} reference the
// canonical carries against the configured backends and reports any that cannot
// resolve now — a typo'd vault key (${secret:GITHUB_TOEKN}), a ref with no
// [secrets] backend configured, or an env var unset in this shell. Without it,
// doctor validated only the [secrets] block's SHAPE and reported all-green while
// a broken reference would fail the next apply.
//
// A ${secret:…} that cannot resolve is a hard fail (the vault has no such entry,
// or no backend is configured); a ${env:…} that cannot resolve is a warning, since
// an env var may legitimately be set only in the apply environment (CI). Resolved
// VALUES are never printed — only the reference names, and those through
// untrusted.Wrap so a config-derived key with control bytes is sanitized.
func checkSecretReferences(p *ui.Printer, c source.Canonical, home string) int {
	userHome := paths.HomeDir(paths.OSEnv{})
	secBackend := secrets.SelectBackend(c.Config.Secrets, home, userHome)
	missing := secrets.UnresolvedSecretRefs(&c, secBackend, secrets.EnvBackend{})
	if len(missing) == 0 {
		// Only affirm when there is actually something to resolve, so a config with
		// no references stays silent instead of adding a spurious "ok" line.
		if resolved := secrets.CollectResolved(&c, secBackend, secrets.EnvBackend{}); len(resolved) > 0 {
			okCheck(p, "references ", fmt.Sprintf("ok (%d reference(s) resolve)", len(resolved)))
		}
		return 0
	}
	fails := 0
	for _, ref := range missing {
		// ref is machine-generated as "secret:KEY" / "env:KEY"; the KEY is
		// config-derived (a shareable dotfile), so display via untrusted.Wrap.
		disp := untrusted.Wrap(ref)
		if strings.HasPrefix(ref, "env:") {
			warnCheck(p, "references ", fmt.Sprintf("%s does not resolve (env var unset here; may be set at apply time)", disp))
			continue
		}
		failCheck(p, "references ", fmt.Sprintf("%s does not resolve (no such vault key, or no [secrets] backend configured)", disp))
		fails++
	}
	return fails
}

// checkPlugins surfaces plugins installed natively in an agent (Claude and Codex)
// that are NOT declared in the canonical source — informational only, never a
// failure: agentsync treats them as foreign-managed, and `import <agent>:plugin`
// brings them under management. Probes every registered adapter (not just the
// enabled set) so a fresh user with Claude plugins but no agentsync config still
// sees the nudge.
func checkPlugins(p *ui.Printer, home string) {
	// Tolerant for the same reason checkSchema is: doctor reports a pending
	// subagent migration once, in its own check, rather than dying at load.
	c, err := source.LoadTolerant(afero.NewOsFs(), home)
	if err != nil {
		fmt.Fprintln(p.Out, "  skipped (source not loadable)")
		return
	}
	reg := registryFactory()
	undeclared := undeclaredNativePlugins(c, reg, reg.Names())
	// A plugin the agent installs itself that agentsync ALSO projects there
	// duplicates every component it ships. apply cannot see this (its plan never
	// reads the destination), so doctor is one of the two places it surfaces.
	duplicated := duplicatedNativePlugins(c, reg, reg.Names())
	if len(undeclared) == 0 && len(duplicated) == 0 {
		okCheck(p, "", "ok (no undeclared or duplicated native plugins)")
		return
	}
	for _, name := range reg.Names() {
		if dupes := duplicated[name]; len(dupes) > 0 {
			warnCheck(p, fmt.Sprintf("%-10s ", name), fmt.Sprintf(
				"%d also projected by agentsync: %s — components land twice; uninstall them in %s "+
					"or add %q to `native_agents`",
				len(dupes), untrusted.Join(dupes, ", "), name, name,
			))
		}
		missing := undeclared[name]
		if len(missing) == 0 {
			continue
		}
		// Native plugin names are influenced by the plugin author (read from the
		// agent's native config); they are untrusted.Text and sanitize on display
		// by construction (untrusted.Join renders each via its String()), so no
		// manual ui.Sanitize is needed here.
		warnCheck(p, fmt.Sprintf("%-10s ", name), fmt.Sprintf("%d not in source: %s — run `agentsync import %s:plugin`",
			len(missing), untrusted.Join(missing, ", "), name))
	}
}

// checkDestinationGitBackup reports the destination-git-backup mode and, per
// deep agent whose dir exists on disk, whether it is agentsync-versioned, under
// foreign source control, or untracked. Informational only — never a failure.
// Changing the mode is a one-line agentsync.toml edit (there is no `agentsync git`
// command, by design — issue #118).
func checkDestinationGitBackup(p *ui.Printer, cfg source.DestinationGitBackupConfig) {
	switch cfg.EffectiveMode() {
	case source.GitBackupModeOff:
		warnCheck(p, "mode       ", "off (rendered destination dirs are not versioned)")
	case source.GitBackupModeOn:
		okCheck(p, "mode       ", "on")
	case source.GitBackupModePrompt:
		okCheck(p, "mode       ", "prompt (asks on first apply to an untracked dir)")
	default:
		// Defensive: source.Load now rejects an invalid mode at load time
		// (validateMode), so doctor should never reach here on a loaded config.
		// Kept as a belt-and-suspenders guard.
		warnCheck(p, "mode       ", fmt.Sprintf("unknown value %q — use \"prompt\", \"on\", or \"off\"", cfg.Mode))
	}

	// Report per VERSION ROOT (deduped/de-nested across all agents), since a shared
	// dir like ~/.agents/skills belongs to several agents but is one repo.
	//
	// Probing ALL registered agents (reg.Names()) rather than only the enabled set
	// apply acts on is INTENTIONAL — like checkPlugins above, doctor surfaces
	// foreign/owned destination dirs the user should know about even for agents that
	// aren't enabled; the os.Stat filter below drops any root that doesn't exist yet.
	reg := registryFactory()
	for _, root := range enabledVersionRoots(reg, reg.Names(), adapter.ScopeUser, "") {
		if _, err := os.Stat(root); err != nil {
			continue // dir not created yet — nothing to report
		}
		st, err := agit.Detect(root)
		if err != nil {
			continue
		}
		label := root + " — "
		switch st {
		case agit.StateAgentsyncOwned:
			okCheck(p, label, st.String())
		case agit.StateUntracked:
			// An untracked root that CONTAINS a nested repo is one apply's initGuarded
			// will refuse to init (no repo-inside-a-repo) — so surface a warn here
			// instead of a clean "untracked" line, matching what apply actually does.
			if nested, _ := agit.HasNestedRepoBelow(root); nested {
				warnCheck(p, label, "contains a nested git repo — apply will skip git backup here")
			} else {
				fmt.Fprintf(p.Out, "  %s %s%s\n", p.Faint(ui.GlyphInfo), label, p.Faint(st.String()))
			}
		default:
			fmt.Fprintf(p.Out, "  %s %s%s\n", p.Faint(ui.GlyphInfo), label, p.Faint(st.String()))
		}
	}
}

// secretsFieldLabel maps a [secrets] validator Field to doctor's padded report
// column. The padding is part of the "<label><status>" substring the doctor
// tests pin, so it belongs to the LABEL, not to the message.
func secretsFieldLabel(f secrets.Field) string {
	switch f {
	case secrets.FieldBackend:
		return "backend    "
	case secrets.FieldRecipient:
		return "recipient  "
	case secrets.FieldIdentityFile:
		return "identity   "
	case secrets.FieldFile:
		return "age file   "
	}
	// Defensive: a Field added to internal/secrets without a column here still
	// renders, just unaligned, rather than printing an empty label.
	return string(f) + " "
}

// checkSecrets renders the [secrets] block's readiness from the SINGLE
// validator in internal/secrets — the same secrets.ValidateConfig `agentsync
// check` errors from, folding the backend name through the same
// NormalizeBackend `apply`'s SelectBackend uses.
//
// doctor does not decide what a valid [secrets] block is; it only chooses a
// glyph per severity. That is the whole point: doctor and check each used to
// carry their own copy of the contract, and the copies disagreed with each
// other AND with apply — doctor hard-failed `backend = "env"` (a supported
// backend that check accepted and apply resolves), both rejected an uppercase
// `"AGE"` that apply accepts, and doctor reported an un-stat-able vault path as
// "not yet created" and passed where check failed. (issue #228)
//
// Returns the number of hard failures, which the caller adds to doctor's issue
// count.
func checkSecrets(p *ui.Printer, cfg source.SecretsConfig, home string) int {
	userHome := paths.HomeDir(paths.OSEnv{})
	fails := 0
	for _, f := range secrets.ValidateConfig(cfg, home, userHome) {
		// f.Message is untrusted.Text: String() sanitizes the config-derived
		// identity_file / file paths it embeds, so a crafted (shareable-dotfile)
		// [secrets] value cannot inject terminal escapes into doctor's report
		// (issue #93/#171).
		label, status := secretsFieldLabel(f.Field), f.Message.String()
		switch f.Severity {
		case secrets.SeverityFail:
			failCheck(p, label, status)
			fails++
		case secrets.SeverityWarn:
			warnCheck(p, label, status)
		case secrets.SeverityInfo:
			infoCheck(p, label, status)
		default:
			okCheck(p, label, status)
		}
	}
	return fails
}
