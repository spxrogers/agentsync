package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

func newVerifyCmd() *cobra.Command {
	var (
		scopeFlag   string
		projectFlag string
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "schema-lint the canonical source and validate secrets resolvability on demand",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fs := afero.NewOsFs()
			// home is the USER agentsync home. It is both the user-scope source
			// root AND the anchor for relative secrets paths (identity_file /
			// file) at EVERY scope: project trees inherit the user's [secrets]
			// config and resolve against the same vault, exactly as apply does, so
			// verify and apply never disagree on which key/file to read.
			home := paths.AgentsyncHome(paths.OSEnv{})
			userHome := paths.HomeDir(paths.OSEnv{})

			// Resolve scope ONCE — resolveScope may prompt interactively when cwd
			// sits inside a project tree and no scope was given, so calling it
			// twice (here and again inside a shared loader) would prompt twice.
			sc, projectRoot, err := resolveScope(cmd, scopeFlag, projectFlag, noInputFlag(cmd))
			if err != nil {
				return err
			}

			// The source root the init/half-init guards below check: the project
			// tree at project scope, the user home otherwise. resolveScope already
			// guarantees a project tree's .agentsync/ DIRECTORY exists; the guards
			// add the agentsync.toml check so verify never reports a false "ok" on
			// a half-initialized tree (source.Load tolerates a missing config).
			sourceRoot := home
			if sc == adapter.ScopeProject {
				sourceRoot = project.Home(projectRoot)
			}
			if err := requireInitializedSource(sourceRoot, sc); err != nil {
				return err
			}

			// Load the source for this scope: the user base, plus the project
			// overlay at project scope so the project's ${secret:…}/${env:…}
			// references resolve against the inherited user [secrets] config —
			// mirroring apply's merged render. verify deliberately does NOT project
			// marketplace plugins; it validates the source the user authors.
			c, err := source.Load(fs, home)
			if err != nil {
				return fmt.Errorf("verify: %w", err)
			}
			if sc == adapter.ScopeProject {
				pc, perr := source.Load(fs, sourceRoot)
				if perr != nil {
					return fmt.Errorf("verify: load project source %s: %w", sourceRoot, perr)
				}
				// verify lints the source a project-scope apply would consume, so
				// it must reject the same undeclared-agents state apply rejects.
				if err := requireProjectAgents(pc, sourceRoot); err != nil {
					return err
				}
				c = project.Merge(c, pc)
			}
			for name := range c.Config.Agents {
				if err := validateAgent(name); err != nil {
					// %q, not %s: name is a config-derived [agents.<name>] TOML key
					// and a quoted key can carry raw ESC/bidi bytes; %q escapes them
					// so a shared config can't inject terminal escapes here (matches
					// the agent-name print convention in agent.go/status.go/revert.go).
					return fmt.Errorf("agents.%q: %w", name, err)
				}
			}
			if err := verifySecrets(c.Config.Secrets, home); err != nil {
				return fmt.Errorf("verify secrets: %w", err)
			}
			// Reference checking. Online (default) resolves every ${secret:…}/
			// ${env:…} against the live backends. Offline (AGENTSYNC_ALLOW_OFFLINE_VERIFY=1,
			// the documented CI path without an age key) can't RESOLVE, but it still
			// validates reference SHAPE — flagging a malformed ref (${secret:} empty
			// key, missing colon, illegal char) that the strict resolver would
			// otherwise silently pass through as literal text. It does NOT catch a
			// well-shaped-but-wrong/unresolvable name (a typo'd key, a missing vault
			// entry); that requires running WITHOUT the offline flag. (issue #171)
			offline := os.Getenv("AGENTSYNC_ALLOW_OFFLINE_VERIFY") == "1"
			if offline {
				if bad := secrets.MalformedSecretRefs(&c); len(bad) > 0 {
					// bad is []untrusted.Text — untrusted.Join sanitizes each token
					// on display, so a config-crafted ESC/control byte in a malformed
					// ref can't inject terminal escapes here (issue #171 / #93).
					return fmt.Errorf("verify: malformed secret/env reference(s): %s "+
						"(offline mode checks reference shape only; run without "+
						"AGENTSYNC_ALLOW_OFFLINE_VERIFY=1 to also check resolvability)", untrusted.Join(bad, ", "))
				}
			} else {
				secBackend := secrets.SelectBackend(c.Config.Secrets, home, userHome)
				envBackend := secrets.EnvBackend{}
				if _, err := secrets.SubstituteCanonical(c, secBackend, envBackend); err != nil {
					return fmt.Errorf("verify ${secret:}/${env:} resolution: %w", err)
				}
			}
			if offline {
				fmt.Fprintln(cmd.OutOrStdout(), "ok: schema valid; reference shapes valid (offline — resolvability not checked)")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "ok: schema valid; all references resolve")
			}
			return nil
		},
	}
	addScopeFlags(cmd, &scopeFlag, &projectFlag)
	return cmd
}

// requireInitializedSource fails verify with an actionable error when the source
// root for the chosen scope is absent or half-initialized (present but missing
// agentsync.toml). source.Load tolerates both and returns an empty model, so
// without this guard verify would print a false "ok: schema valid" on a tree
// that was never set up. The message points at the scope-appropriate `init`.
func requireInitializedSource(root string, sc adapter.Scope) error {
	label := "agentsync home"
	initCmd := "`agentsync init`"
	if sc == adapter.ScopeProject {
		label = "project source tree"
		initCmd = "`agentsync init --scope project`"
	}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s %s does not exist; run %s first", label, root, initCmd)
		}
		return fmt.Errorf("verify: stat %s: %w", root, err)
	}
	if _, err := os.Stat(filepath.Join(root, "agentsync.toml")); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s %s is missing agentsync.toml (half-initialized); run %s", label, root, initCmd)
		}
		return fmt.Errorf("verify: stat agentsync.toml: %w", err)
	}
	return nil
}

// verifySecrets validates the [secrets] block beyond the schema decode:
// recipient and identity_file are required when backend is age, the
// identity file must be readable, and on POSIX the file must not be
// world- or group-readable.
func verifySecrets(cfg source.SecretsConfig, home string) error {
	if cfg.Backend == "" || cfg.Backend == "env" {
		return nil
	}
	if cfg.Backend != "age" {
		return fmt.Errorf("backend %q not supported (want \"age\" or \"env\")", cfg.Backend)
	}
	if cfg.Recipient == "" {
		return fmt.Errorf("[secrets].recipient is required for backend = \"age\"")
	}
	if cfg.IdentityFile == "" {
		return fmt.Errorf("[secrets].identity_file is required for backend = \"age\"")
	}
	userHome := paths.HomeDir(paths.OSEnv{})
	// Resolve identity_file exactly as apply does (SelectBackend ->
	// ResolveIdentityFile) so verify and apply never disagree on the path.
	idPath := secrets.ResolveIdentityFile(cfg, home, userHome)
	if _, err := os.Stat(idPath); err != nil {
		return fmt.Errorf("identity_file %s: %w", idPath, err)
	}
	// Use the same permission check apply uses — it honours
	// AGENTSYNC_AGE_SKIP_PERM_CHECK=1, which the previous inline
	// runtime.GOOS check did not. Apply and verify must agree on
	// what "secure" means or users will end up in a config where
	// verify refuses but apply works (or vice versa).
	if err := secrets.CheckIdentityPermissions(idPath); err != nil {
		return err
	}
	// Age file location is optional in config (defaults to DefaultAgeFile)
	// and may not exist yet on a brand-new install. Resolve it the same way
	// apply does, then only flag a present-but-unreadable file.
	agePath := secrets.ResolveAgeFile(cfg, home, userHome)
	if _, err := os.Stat(agePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("secrets.file %s: %w", agePath, err)
	}
	return nil
}
