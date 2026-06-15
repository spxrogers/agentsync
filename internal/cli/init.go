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
	"github.com/spxrogers/agentsync/internal/project"
)

const initialAgentsyncTOML = `# agentsync source-of-truth config
# Schema reference: https://github.com/spxrogers/agentsync#readme

[agents]
# Deep adapters (richest coverage):
# claude   = { enabled = true,  scope = "user" }
# opencode = { enabled = true,  scope = "user" }
# codex    = { enabled = true,  scope = "user" }
# cursor   = { enabled = true,  scope = "user" }
# gemini   = { enabled = true,  scope = "user" }
# continue = { enabled = true,  scope = "user" }
# windsurf = { enabled = true,  scope = "user" }
# roo      = { enabled = true,  scope = "user" }
# cline    = { enabled = true,  scope = "user" }
# …plus a breadth tier (memory + MCP) for amp, goose, qwen, warp, zed, kiro,
# kilocode, junie, factory, copilot, crush, and more — see the capability matrix.
# Run "agentsync agent add <name>" for any agent in "agentsync agent list --all".

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
	var scopeFlag, projectFlag string
	cmd := &cobra.Command{
		Use:   "init [<git-url>]",
		Short: "scaffold ~/.agentsync/ (user) or <repo>/.agentsync/ (project)",
		Long: `init scaffolds a canonical source tree with the standard subdirectory
layout and a stub agentsync.toml.

At user scope (the default) it targets ~/.agentsync/. If a git URL is
supplied, init clones that repo into ~/.agentsync/ instead of scaffolding —
for bootstrapping a new machine from an existing source-of-truth repo
(typically chezmoi-managed).

With --scope project (or --project <path>) it scaffolds <path>/.agentsync/
— a project-local source tree, with the same layout as ~/.agentsync/, that
you commit to the repo to share project-scoped agent config with
collaborators. Apply/import/status/diff auto-detect it by walking up from
the current directory. A git-URL clone is only supported at user scope.

The destination must not exist or be empty; init refuses to overwrite a
populated home so a careless re-run on a working install does not nuke it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectMode, projectRoot, err := initProjectRoot(scopeFlag, projectFlag)
			if err != nil {
				return err
			}
			if projectMode {
				if len(args) == 1 {
					return fmt.Errorf("init <git-url> clone is only supported at user scope; "+
						"project scope scaffolds a %s/ tree", project.DirName)
				}
				return scaffoldProjectHome(cmd, projectRoot)
			}
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
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: user)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project)")
	return cmd
}

// initProjectRoot resolves whether init runs at project scope and, if so, the
// project root whose <root>/.agentsync/ tree to scaffold. Unlike apply, init does
// NOT auto-detect an existing project tree from cwd: scaffolding is an explicit
// act, so project scope requires --scope project or --project <path>.
func initProjectRoot(scopeFlag, projectFlag string) (projectMode bool, root string, err error) {
	if projectFlag != "" {
		if scopeFlag == "user" {
			return false, "", fmt.Errorf("--scope user conflicts with --project (which implies project scope); pass only one")
		}
		abs, aerr := filepath.Abs(projectFlag)
		if aerr != nil {
			return false, "", fmt.Errorf("resolve --project path: %w", aerr)
		}
		return true, abs, nil
	}
	switch scopeFlag {
	case "", "user":
		return false, "", nil
	case "project":
		cwd, cerr := os.Getwd()
		if cerr != nil {
			return false, "", fmt.Errorf("getwd: %w", cerr)
		}
		return true, cwd, nil
	default:
		return false, "", fmt.Errorf("unknown --scope value %q; want user or project", scopeFlag)
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
	if err := ensureStateGitignore(home); err != nil {
		return err
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

const initialProjectAgentsyncTOML = `# agentsync project-scope config
# Rendered into <repo>/.claude/, <repo>/.opencode/, <repo>/.codex/, … by
# 'agentsync apply' when run inside this repo. Commit this .agentsync/ tree to
# share project-scoped agent config with collaborators.

[agents]
# Leave this empty to inherit the agents you enabled at user scope, or pin a
# subset for this project:
# claude   = { enabled = true }
# opencode = { enabled = true }

# Author project components by adding files under this tree (mcp/<id>.toml,
# skills/<name>/SKILL.md, agents/<name>.md, commands/<name>.md, hooks/<event>.toml,
# lsp/<id>.toml, memory/AGENTS.md) or capture existing native config with
# 'agentsync import <agent> --scope project'.
`

// scaffoldProjectHome populates an empty <root>/.agentsync/ project source tree
// with the canonical subdirectory layout and a project-scope stub agentsync.toml.
// Unlike the user-scope home it has no .state/ (apply records state centrally in
// ~/.agentsync/.state, keyed by project root) and no .state .gitignore, so the
// committed tree carries only the source the user authors.
func scaffoldProjectHome(cmd *cobra.Command, root string) error {
	home := project.Home(root)
	if err := guardEmptyHome(home); err != nil {
		return err
	}
	subs := []string{
		"mcp", "lsp", "skills", "agents", "commands", "hooks",
		"memory", "memory/fragments",
	}
	for _, sub := range subs {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, "secrets"), 0o700); err != nil {
		return fmt.Errorf("mkdir secrets: %w", err)
	}
	if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(initialProjectAgentsyncTOML), 0o644); err != nil {
		return fmt.Errorf("write agentsync.toml: %w", err)
	}
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "agentsync project source initialized at", home)
	// A stray retired single-file marker would be silently ignored now; nudge
	// the user to fold its settings into the new tree and delete it.
	if fi, err := os.Stat(filepath.Join(root, project.LegacyMarkerFile)); err == nil && !fi.IsDir() {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"agentsync: a legacy %s is present at %s; it is no longer read — move its settings into %s/ and delete it.\n",
			project.LegacyMarkerFile, root, project.DirName)
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Next steps:")
	fmt.Fprintln(w, "  1. add project components under", home)
	fmt.Fprintf(w, "     (or: agentsync import claude --scope project --project %s)\n", root)
	fmt.Fprintf(w, "  2. agentsync apply --project %s --dry-run   # preview\n", root)
	fmt.Fprintf(w, "  3. agentsync apply --project %s             # write project destinations\n", root)
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
	if err := ensureStateGitignore(home); err != nil {
		return err
	}
	fmt.Fprintln(w, "cloned. Run `agentsync apply --dry-run` to preview against this machine.")
	return nil
}

// ensureStateGitignore guarantees ~/.agentsync/.gitignore excludes /.state/.
// .state/ holds local-only state (targets.json) and .state/backups/<ts>/ —
// verbatim copies of pre-existing native config files that routinely contain
// API tokens. The README recommends syncing ~/.agentsync via chezmoi/git, so
// without this a user would commit plaintext credential backups. Idempotent:
// appends the rule if a .gitignore already exists (e.g. a cloned source repo)
// and doesn't already ignore .state/.
func ensureStateGitignore(home string) error {
	const rule = "/.state/"
	p := filepath.Join(home, ".gitignore")
	data, err := os.ReadFile(p)
	switch {
	case err == nil:
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == rule {
				return nil // already ignored
			}
		}
		out := string(data)
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += rule + "\n"
		if werr := os.WriteFile(p, []byte(out), 0o644); werr != nil {
			return fmt.Errorf("update .gitignore: %w", werr)
		}
		return nil
	case os.IsNotExist(err):
		body := "# agentsync: local state + plaintext config backups — never commit.\n" + rule + "\n"
		if werr := os.WriteFile(p, []byte(body), 0o644); werr != nil {
			return fmt.Errorf("write .gitignore: %w", werr)
		}
		return nil
	default:
		return fmt.Errorf("read .gitignore: %w", err)
	}
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
