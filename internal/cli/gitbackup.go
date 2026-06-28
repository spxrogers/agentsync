package cli

import (
	"bufio"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	agit "github.com/spxrogers/agentsync/internal/git"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
)

// runDestinationGitBackup checkpoints each user-scope agent dir that received
// managed writes this apply, giving it a local-only git history so a bad apply is
// revertible (issue #118). It is BEST-EFFORT by contract: the files are already
// written and state is already saved, so a git failure here is reported but never
// fails the apply. It is a no-op for project scope, --no-git-backup, mode "off",
// an empty write set, or an agent dir under foreign source control.
func runDestinationGitBackup(
	cmd *cobra.Command, p *ui.Printer, reg *adapter.Registry, agents []string,
	sc adapter.Scope, projectRoot, home string,
	cfg source.DestinationGitBackupConfig, written map[string]bool, noGitBackup bool,
) error {
	if sc != adapter.ScopeUser || noGitBackup {
		return nil
	}
	mode := cfg.EffectiveMode()
	if mode == source.GitBackupModeOff {
		return nil
	}
	id := agit.Identity{Name: cfg.AuthorName, Email: cfg.AuthorEmail}

	// The unit of versioning is the DIRECTORY, not the agent: union every enabled
	// agent's declared version roots, drop nested roots (no repo inside a repo), and
	// de-dup, so a shared dir (e.g. ~/.agents/skills, written by Codex + several
	// breadth agents) is checkpointed exactly once with all its files.
	roots := enabledVersionRoots(reg, agents, sc, projectRoot)

	hintedUnavailable := false
	for _, root := range roots {
		rels := managedRelsUnder(root, written)

		st, err := agit.Detect(root)
		if err != nil {
			fmt.Fprintf(p.Err, "%s git backup: %v\n", p.Yellow("agentsync:"), err)
			continue
		}

		var repo *agit.Repo
		switch st {
		case agit.StateForeign:
			// The user already source-controls this dir — stay out of their way.
			if len(rels) > 0 {
				fmt.Fprintf(p.Err, "%s %s is under your own source control; skipping agentsync git backup.\n",
					p.Faint(ui.GlyphInfo), root)
			}
			continue
		case agit.StateAgentsyncOwned:
			// Open even when nothing was written under this root: a delete-only apply
			// (a managed file removed, nothing added) still needs its checkpoint.
			repo, err = agit.Open(root)
		case agit.StateUntracked:
			if len(rels) == 0 {
				continue // nothing written here and no existing repo to record into
			}
			repo, err = ensureUntrackedRepo(cmd, p, root, home, &mode, &hintedUnavailable)
		}
		if err != nil {
			fmt.Fprintf(p.Err, "%s git backup for %s: %v\n", p.Yellow("agentsync:"), root, err)
			continue
		}
		if repo == nil {
			continue // declined / unavailable / now off — nothing to commit into
		}

		// Stage the written files + the notice (so a freshly-inited repo tracks it,
		// a no-op on an already-owned repo). Build a fresh slice — don't append onto
		// rels' backing array.
		toStage := make([]string, 0, len(rels)+1)
		toStage = append(toStage, rels...)
		toStage = append(toStage, agit.NoticeFile)
		if err := repo.Stage(toStage); err != nil {
			fmt.Fprintf(p.Err, "%s git backup for %s: %v\n", p.Yellow("agentsync:"), root, err)
			continue
		}
		deleted, err := repo.StageTrackedDeletions()
		if err != nil {
			fmt.Fprintf(p.Err, "%s git backup for %s: %v\n", p.Yellow("agentsync:"), root, err)
			continue
		}
		staged := dedupeSorted(append(toStage, deleted...))
		msg := checkpointMessage(root, staged)
		h, err := repo.CommitStaged(msg, id)
		if err != nil {
			fmt.Fprintf(p.Err, "%s git backup for %s: %v\n", p.Yellow("agentsync:"), root, err)
			continue
		}
		if h != "" {
			fmt.Fprintf(p.Err, "%s %s\n", p.Faint(ui.GlyphInfo),
				p.Faint(fmt.Sprintf("git backup: checkpointed %s", root)))
		}
	}
	return nil
}

// enabledVersionRoots returns the de-duplicated, de-nested, sorted set of
// version-root directories declared by the given agents at the scope. The unit is
// the directory: a shared dir declared by several agents appears once, and a dir
// nested under another (e.g. ~/.claude/skills under ~/.claude) is dropped in favor
// of the ancestor — so agentsync never creates a repo inside another repo.
func enabledVersionRoots(reg *adapter.Registry, agents []string, sc adapter.Scope, project string) []string {
	seen := map[string]bool{}
	var all []string
	for _, name := range agents {
		ad := reg.Lookup(name)
		if ad == nil {
			continue
		}
		vd, ok := ad.(adapter.VersionedDirs)
		if !ok {
			continue
		}
		for _, r := range vd.VersionRoots(sc, project) {
			if r == "" {
				continue
			}
			c := filepath.Clean(r)
			if !seen[c] {
				seen[c] = true
				all = append(all, c)
			}
		}
	}
	return denestRoots(all)
}

// versionRootOwners maps each post-de-nest version root to the sorted set of
// agents whose declared dirs land under it. A root with more than one owner is
// SHARED (e.g. ~/.agents/skills ← codex + warp + …) — reverting it rolls back every
// owner's files, which the revert path warns about.
func versionRootOwners(reg *adapter.Registry, agents []string, sc adapter.Scope, project string) map[string][]string {
	roots := enabledVersionRoots(reg, agents, sc, project)
	owners := map[string]map[string]bool{}
	for _, name := range agents {
		ad := reg.Lookup(name)
		if ad == nil {
			continue
		}
		vd, ok := ad.(adapter.VersionedDirs)
		if !ok {
			continue
		}
		for _, r := range vd.VersionRoots(sc, project) {
			if r == "" {
				continue
			}
			c := filepath.Clean(r)
			for _, root := range roots {
				if isUnderDir(c, root) {
					if owners[root] == nil {
						owners[root] = map[string]bool{}
					}
					owners[root][name] = true
					break
				}
			}
		}
	}
	out := make(map[string][]string, len(owners))
	for root, set := range owners {
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		out[root] = names
	}
	return out
}

// denestRoots returns roots sorted, with any root nested under another removed.
// Lexical sort places an ancestor before its descendants (the ancestor path is a
// prefix), so a single forward pass keeping non-nested roots is sufficient.
func denestRoots(roots []string) []string {
	sort.Strings(roots)
	var kept []string
	for _, r := range roots {
		nested := false
		for _, k := range kept {
			if isUnderDir(r, k) {
				nested = true
				break
			}
		}
		if !nested {
			kept = append(kept, r)
		}
	}
	return kept
}

// isUnderDir reports whether child is the same as, or nested under, parent.
func isUnderDir(child, parent string) bool {
	if child == parent {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ensureUntrackedRepo returns a repo to commit into for an untracked dir, honoring
// the mode: "on" inits silently; "prompt" asks (opt-out) and persists the answer.
// Returns (nil, nil) when init was declined or could not be offered. It may flip
// *mode to "off" for the rest of this run when the user picks "don't ask again".
func ensureUntrackedRepo(cmd *cobra.Command, p *ui.Printer, dir, home string, mode *string, hintedUnavailable *bool) (*agit.Repo, error) {
	switch *mode {
	case source.GitBackupModeOn:
		return initGuarded(p, dir)
	case source.GitBackupModePrompt:
		switch gitBackupPrompter(cmd, p, dir) {
		case promptYes:
			repo, err := initGuarded(p, dir)
			if err != nil {
				return nil, err
			}
			// Sticky "yes": stop asking on future applies (even if THIS dir was
			// skipped for a nesting conflict — other dirs should still auto-init).
			if perr := setDestinationGitBackupMode(home, source.GitBackupModeOn); perr != nil {
				fmt.Fprintf(p.Err, "%s could not persist git-backup mode: %v\n", p.Yellow("agentsync:"), perr)
			}
			*mode = source.GitBackupModeOn
			return repo, nil
		case promptNever:
			if perr := setDestinationGitBackupMode(home, source.GitBackupModeOff); perr != nil {
				fmt.Fprintf(p.Err, "%s could not persist git-backup mode: %v\n", p.Yellow("agentsync:"), perr)
			}
			*mode = source.GitBackupModeOff
			fmt.Fprintf(p.Err, "%s destination git backup disabled; re-enable later by setting mode in agentsync.toml.\n", p.Faint(ui.GlyphInfo))
			return nil, nil
		case promptUnavailable:
			if !*hintedUnavailable {
				fmt.Fprintf(p.Err, "%s destination git backup is available — run `agentsync apply` interactively to enable it, "+
					"or set [destination_directory_git_backup] mode = \"on\" in agentsync.toml.\n", p.Faint(ui.GlyphInfo))
				*hintedUnavailable = true
			}
			return nil, nil
		default: // promptNo
			return nil, nil
		}
	}
	return nil, nil // mode "off" reached here only if flipped mid-run
}

// initGuarded inits an agentsync repo at dir UNLESS dir already contains a nested
// git repo below it — agentsync never creates a repo that would wrap another (the
// cross-run nesting hazard: a child dir was versioned in an earlier run, before a
// parent-dir agent was enabled). Returns (nil, nil) and warns when it skips.
func initGuarded(p *ui.Printer, dir string) (*agit.Repo, error) {
	nested, err := agit.HasNestedRepoBelow(dir)
	if err != nil {
		return nil, err
	}
	if nested {
		fmt.Fprintf(p.Err, "%s %s contains a nested git repository; skipping agentsync git backup here "+
			"to avoid a repo inside a repo (remove the inner .git or reconcile to merge).\n",
			p.Yellow("agentsync:"), dir)
		return nil, nil
	}
	return agit.Init(dir)
}

// managedRelsUnder returns the slash-relative paths of every written file that
// lives under dir, sorted.
func managedRelsUnder(dir string, written map[string]bool) []string {
	var rels []string
	for abs := range written {
		rel, err := filepath.Rel(dir, abs)
		if err != nil {
			continue
		}
		if rel == "." || strings.HasPrefix(rel, "..") {
			continue // not under dir
		}
		rels = append(rels, filepath.ToSlash(rel))
	}
	sort.Strings(rels)
	return rels
}

// dedupeSorted returns the unique, sorted elements of in.
func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return in
	}
	sort.Strings(in)
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// checkpointMessage renders the per-apply commit message for a version root.
func checkpointMessage(root string, staged []string) string {
	return fmt.Sprintf("agentsync apply: %s — %d file(s)\n\n%s",
		root, len(staged), strings.Join(staged, "\n"))
}

// promptResult is the outcome of the opt-out git-init prompt.
type promptResult int

const (
	promptYes         promptResult = iota // init + persist mode "on"
	promptNo                              // skip this run, ask again next time
	promptNever                           // persist mode "off", stop asking
	promptUnavailable                     // no TTY / --no-input: caller hints + skips
)

// gitBackupPrompter is the interactive prompt, injectable so tests can drive the
// yes/no/never branches without a real terminal.
var gitBackupPrompter = promptInitGitBackup

// promptInitGitBackup asks the user whether to start git-versioning an untracked
// destination dir. It fails closed (promptUnavailable) when there is no terminal
// or --no-input is set, so headless runs never block.
func promptInitGitBackup(cmd *cobra.Command, p *ui.Printer, dir string) promptResult {
	if noInputFlag(cmd) || !stdinIsTerminal(cmd) {
		return promptUnavailable
	}
	w := cmd.OutOrStdout()
	r := bufio.NewReader(cmd.InOrStdin())
	fmt.Fprintf(w, "%s agentsync can keep a local rollback history of %s so a bad apply is revertible.\n", ui.GlyphInfo, dir)
	fmt.Fprintf(w, "  This is a %s git repo (never pushed). It may contain secrets in cleartext, like the files it versions.\n", p.Bold("local-only"))
	for attempts := 0; attempts < 5; attempts++ {
		fmt.Fprintf(w, "  Enable git backup for this directory? [y]es / [n]ot now / [d]on't ask again: ")
		line, err := r.ReadString('\n')
		if res, ok := interpretPromptLine(line); ok {
			return res
		}
		if err != nil {
			return promptNo // EOF / closed stdin: treat as "not now", never loop forever
		}
		fmt.Fprintf(w, "  please enter y, n, or d.\n")
	}
	return promptNo
}

// interpretPromptLine maps a typed line to a promptResult; ok is false for an
// unrecognized line (the caller re-prompts).
func interpretPromptLine(line string) (promptResult, bool) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return promptYes, true
	case "n", "no":
		return promptNo, true
	case "d", "dont", "don't":
		return promptNever, true
	}
	return promptNo, false
}
