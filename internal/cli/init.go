package cli

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
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
		Use:   "init [<git-url>]",
		Short: "scaffold ~/.agentsync/ (or clone an existing source repo)",
		Long: `init scaffolds ~/.agentsync/ with the canonical subdirectory layout and
a stub agentsync.toml. If a git URL is supplied, init clones that repo
into ~/.agentsync/ instead of scaffolding — for bootstrapping a new
machine from an existing source-of-truth repo (typically chezmoi-
managed).

The destination must not exist or be empty; init refuses to overwrite
a populated home so a careless re-run on a working install does not
nuke it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			if err := guardEmptyHome(home); err != nil {
				return err
			}
			if len(args) == 1 {
				return cloneSourceRepo(cmd, home, args[0])
			}
			return scaffoldHome(cmd, home)
		},
	}
}

// guardEmptyHome refuses to operate when home already contains files.
// Missing path is fine — caller's MkdirAll / clone will create it.
func guardEmptyHome(home string) error {
	entries, err := os.ReadDir(home)
	switch {
	case err == nil && len(entries) > 0:
		return fmt.Errorf("%s already contains files; refusing to overwrite", home)
	case err == nil:
		return nil
	case os.IsNotExist(err):
		return nil
	default:
		return fmt.Errorf("inspect %s: %w (remove the path or set $AGENTSYNC_HOME to a writable location)", home, err)
	}
}

// scaffoldHome populates an empty home directory with the canonical
// subdirectory layout and a stub agentsync.toml.
func scaffoldHome(cmd *cobra.Command, home string) error {
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
	if err := os.MkdirAll(filepath.Join(home, "secrets"), 0o700); err != nil {
		return fmt.Errorf("mkdir secrets: %w", err)
	}
	if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(initialAgentsyncTOML), 0o644); err != nil {
		return fmt.Errorf("write agentsync.toml: %w", err)
	}
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "agentsync home initialized at", home)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Next steps:")
	fmt.Fprintln(w, "  1. agentsync agent add claude        # register an agent")
	fmt.Fprintln(w, "  2. agentsync mcp add github ...      # author an MCP server")
	fmt.Fprintln(w, "  3. agentsync apply --dry-run         # preview before writing")
	fmt.Fprintln(w, "  4. agentsync apply                   # write to agent destinations")
	return nil
}

// cloneSourceRepo clones rawURL into home. The URL must use https://,
// ssh://, git+ssh://, or file:// — plain http and git:// are rejected
// because the clone target is the user's canonical config.
// After clone, `.state/` is created (gitignored on the source side, but
// the directory must exist for later commands).
func cloneSourceRepo(cmd *cobra.Command, home, rawURL string) error {
	if err := validateCloneURL(rawURL); err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "cloning %s into %s ...\n", rawURL, home)
	if _, err := git.PlainClone(home, false, &git.CloneOptions{URL: rawURL, Depth: 1}); err != nil {
		return fmt.Errorf("clone %s: %w", rawURL, err)
	}
	// .state/ is always local — never tracked by the source repo.
	if err := os.MkdirAll(filepath.Join(home, ".state"), 0o755); err != nil {
		return fmt.Errorf("mkdir .state: %w", err)
	}
	fmt.Fprintln(w, "cloned. Run `agentsync apply --dry-run` to preview against this machine.")
	return nil
}

// validateCloneURL rejects unsafe URL schemes for the source-repo
// bootstrap (where unrestricted user execution flows are likely).
func validateCloneURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("git URL is empty")
	}
	if strings.HasPrefix(raw, "git@") {
		// scp-style ssh URL — go-git accepts these.
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse %q: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "ssh", "git+ssh", "file":
		return nil
	case "http", "git":
		if os.Getenv("AGENTSYNC_ALLOW_INSECURE_URLS") == "1" {
			return nil
		}
		return fmt.Errorf("init URL %q uses insecure scheme %q; "+
			"set AGENTSYNC_ALLOW_INSECURE_URLS=1 to override", raw, u.Scheme)
	default:
		return fmt.Errorf("init URL %q has unsupported scheme %q (want https/ssh/file)", raw, u.Scheme)
	}
}
