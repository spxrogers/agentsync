package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	agit "github.com/spxrogers/agentsync/internal/git"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
)

// gitBackupSession carries the per-run state the PRE-APPLY baseline pass and the
// POST-APPLY checkpoint pass MUST share so they never diverge (issue #143). A
// "yes"/"never" answer to the init prompt, the one-time "backup available" hint, and
// the foreign/nested skip decisions are taken once and honored by both passes: both
// select the SAME de-nested version roots and resolve each root through the SAME
// logic (resolveBackupRepo), so a dir the baseline inits is already agentsync-owned
// by the time the checkpoint pass opens it, and a dir the user declined (or a foreign
// dir) is remembered rather than prompted/hinted twice.
type gitBackupSession struct {
	cmd  *cobra.Command
	p    *ui.Printer
	home string
	id   agit.Identity
	mode string
	// roots is the union of every enabled agent's declared version roots, de-nested
	// (no repo inside a repo) and de-duped — the DIRECTORY is the unit of versioning,
	// so a shared dir (e.g. ~/.agents/skills, written by Codex + several breadth
	// agents) is versioned exactly once.
	roots             []string
	hintedUnavailable bool
	// warnedCleartext gates the one-time-per-run cleartext notices — the mode=on
	// auto-init notice AND the baseline-snapshot caution — so an apply warns about
	// the local-only cleartext-secret history once, not once per dir, and a fresh
	// init that already warned suppresses the baseline caution for the same run.
	warnedCleartext bool
	// handled records roots whose init/prompt (or foreign-skip hint) decision has
	// already been made THIS run, so the baseline and checkpoint passes never
	// double-prompt or double-hint the same dir.
	handled map[string]bool
}

// gitPermWarn returns the callback EnsureDirSecure reports through — a loosened
// .git-history warning to stderr with the existing agentsync yellow prefix.
func (s *gitBackupSession) gitPermWarn() func(string) {
	return func(msg string) {
		fmt.Fprintf(s.p.Err, "%s %s\n", s.p.Yellow("agentsync:"), msg)
	}
}

// newGitBackupSession builds the shared session, or returns nil when git backup does
// nothing this run: project scope, --no-git-backup, or mode "off". A nil session's
// baseline/checkpoint methods are safe no-ops, so callers need not branch.
func newGitBackupSession(
	cmd *cobra.Command, p *ui.Printer, reg *adapter.Registry, agents []string,
	sc adapter.Scope, projectRoot, home string,
	cfg source.DestinationGitBackupConfig, noGitBackup bool,
) *gitBackupSession {
	if sc != adapter.ScopeUser || noGitBackup {
		return nil
	}
	mode := cfg.EffectiveMode()
	if mode == source.GitBackupModeOff {
		return nil
	}
	return &gitBackupSession{
		cmd:     cmd,
		p:       p,
		home:    home,
		id:      agit.Identity{Name: cfg.AuthorName, Email: cfg.AuthorEmail},
		mode:    mode,
		roots:   enabledVersionRoots(reg, agents, sc, projectRoot),
		handled: map[string]bool{},
	}
}

// resolveBackupRepo resolves the agentsync repo to checkpoint into for one version
// root, applying every skip the baseline and checkpoint passes share: a foreign dir
// is left untouched (with a one-time hint when managed writes land in it), an
// untracked dir with no managed writes is skipped, and an untracked dir is inited
// under the mode/prompt policy (initGuarded refuses a nesting hazard). hasWrites
// reports whether managed files land under this root this run. Returns (nil, nil) on
// a clean skip. The init/prompt/foreign-hint decision for a given dir is taken at
// most once per run (handled) so the two passes never double-prompt or double-hint.
func (s *gitBackupSession) resolveBackupRepo(root string, st agit.State, hasWrites bool) (*agit.Repo, error) {
	switch st {
	case agit.StateForeign:
		// The user already source-controls this dir — stay out of their way.
		if hasWrites && !s.handled[root] {
			s.handled[root] = true
			fmt.Fprintf(s.p.Err, "%s %s is under your own source control; skipping agentsync git backup.\n",
				s.p.Faint(ui.GlyphInfo), root)
		}
		return nil, nil
	case agit.StateAgentsyncOwned:
		// Open even when nothing was written under this root: a delete-only apply
		// (a managed file removed, nothing added) still needs its checkpoint.
		return agit.Open(root)
	case agit.StateUntracked:
		if !hasWrites || s.handled[root] {
			return nil, nil // nothing to record, or the decision was already made this run
		}
		s.handled[root] = true
		return ensureUntrackedRepo(s.cmd, s.p, root, s.home, &s.mode, &s.hintedUnavailable, &s.warnedCleartext)
	}
	return nil, nil
}

// baseline records a PRE-APPLY baseline checkpoint of every version root this apply
// will touch, BEFORE render.Apply overwrites anything — so even the FIRST apply
// into a dir is revertible (the apply checkpoint's parent is the genuine pre-apply
// state), not just the second apply onward (issue #143). planned is the set of
// destination paths the plan will write OR delete (including writer-derived skill
// orphan deletes — see baselinePaths): a to-be-deleted file's pre-apply bytes must
// land in the baseline, because the writer removes an in-sync orphan without a
// backup and a deletion can't be recovered from any later checkpoint. This pass
// manages exactly the roots the post-apply checkpoint pass would.
//
// A root with NO planned path under it is skipped outright: there is no imminent
// overwrite for a baseline to protect, and — because resolveBackupRepo opens an
// agentsync-owned root unconditionally (the checkpoint pass needs that for
// delete-only applies) — snapshotting there would grow "pre-apply baseline"
// commits in dirs the apply never touches whenever they hold uncommitted tracked
// drift. That drift stays on disk exactly as a --no-git-backup run would leave
// it; revert's own snapshot still preserves it if a revert ever runs there.
//
// When a baseline snapshot actually commits something, the issue #126 cleartext
// caution is printed once per run (cautionCleartextOnce): the snapshot commits
// freshly hand-typed dest edits verbatim, the same exposure the revert snapshot
// warns about.
//
// FAILURE POLICY — best-effort WITH A LOUD WARNING. Unlike the post-apply checkpoint
// (best-effort-silent, because the files are already written), the baseline runs
// BEFORE the destructive write, so a failure means the first apply will be
// unrecoverable. It still NEVER aborts the apply — but the user is clearly told the
// first apply to that dir will not be revertible. Foreign dirs (the user's own source
// control) and mode/scope skips are honored exactly as the checkpoint pass honors
// them, and a declined prompt does not block apply.
func (s *gitBackupSession) baseline(planned map[string]bool) {
	if s == nil {
		return
	}
	for _, root := range s.roots {
		rels := managedRelsUnder(root, planned)
		if len(rels) == 0 {
			continue // no planned write/delete under this root — nothing to protect (see doc comment)
		}
		st, err := agit.Detect(root)
		if err != nil {
			fmt.Fprintf(s.p.Err, "%s git baseline: %v\n", s.p.Yellow("agentsync:"), err)
			continue
		}
		repo, err := s.resolveBackupRepo(root, st, true)
		if err != nil {
			s.warnFirstApplyUnrevertible(root, err)
			continue
		}
		if repo == nil {
			// A foreign dir is the user's own repo (not inited by design) — its
			// existing "under your own source control" hint already fired, so this is
			// not our failure to warn about. Any OTHER skip of a write-bound dir (a
			// nesting hazard, a declined prompt) means its first apply is unrevertible.
			if st != agit.StateForeign {
				s.warnFirstApplyUnrevertible(root, nil)
			}
			continue
		}
		var (
			h    string
			cerr error
		)
		if st == agit.StateUntracked {
			// Freshly inited: stage the NOTICE plus the PRE-APPLY content of only the
			// paths this apply will manage — deliberately NOT the whole dir. Sweeping
			// the whole dir (git add -A) would durably commit unrelated sensitive
			// siblings (an agent's credentials file, conversation transcripts) into the
			// local-only history, enlarging the secret-history surface far beyond the
			// resolved-config model the "harden secret-history" guard is scoped to.
			// #128 already preserves untracked, non-managed siblings across a revert
			// (Restore never enumerates them), so they survive WITHOUT being versioned:
			// pre-existing non-managed content is OUT of the recoverability contract
			// (preserved, not versioned) — the alternative issue #143 explicitly permits.
			// The NOTICE guarantees a non-empty baseline even when every managed path is
			// new, so a revert still has a target that removes exactly what this apply
			// creates.
			toStage := []string{agit.NoticeFile}
			for _, rel := range rels {
				if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); statErr == nil {
					toStage = append(toStage, rel) // capture a managed path's pre-apply content
				}
			}
			if serr := repo.Stage(toStage); serr != nil {
				s.warnFirstApplyUnrevertible(root, serr)
				continue
			}
			h, cerr = repo.CommitStaged(baselineMessage(root), s.id)
		} else {
			// Already-owned (a later apply): history already makes this dir revertible
			// for TRACKED content, so the baseline snapshots uncommitted tracked drift
			// plus the pre-apply bytes of any PLANNED path that is still untracked
			// (created while backup was off or declined — this apply is about to
			// overwrite or delete bytes history has never seen). It must NOT sweep in
			// the user's other untracked files (#128), which git add -A would.
			// SnapshotPreApply is a no-op on a clean worktree with no untracked planned
			// paths, so a re-apply with no drift records no spurious baseline commit.
			h, cerr = repo.SnapshotPreApply(baselineMessage(root), s.id, rels)
		}
		if cerr != nil {
			s.warnFirstApplyUnrevertible(root, cerr)
			continue
		}
		if h != "" {
			fmt.Fprintf(s.p.Err, "%s %s\n", s.p.Faint(ui.GlyphInfo),
				s.p.Faint(fmt.Sprintf("git backup: pre-apply baseline of %s", root)))
			s.cautionCleartextOnce()
		}
	}
}

// cautionCleartextOnce prints the issue #126 cleartext caution the first time
// THIS RUN's baseline pass durably commits dest content into the local-only
// history — the same caution the revert snapshot prints. The baseline commits
// freshly hand-typed dest edits (and the pre-apply bytes of planned paths)
// verbatim, so a secret typed into a dest file since the last apply lands in the
// history; without this the identical exposure the revert path warns about
// happened silently on apply. Gated by the same warnedCleartext bool as the
// mode="on" auto-init notice, so an apply cautions once per run — not once per
// root, and not a second time when a fresh init already warned.
func (s *gitBackupSession) cautionCleartextOnce() {
	if s.warnedCleartext {
		return
	}
	s.warnedCleartext = true
	fmt.Fprintf(s.p.Err, "%s that baseline is kept in the local-only history and, like the files it versions, may contain secrets in cleartext.\n",
		s.p.Faint(ui.GlyphInfo))
}

// warnFirstApplyUnrevertible tells the user, loudly, that a pre-apply baseline could
// not be taken for root, so the first apply to it is not recoverable — the deliberate,
// documented failure policy of the baseline pass (it warns but never aborts apply).
func (s *gitBackupSession) warnFirstApplyUnrevertible(root string, err error) {
	if err != nil {
		fmt.Fprintf(s.p.Err, "%s could not take a pre-apply baseline of %s (%v); the first apply to it will not be revertible.\n",
			s.p.Yellow("agentsync:"), root, err)
		return
	}
	fmt.Fprintf(s.p.Err, "%s could not take a pre-apply baseline of %s; the first apply to it will not be revertible.\n",
		s.p.Yellow("agentsync:"), root)
}

// checkpoint records the POST-APPLY checkpoint for each version root that received
// managed writes (or a managed deletion) this run — the second commit after a first
// apply, whose parent is the pre-apply baseline. It is BEST-EFFORT by contract: the
// files are already written and state is already saved, so a git failure here is
// reported (never returned) and never fails the apply. It is a no-op for an empty write
// set or an agent dir under foreign source control.
//
// It always returns nil today (every per-root failure is warned-and-continued). The
// error return is retained deliberately: the test-facing wrapper runDestinationGitBackup
// exposes it, and keeping the shape lets a future non-best-effort caller surface a hard
// failure without a signature break across the wrapper and its test callers.
func (s *gitBackupSession) checkpoint(written map[string]bool) error {
	if s == nil {
		return nil
	}
	for _, root := range s.roots {
		rels := managedRelsUnder(root, written)

		st, err := agit.Detect(root)
		if err != nil {
			fmt.Fprintf(s.p.Err, "%s git backup: %v\n", s.p.Yellow("agentsync:"), err)
			continue
		}
		repo, err := s.resolveBackupRepo(root, st, len(rels) > 0)
		if err != nil {
			fmt.Fprintf(s.p.Err, "%s git backup for %s: %v\n", s.p.Yellow("agentsync:"), root, err)
			continue
		}
		if repo == nil {
			continue // foreign / declined / unavailable / nothing written — nothing to commit into
		}
		// Re-assert the at-rest control on every apply's commit path (issue #126): the
		// history holds cleartext-resolved secrets and receives fresh secret blobs each
		// apply, so a .git dir whose perms drifted looser than 0o700 is best-effort
		// re-tightened and surfaced — covering both already-owned repos and ones just
		// inited (whose init-time chmod may have silently failed). Best-effort; never
		// aborts the apply.
		repo.EnsureDirSecure(s.gitPermWarn())
		if st == agit.StateAgentsyncOwned {
			// Re-probe: a foreign repo may have been cloned below this owned root since
			// init. The append-only checkpoint below is harmless, but warn now so the
			// user learns `revert` here is unsafe BEFORE they ever run it (revertRoot
			// then refuses/skips the same case).
			warnNestedForeignRepoOnCommit(s.p, root)
		}

		// Per-root cost, and its short-circuit. Without one, every root runs
		// repo.Stage (one wt.Add per written file, each an internal full-tree git
		// status) + StageTrackedDeletions (one status) + CommitStaged→indexClean
		// (one status) — O(roots × (writtenFiles × status + 2 status)) on EVERY
		// apply, even a clean re-apply that changed nothing. Short-circuit an
		// agentsync-owned root that WROTE nothing this run: stage any tracked
		// deletions first (the only change a delete-only apply carries under this
		// root), and if there are none either, skip the Stage(notice)+CommitStaged
		// round-trip entirely — a no-write, no-delete root produces no checkpoint
		// anyway (CommitStaged returns "" on a clean index), so nothing is lost.
		deleted, err := repo.StageTrackedDeletions()
		if err != nil {
			fmt.Fprintf(s.p.Err, "%s git backup for %s: %v\n", s.p.Yellow("agentsync:"), root, err)
			continue
		}
		if len(rels) == 0 && len(deleted) == 0 {
			continue // nothing written and nothing deleted under this root
		}
		// Stage the written files + the notice (so a freshly-inited repo tracks it,
		// a no-op on an already-owned repo). Build a fresh slice — don't append onto
		// rels' backing array.
		toStage := make([]string, 0, len(rels)+1)
		toStage = append(toStage, rels...)
		toStage = append(toStage, agit.NoticeFile)
		if err := repo.Stage(toStage); err != nil {
			fmt.Fprintf(s.p.Err, "%s git backup for %s: %v\n", s.p.Yellow("agentsync:"), root, err)
			continue
		}
		staged := dedupeSorted(append(toStage, deleted...))
		msg := checkpointMessage(root, staged)
		h, err := repo.CommitStaged(msg, s.id)
		if err != nil {
			fmt.Fprintf(s.p.Err, "%s git backup for %s: %v\n", s.p.Yellow("agentsync:"), root, err)
			continue
		}
		if h != "" {
			fmt.Fprintf(s.p.Err, "%s %s\n", s.p.Faint(ui.GlyphInfo),
				s.p.Faint(fmt.Sprintf("git backup: checkpointed %s", root)))
		}
	}
	return nil
}

// runDestinationGitBackup is the standalone POST-APPLY checkpoint pass. The pre-apply
// baseline is taken separately in apply (via gitBackupSession.baseline) so the two
// passes share one session; this thin wrapper is kept so focused tests can drive the
// checkpoint pass directly. It is a no-op for project scope, --no-git-backup, mode
// "off", an empty write set, or an agent dir under foreign source control.
func runDestinationGitBackup(
	cmd *cobra.Command, p *ui.Printer, reg *adapter.Registry, agents []string,
	sc adapter.Scope, projectRoot, home string,
	cfg source.DestinationGitBackupConfig, written map[string]bool, noGitBackup bool,
) error {
	return newGitBackupSession(cmd, p, reg, agents, sc, projectRoot, home, cfg, noGitBackup).checkpoint(written)
}

// baselineMessage renders the commit subject for a pre-apply baseline checkpoint.
func baselineMessage(root string) string {
	return fmt.Sprintf("agentsync apply: pre-apply baseline of %s", root)
}

// enabledVersionRoots returns the de-duplicated, de-nested, sorted set of
// version-root directories declared by the given agents at the scope. The unit is
// the directory: a shared dir declared by several agents appears once, and a dir
// nested under another (e.g. ~/.claude/skills under ~/.claude) is dropped in favor
// of the ancestor — so agentsync never creates a repo inside another repo.
//
// DEDUP CASING (nit, issue #175): the seen[] key is the BYTE-EXACT cleaned path
// (filepath.Clean), so on a case-insensitive filesystem two roots differing only in
// case would be treated as distinct and could both be inited. That is safe today
// because every version root is a hardcoded string literal with fixed casing (the
// deep adapters' Paths + generic.versionRootOf), so no two roots ever differ only in
// case — the dedup relies on that. If a future adapter derives a root from a
// case-varying source, this key would need case-folding on case-insensitive FSes.
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

// ownersFor returns the cross-agent owner set for a version root by boundary-correct
// match. The owners map is keyed by the GLOBALLY de-nested root set, so an agent's
// OWN de-nested root can be a child that was parent-folded away in that map (e.g.
// OpenCode's ~/.claude/skills folds into Claude's ~/.claude). An exact-key miss
// therefore falls back to the owners of the nearest ANCESTOR key (deepest key that
// contains root, via isUnderDir), never a byte prefix. This keeps the shared-dir
// blast-radius warning intact for parent-folded roots (issue #154).
func ownersFor(owners map[string][]string, root string) []string {
	if o, ok := owners[root]; ok {
		return o
	}
	best := ""
	for k := range owners {
		if isUnderDir(root, k) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return nil
	}
	return owners[best]
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
//
// warnedCleartext gates the mode="on" cleartext notice: unlike the interactive prompt
// (which already warns), mode="on" auto-inits with no caution, so an unattended apply
// would grow its cleartext-secret git footprint across new dirs silently. The notice
// fires once per run (the first NEW dir it actually inits), never on a declined /
// nested-skip (nil repo) init.
func ensureUntrackedRepo(cmd *cobra.Command, p *ui.Printer, dir, home string, mode *string, hintedUnavailable, warnedCleartext *bool) (*agit.Repo, error) {
	switch *mode {
	case source.GitBackupModeOn:
		repo, err := initGuarded(p, dir)
		if err == nil && repo != nil && !*warnedCleartext {
			fmt.Fprintf(p.Err, "%s started a %s git backup of %s; like the files it versions, its history may contain secrets in cleartext (it is never pushed).\n",
				p.Faint(ui.GlyphInfo), p.Bold("local-only"), dir)
			*warnedCleartext = true
		}
		return repo, err
	case source.GitBackupModePrompt:
		switch gitBackupPrompter(cmd, p, dir) {
		case promptYes:
			repo, err := initGuarded(p, dir)
			if err != nil {
				return nil, err
			}
			// Sticky "yes": stop asking on future applies (even if THIS dir was
			// skipped for a nesting conflict — other dirs should still auto-init).
			if repo == nil {
				// initGuarded skipped THIS dir for a nesting conflict (it already
				// warned why), yet the user said "yes" — surface that the global
				// switch is flipping on regardless, so the persisted mode=on below
				// is not a silent change.
				fmt.Fprintf(p.Err, "%s this directory was skipped, but auto-backup is now enabled for future directories and applies.\n",
					p.Faint(ui.GlyphInfo))
			}
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
	// Defensive fallback: load-time validation (source.loadConfig →
	// validateMode) now rejects any mode outside {"", prompt, on, off}, so an
	// invalid value never reaches here; "off" is the only remaining case,
	// reachable only if flipped mid-run.
	return nil, nil
}

// initGuarded inits an agentsync repo at dir UNLESS dir already contains a nested
// git repo below it — agentsync never creates a repo that would wrap another (the
// cross-run nesting hazard: a child dir was versioned in an earlier run, before a
// parent-dir agent was enabled). Returns (nil, nil) and warns when it skips.
//
// The nesting probe (agit.HasNestedRepoBelow) is a full-subtree WalkDir that
// short-circuits only on finding a nested `.git`; for a version root that collapses
// to a busy IDE/app dir this is a one-time full-tree walk on first init — a bounded
// but potentially large apply-tail cost. See HasNestedRepoBelow's COST note for why
// it is left unbounded.
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

// warnNestedForeignRepoOnCommit warns when a foreign git repo now lives below an
// agentsync-owned root (cloned there after init). The commit path is non-destructive
// (append-only), so this is a warning, not a refusal — but agentsync's clean ownership
// of the root no longer holds, so `agentsync revert` of this root will now refuse (as a
// precaution) until the nesting is resolved; the user is told before ever running it.
// Best-effort: a scan error is swallowed (a git-backup step must never fail the apply
// over an unreadable subtree).
func warnNestedForeignRepoOnCommit(p *ui.Printer, root string) {
	nested, err := agit.HasNestedRepoBelow(root)
	if err != nil || !nested {
		return
	}
	fmt.Fprintf(p.Err, "%s a nested git repository now lives below the agentsync-owned dir %s; "+
		"`agentsync revert` of this root will refuse as a precaution until the inner repo is "+
		"removed or reconciled.\n",
		p.Yellow("agentsync:"), root)
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
