package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/ui"
	"golang.org/x/term"
)

// resolveScope determines the effective scope and project root for a command.
// Project scope is always an EXPLICIT opt-in — agentsync never silently acts on
// a project tree just because cwd happens to sit inside one:
//
//   - --project <path>  → project scope rooted at <path>.
//   - --scope project   → project scope; walks up from cwd for a .agentsync/ tree
//     and ERRORS if none is found (never downgrades to user).
//   - --scope user      → user scope.
//   - (no scope flag)   → user scope, UNLESS cwd is inside a project tree, in
//     which case the choice is ambiguous: prompt interactively (no default), or
//     ERROR when non-interactive (--no-input, or stdin is not a TTY).
//
// For every command except init's own scaffolding, project scope requires the
// <root>/.agentsync/ tree to already exist; resolveScope returns an actionable
// error pointing at `agentsync init --scope project` otherwise.
func resolveScope(cmd *cobra.Command, noInput bool) (adapter.Scope, string, error) {
	scopeFlag, projectFlag := scopeFlagValues(cmd)
	return resolveScopeFlags(cmd, scopeFlag, projectFlag, noInput)
}

// resolveScopeFlags is resolveScope over EXPLICIT flag values, for the one
// caller that supplies its own: `explain <path>` infers project scope from the
// destination path when the user named neither flag.
func resolveScopeFlags(cmd *cobra.Command, scopeFlag, projectFlag string, noInput bool) (adapter.Scope, string, error) {
	switch {
	case projectFlag != "":
		// Explicit --project implies project scope, so --scope user alongside it
		// is contradictory — refuse rather than silently pick one.
		if scopeFlag == "user" {
			return adapter.ScopeUser, "", fmt.Errorf("--scope user conflicts with --project (which implies project scope); pass only one")
		}
		abs, err := filepath.Abs(projectFlag)
		if err != nil {
			return adapter.ScopeUser, "", fmt.Errorf("resolve --project path: %w", err)
		}
		if err := requireProjectTree(abs); err != nil {
			return adapter.ScopeUser, "", err
		}
		return adapter.ScopeProject, abs, nil

	case scopeFlag == "user":
		return adapter.ScopeUser, "", nil

	case scopeFlag == "project":
		cwd, err := os.Getwd()
		if err != nil {
			return adapter.ScopeUser, "", fmt.Errorf("getwd: %w", err)
		}
		root, found, derr := discoverProjectTree(cwd)
		if derr != nil {
			return adapter.ScopeUser, "", fmt.Errorf("discover project: %w", derr)
		}
		if !found {
			return adapter.ScopeUser, "", fmt.Errorf(
				"--scope project: no .agentsync/ project tree found at or above %s; "+
					"run `agentsync init --scope project` to create one", cwd,
			)
		}
		return adapter.ScopeProject, root, nil

	case scopeFlag == "":
		cwd, err := os.Getwd()
		if err != nil {
			// Can't inspect cwd — no project tree to detect; plain user scope.
			return adapter.ScopeUser, "", nil //nolint:nilerr // getwd failure degrades to the documented default
		}
		root, found, derr := discoverProjectTree(cwd)
		if derr != nil {
			return adapter.ScopeUser, "", fmt.Errorf("discover project: %w", derr)
		}
		if !found {
			return adapter.ScopeUser, "", nil
		}
		// Ambiguous: cwd is inside a project tree but no scope was requested.
		userHome := paths.AgentsyncHome(paths.OSEnv{})
		if noInput || !stdinIsTerminal(cmd) {
			return adapter.ScopeUser, "", fmt.Errorf(
				"a .agentsync/ project tree was detected at %s but no scope was given; "+
					"re-run with --scope project (apply it here) or --scope user (apply your user config) "+
					"— cannot prompt (non-interactive)", root,
			)
		}
		return promptScopeChoice(cmd, root, userHome)

	default:
		return adapter.ScopeUser, "", fmt.Errorf("unknown --scope value %q; want user or project", scopeFlag)
	}
}

// requireProjectTree errors unless <root>/.agentsync/ exists, so a non-init
// command never proceeds against a project root that was never scaffolded.
func requireProjectTree(root string) error {
	home := project.Home(root)
	fi, err := os.Stat(home)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("no .agentsync/ project tree at %s; "+
			"run `agentsync init --scope project --project %s` to create one", home, root)
	}
	return nil
}

// noInputFlag reads the inherited global --no-input flag. When set, ambiguous
// scope resolution fails closed instead of prompting (for headless scripts).
func noInputFlag(cmd *cobra.Command) bool {
	if v, err := cmd.Flags().GetBool("no-input"); err == nil {
		return v
	}
	if f := cmd.InheritedFlags().Lookup("no-input"); f != nil {
		return f.Value.String() == "true"
	}
	return false
}

// stdinIsTerminal reports whether the command's stdin is an interactive
// terminal (so a prompt would actually reach a human). A pipe, file, or the
// test harness's string reader is not — which routes scripts to the
// fail-closed path instead of a hung Read.
func stdinIsTerminal(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// promptScopeChoice asks the user to pick project vs user scope when cwd is
// inside a project tree and neither --scope nor --project was given. There is no
// default — an empty or invalid line re-prompts — so the choice is always a
// deliberate keystroke.
func promptScopeChoice(cmd *cobra.Command, projectRoot, userHome string) (adapter.Scope, string, error) {
	// The menu goes to STDERR, never stdout: scope is resolved before a command
	// like `status --json`/`diff --json` produces its payload, so writing the
	// prompt to stdout would corrupt the machine-readable output a caller is
	// piping. stdin is still read from InOrStdin so the keystroke reaches us.
	w := cmd.ErrOrStderr()
	p := printerOn(cmd, w)
	r := bufio.NewReader(cmd.InOrStdin())
	p.Fdiagf(w, ui.LevelInfo, "this repo has a .agentsync/ project tree.")
	p.Fdetailf(w, "[1] project scope (%s)", projectRoot)
	p.Fdetailf(w, "[2] user scope (%s)", userHome)
	for attempts := 0; attempts < 5; attempts++ {
		fmt.Fprintf(w, "run `agentsync %s` at which scope? [1/2]: ", cmd.Name())
		line, err := r.ReadString('\n')
		switch strings.TrimSpace(line) {
		case "1":
			return adapter.ScopeProject, projectRoot, nil
		case "2":
			return adapter.ScopeUser, "", nil
		}
		if err != nil {
			// EOF / closed stdin with no valid choice — don't loop forever.
			return adapter.ScopeUser, "", fmt.Errorf("no scope selected (input closed); pass --scope user|project")
		}
		p.Fdetailf(w, "please enter 1 (project) or 2 (user).")
	}
	return adapter.ScopeUser, "", fmt.Errorf("no valid scope selected after 5 attempts; pass --scope user|project")
}

// discoverProjectTree walks up from cwd for a <root>/.agentsync/ tree, but skips
// the user's OWN canonical home: ~/.agentsync/ is itself a .agentsync/ directory,
// so running from inside it (or from $HOME) must NOT be mistaken for a project —
// that would silently flip every command to project scope and stop writing the
// user-scope destinations. When the nearest match IS the user home, the search
// continues above it for a genuine project ancestor.
func discoverProjectTree(cwd string) (string, bool, error) {
	agentsyncHome := paths.AgentsyncHome(paths.OSEnv{})
	dir := cwd
	for {
		root, found, err := project.Discover(dir)
		if err != nil || !found {
			return "", false, err
		}
		if !sameDir(project.Home(root), agentsyncHome) {
			return root, true, nil
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", false, nil
		}
		dir = parent
	}
}

// sameDir reports whether a and b name the same directory, comparing cleaned
// absolute paths and (best-effort) their symlink-resolved forms so a symlinked
// temp root (macOS /tmp → /private/tmp) doesn't cause a false mismatch.
func sameDir(a, b string) bool {
	ca, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	cb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	ca, cb = filepath.Clean(ca), filepath.Clean(cb)
	if ca == cb {
		return true
	}
	if ra, rerr := filepath.EvalSymlinks(ca); rerr == nil {
		ca = ra
	}
	if rb, rerr := filepath.EvalSymlinks(cb); rerr == nil {
		cb = rb
	}
	return ca == cb
}
