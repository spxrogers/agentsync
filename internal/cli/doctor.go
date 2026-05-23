package cli

import (
	"errors"
	"fmt"
	"io"
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
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "diagnose first-run readiness: environment, schema, secrets, adapters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			home := paths.AgentsyncHome(paths.OSEnv{})

			fmt.Fprintln(w, "agentsync doctor")
			fmt.Fprintln(w, "  AGENTSYNC_HOME:", home)
			fmt.Fprintln(w, "  Go version:    ", runtime.Version())
			fmt.Fprintln(w, "  OS / arch:     ", runtime.GOOS, runtime.GOARCH)

			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "Source repo")
			fails := 0
			fails += checkHomeDir(w, home)
			fails += checkStateDir(w, home)
			cfg, schemaOK := checkSchema(w, home)
			if !schemaOK {
				fails++
			}

			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "Secrets")
			if schemaOK {
				fails += checkSecrets(w, cfg.Secrets, home)
			} else {
				fmt.Fprintln(w, "  skipped (schema invalid above)")
			}

			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "Adapter detection (PATH-only)")
			for _, agent := range []struct {
				name string
				bin  string
			}{
				{"claude", "claude"},
				{"opencode", "opencode"},
				{"codex", "codex"},
				{"cursor", "cursor"},
			} {
				p, err := exec.LookPath(agent.bin)
				if err != nil {
					fmt.Fprintf(w, "  %-10s not found in PATH\n", agent.name)
					continue
				}
				fmt.Fprintf(w, "  %-10s %s\n", agent.name, p)
			}

			fmt.Fprintln(w, "")
			if fails > 0 {
				fmt.Fprintf(w, "%d issue(s) detected — fix before running `agentsync apply`\n", fails)
				return fmt.Errorf("doctor: %d issue(s) detected", fails)
			}
			fmt.Fprintln(w, "all checks passed")
			return nil
		},
	}
}

// checkHomeDir verifies that AGENTSYNC_HOME exists and is a directory.
// Returns 1 if the check fails, 0 otherwise.
func checkHomeDir(w io.Writer, home string) int {
	info, err := os.Stat(home)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(w, "  home dir   missing — run `agentsync init`\n")
			return 1
		}
		fmt.Fprintf(w, "  home dir   unreadable: %v\n", err)
		return 1
	}
	if !info.IsDir() {
		fmt.Fprintf(w, "  home dir   exists but is not a directory: %s\n", home)
		return 1
	}
	fmt.Fprintf(w, "  home dir   ok\n")
	return 0
}

// checkStateDir verifies that .state/ is writable.
func checkStateDir(w io.Writer, home string) int {
	stateDir := filepath.Join(home, ".state")
	if info, err := os.Stat(stateDir); err != nil {
		fmt.Fprintf(w, "  .state/    missing — run `agentsync init`\n")
		return 1
	} else if !info.IsDir() {
		fmt.Fprintf(w, "  .state/    exists but is not a directory\n")
		return 1
	}
	probe := filepath.Join(stateDir, ".doctor-write-probe")
	if err := os.WriteFile(probe, []byte{}, 0o600); err != nil {
		fmt.Fprintf(w, "  .state/    not writable: %v\n", err)
		return 1
	}
	_ = os.Remove(probe)
	fmt.Fprintf(w, "  .state/    ok (writable)\n")

	// Verify targets.json parses — the same load status/apply/diff/reconcile
	// do. A corrupt state file makes every real command exit 1, so a readiness
	// check that ignores it would falsely report healthy. A missing file is
	// fine (state.Load returns an empty state on a fresh install).
	if _, err := state.Load(filepath.Join(stateDir, "targets.json")); err != nil {
		fmt.Fprintf(w, "  state file corrupt: %v\n", err)
		return 1
	}
	fmt.Fprintf(w, "  state file ok\n")
	return 0
}

// checkSchema validates agentsync.toml. Returns the parsed Config plus a
// success flag so secrets checks can reuse it.
func checkSchema(w io.Writer, home string) (source.Config, bool) {
	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		fmt.Fprintf(w, "  schema     invalid: %v\n", err)
		return source.Config{}, false
	}
	fmt.Fprintf(w, "  schema     ok (%d mcp, %d plugin(s), %d marketplace(s))\n",
		len(c.MCPServers), len(c.Plugins), len(c.Marketplaces))
	return c.Config, true
}

// checkSecrets validates the [secrets] block: backend present, identity
// file exists with restrictive permissions, recipient set.
func checkSecrets(w io.Writer, cfg source.SecretsConfig, home string) int {
	if cfg.Backend == "" {
		fmt.Fprintf(w, "  backend    not configured (skip — no [secrets] block)\n")
		return 0
	}
	if cfg.Backend != "age" {
		fmt.Fprintf(w, "  backend    unsupported: %q (want \"age\")\n", cfg.Backend)
		return 1
	}
	fmt.Fprintf(w, "  backend    age\n")

	fails := 0
	if cfg.Recipient == "" {
		fmt.Fprintf(w, "  recipient  missing — set [secrets].recipient in agentsync.toml\n")
		fails++
	} else {
		fmt.Fprintf(w, "  recipient  set\n")
	}

	if cfg.IdentityFile == "" {
		fmt.Fprintf(w, "  identity   missing — set [secrets].identity_file in agentsync.toml\n")
		return fails + 1
	}
	// Resolve identity_file the same way apply does (expanding ${env:HOME}/~
	// via paths.HomeDir so it honours AGENTSYNC_TARGET_ROOT), so doctor and
	// apply never disagree on the path.
	userHome := paths.HomeDir(paths.OSEnv{})
	idPath := secrets.ResolveIdentityFile(cfg, home, userHome)
	info, err := os.Stat(idPath)
	if err != nil {
		fmt.Fprintf(w, "  identity   %s — not readable (%v)\n", idPath, err)
		return fails + 1
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(w, "  identity   %s — too permissive (%v); chmod 600\n", idPath, info.Mode().Perm())
		return fails + 1
	}
	fmt.Fprintf(w, "  identity   ok (%s)\n", idPath)

	// Age-encrypted file location — warn if missing (legitimate on a
	// fresh install where the user hasn't called `secrets set` yet).
	agePath := secrets.ResolveAgeFile(cfg, home, userHome)
	if _, err := os.Stat(agePath); err != nil {
		fmt.Fprintf(w, "  age file   %s — not yet created (run `agentsync secrets edit` to author)\n", agePath)
	} else {
		fmt.Fprintf(w, "  age file   %s\n", agePath)
	}
	return fails
}
