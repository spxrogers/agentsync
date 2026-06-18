package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
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
			cfg, schemaOK := checkSchema(p, home)
			if !schemaOK {
				fails++
			}

			fmt.Fprintln(p.Out, "")
			p.Section("Secrets")
			if schemaOK {
				fails += checkSecrets(p, cfg.Secrets, home)
			} else {
				fmt.Fprintln(p.Out, "  skipped (schema invalid above)")
			}

			fmt.Fprintln(p.Out, "")
			p.Section("Adapter detection (PATH-only)")
			for _, name := range allAgentNames() {
				bin := agentBinary(name)
				if bin == "" {
					fmt.Fprintf(p.Out, "  %s %-12s %s\n", p.Faint(ui.GlyphInfo), name, p.Faint("(no PATH binary; detected by config dir)"))
					continue
				}
				path, lookErr := exec.LookPath(bin)
				if lookErr != nil {
					fmt.Fprintf(p.Out, "  %s %-12s %s\n", p.Faint(ui.GlyphInfo), name, p.Faint("not found in PATH"))
					continue
				}
				fmt.Fprintf(p.Out, "  %s %-12s %s\n", p.Green(ui.GlyphOK), name, p.Faint(path))
			}

			fmt.Fprintln(p.Out, "")
			p.Section("Plugins")
			if schemaOK {
				checkPlugins(p, home)
			} else {
				fmt.Fprintln(p.Out, "  skipped (schema invalid above)")
			}

			fmt.Fprintln(p.Out, "")
			if fails > 0 {
				fmt.Fprintf(p.Out, "%s %s\n", p.Red(ui.GlyphErr),
					p.Red(fmt.Sprintf("%d issue(s) detected — fix before running `agentsync apply`", fails)))
				return fmt.Errorf("doctor: %d issue(s) detected", fails)
			}
			fmt.Fprintf(p.Out, "%s %s\n", p.Green(ui.GlyphOK), p.Green("all checks passed"))
			return nil
		},
	}
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
	if err := os.WriteFile(probe, []byte{}, 0o600); err != nil {
		failCheck(p, ".state/    ", fmt.Sprintf("not writable: %v", err))
		return 1
	}
	_ = os.Remove(probe)
	okCheck(p, ".state/    ", "ok (writable)")

	// Verify targets.json parses — the same load status/apply/diff/reconcile
	// do. A corrupt state file makes every real command exit 1, so a readiness
	// check that ignores it would falsely report healthy. A missing file is
	// fine (state.Load returns an empty state on a fresh install).
	if _, err := state.Load(filepath.Join(stateDir, "targets.json")); err != nil {
		failCheck(p, "state file ", fmt.Sprintf("corrupt: %v", err))
		return 1
	}
	okCheck(p, "state file ", "ok")
	return 0
}

// checkSchema validates agentsync.toml. Returns the parsed Config plus a
// success flag so secrets checks can reuse it.
func checkSchema(p *ui.Printer, home string) (source.Config, bool) {
	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		failCheck(p, "schema     ", fmt.Sprintf("invalid: %v", err))
		return source.Config{}, false
	}
	okCheck(p, "schema     ", fmt.Sprintf("ok (%d mcp, %d plugin(s), %d marketplace(s))",
		len(c.MCPServers), len(c.Plugins), len(c.Marketplaces)))
	return c.Config, true
}

// checkPlugins surfaces plugins installed natively in an agent (Claude in v1)
// that are NOT declared in the canonical source — informational only, never a
// failure: agentsync treats them as foreign-managed, and `import <agent>:plugin`
// brings them under management. Probes every registered adapter (not just the
// enabled set) so a fresh user with Claude plugins but no agentsync config still
// sees the nudge.
func checkPlugins(p *ui.Printer, home string) {
	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		fmt.Fprintln(p.Out, "  skipped (source not loadable)")
		return
	}
	reg := registryFactory()
	undeclared := undeclaredNativePlugins(c, reg, reg.Names())
	if len(undeclared) == 0 {
		okCheck(p, "", "ok (no undeclared native plugins)")
		return
	}
	for _, name := range reg.Names() {
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

// checkSecrets validates the [secrets] block: backend present, identity
// file exists with restrictive permissions, recipient set.
func checkSecrets(p *ui.Printer, cfg source.SecretsConfig, home string) int {
	if cfg.Backend == "" {
		fmt.Fprintf(p.Out, "  %s backend    %s\n", p.Faint(ui.GlyphInfo), p.Faint("not configured (skip — no [secrets] block)"))
		return 0
	}
	if cfg.Backend != "age" {
		failCheck(p, "backend    ", fmt.Sprintf("unsupported: %q (want \"age\")", cfg.Backend))
		return 1
	}
	okCheck(p, "backend    ", "age")

	fails := 0
	if cfg.Recipient == "" {
		failCheck(p, "recipient  ", "missing — set [secrets].recipient in agentsync.toml")
		fails++
	} else {
		okCheck(p, "recipient  ", "set")
	}

	if cfg.IdentityFile == "" {
		failCheck(p, "identity   ", "missing — set [secrets].identity_file in agentsync.toml")
		return fails + 1
	}
	// Resolve identity_file the same way apply does (expanding ${env:HOME}/~
	// via paths.HomeDir so it honours AGENTSYNC_TARGET_ROOT), so doctor and
	// apply never disagree on the path.
	userHome := paths.HomeDir(paths.OSEnv{})
	idPath := secrets.ResolveIdentityFile(cfg, home, userHome)
	info, err := os.Stat(idPath)
	if err != nil {
		failCheck(p, "identity   ", fmt.Sprintf("%s — not readable (%v)", idPath, err))
		return fails + 1
	}
	// Use the same check apply/verify use so doctor never disagrees: it honors
	// AGENTSYNC_AGE_SKIP_PERM_CHECK=1 and the Windows ACL caveat, unlike the
	// previous inline 0o077 mask which falsely failed an opted-out 0644 key.
	if permErr := secrets.CheckIdentityPermissions(idPath); permErr != nil {
		failCheck(p, "identity   ", fmt.Sprintf("%s — too permissive (%v); chmod 600 (or set AGENTSYNC_AGE_SKIP_PERM_CHECK=1)", idPath, info.Mode().Perm()))
		return fails + 1
	}
	okCheck(p, "identity   ", fmt.Sprintf("ok (%s)", idPath))

	// Age-encrypted file location — warn if missing (legitimate on a
	// fresh install where the user hasn't called `secrets set` yet).
	agePath := secrets.ResolveAgeFile(cfg, home, userHome)
	if _, err := os.Stat(agePath); err != nil {
		warnCheck(p, "age file   ", fmt.Sprintf("%s — not yet created (run `agentsync secrets edit` to author)", agePath))
	} else {
		okCheck(p, "age file   ", agePath)
	}
	return fails
}
