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
			if entries, _ := os.ReadDir(home); len(entries) > 0 {
				return fmt.Errorf("%s already contains files; refusing to overwrite", home)
			}

			for _, sub := range []string{"mcp", "marketplaces", "plugins", "memory", "memory/fragments", "skills", "secrets", ".state"} {
				if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
					return fmt.Errorf("mkdir %s: %w", sub, err)
				}
			}
			if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(initialAgentsyncTOML), 0o644); err != nil {
				return fmt.Errorf("write agentsync.toml: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "agentsync home initialized at", home)
			return nil
		},
	}
}
