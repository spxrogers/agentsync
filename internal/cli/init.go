package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/paths"
)

const initialAgentsyncTOML = `# agentsync source-of-truth config
# See docs/superpowers/specs/2026-05-04-agentsync-design.md for the full schema.

[agents]
# claude   = { enabled = true,  scope = "user" }
# opencode = { enabled = true,  scope = "user" }
# codex    = { enabled = false }   # v1.1
# cursor   = { enabled = false }   # v1.2

[updates]
default_mode     = "track"        # pinned | track | manual
default_interval = "24h"

# [secrets]
# backend       = "age"
# file          = "secrets/secrets.age"
# recipient     = "age1...your-public-key..."
# identity_file = "${env:HOME}/.config/agentsync/age.key"
`

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "scaffold ~/.agentsync/ with empty subdirectories and stub agentsync.toml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			// Explicit-error guard: tell the user the difference between
			// "directory exists with files" (refuse) and "path exists but
			// is unreadable / is a file" (different actionable error).
			entries, err := os.ReadDir(home)
			switch {
			case err == nil && len(entries) > 0:
				return fmt.Errorf("%s already contains files; refusing to overwrite", home)
			case err == nil:
				// Empty directory — fall through and populate.
			case os.IsNotExist(err):
				// Path doesn't exist yet — MkdirAll below creates it.
			default:
				return fmt.Errorf("inspect %s: %w (remove the path or set $AGENTSYNC_HOME to a writable location)", home, err)
			}

			// Every canonical subdirectory the loader recognizes is created up
			// front so `agentsync agent list`, `verify`, and the writer
			// helpers find a populated tree even on an empty install.
			subs := []string{
				"mcp", "marketplaces", "plugins",
				"memory", "memory/fragments",
				"skills", "agents", "commands", "hooks", "lsp",
				".state",
			}
			for _, sub := range subs {
				if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
					return fmt.Errorf("mkdir %s: %w", sub, err)
				}
			}
			// Secrets dir holds the age-encrypted file; restrict to the user
			// even though the file itself is written 0600.
			if err := os.MkdirAll(filepath.Join(home, "secrets"), 0o700); err != nil {
				return fmt.Errorf("mkdir secrets: %w", err)
			}
			if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(initialAgentsyncTOML), 0o644); err != nil {
				return fmt.Errorf("write agentsync.toml: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "agentsync home initialized at", home)
			return nil
		},
	}
}
