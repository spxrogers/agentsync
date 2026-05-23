package cli

import (
	"fmt"
	"os"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "schema-lint ~/.agentsync/ and validate secrets resolvability on demand",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			// source.Load tolerates a missing home and returns an empty model,
			// so without this guard `verify` on an uninitialized machine
			// printed a false "ok" and exited 0. Fail with a pointer to init.
			if _, err := os.Stat(home); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("agentsync home %s does not exist; run `agentsync init` first", home)
				}
				return fmt.Errorf("verify: stat %s: %w", home, err)
			}
			c, err := source.Load(afero.NewOsFs(), home)
			if err != nil {
				return fmt.Errorf("verify: %w", err)
			}
			for name := range c.Config.Agents {
				if err := validateAgent(name); err != nil {
					return fmt.Errorf("agents.%s: %w", name, err)
				}
			}
			if err := verifySecrets(c.Config.Secrets, home); err != nil {
				return fmt.Errorf("verify secrets: %w", err)
			}
			// Substitute against the live backends. AGENTSYNC_ALLOW_OFFLINE_VERIFY=1
			// skips resolution when running in CI without an age key.
			// Offline mode cannot catch a typo'd secret name (e.g.
			// ${secret:GITHB.token}) — that requires a live backend.
			// The regex inside SubstituteRefs already enforces the
			// reference shape, so the schema decode + this pass cover
			// the offline-validatable space.
			if os.Getenv("AGENTSYNC_ALLOW_OFFLINE_VERIFY") != "1" {
				secBackend := secrets.SelectBackend(c.Config.Secrets, home, paths.HomeDir(paths.OSEnv{}))
				envBackend := secrets.EnvBackend{}
				if err := secrets.SubstituteCanonical(&c, secBackend, envBackend); err != nil {
					return fmt.Errorf("verify ${secret:}/${env:} resolution: %w", err)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ok: schema valid; all references resolve")
			return nil
		},
	}
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
