package git

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// FileChange describes one file a restore would touch, for `revert --dry-run`.
type FileChange struct {
	Path string
	// Kind is "create" | "modify" | "delete" — the three merkletrie actions Plan
	// maps. "unknown" is a defensive label for a future/unexpected go-git action
	// that is neither insert nor delete nor modify (not produced today); like a
	// modify it is applied by writing the target tree's content.
	Kind string
}

// Plan computes what Restore(targetRev) would change in the worktree relative to
// the current HEAD, WITHOUT writing anything. Returns the resolved target hash and
// the file changes (sorted by path).
func (r *Repo) Plan(targetRev string) (targetHash string, changes []FileChange, err error) {
	targetStr, err := r.Resolve(targetRev)
	if err != nil {
		return "", nil, err
	}
	headH, err := r.headHash()
	if err != nil {
		return "", nil, err
	}
	headTree, err := r.commitTree(headH)
	if err != nil {
		return "", nil, err
	}
	targetTree, err := r.commitTree(plumbing.NewHash(targetStr))
	if err != nil {
		return "", nil, err
	}
	diff, err := headTree.Diff(targetTree)
	if err != nil {
		return "", nil, fmt.Errorf("diffing checkpoints in %s: %w", r.dir, err)
	}
	var out []FileChange
	for _, ch := range diff {
		action, aerr := ch.Action()
		if aerr != nil {
			return "", nil, fmt.Errorf("classifying change in %s: %w", r.dir, aerr)
		}
		switch action {
		case merkletrie.Insert:
			out = append(out, FileChange{Path: ch.To.Name, Kind: "create"})
		case merkletrie.Delete:
			out = append(out, FileChange{Path: ch.From.Name, Kind: "delete"})
		case merkletrie.Modify:
			out = append(out, FileChange{Path: ch.To.Name, Kind: "modify"})
		default:
			// merkletrie only defines Insert/Delete/Modify; a value here means a
			// new go-git action we don't yet handle — label it distinctly rather
			// than silently masquerading it as a modify.
			out = append(out, FileChange{Path: ch.To.Name, Kind: "unknown"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return targetStr, out, nil
}

// snapshotMessage is the commit subject Restore uses when it snapshots uncommitted
// tracked edits before resetting. Kept stable so the CLI (and tests) can recognise
// the safety checkpoint in history.
const snapshotMessage = "agentsync revert: snapshot uncommitted changes before revert"

// Restore makes the worktree's TRACKED files match the targetRev checkpoint and
// records the result as a NEW commit on top of the current HEAD. It is append-only:
// HEAD advances, nothing is rewritten or lost, so the bad apply stays in history and
// the revert is itself revertible. Returns the new commit hash plus the snapshot hash
// (below), or ("", snapshot, nil) when the worktree already matches target (no
// checkpoint recorded).
//
// SAFE-BY-CONSTRUCTION for uncommitted TRACKED edits: Restore FIRST snapshots any
// uncommitted changes to already-tracked files into history (via SnapshotDirtyTracked)
// so the delta-apply below cannot lose them — EVERY caller is protected, not only the
// CLI wrapper, so the "nothing is rewritten or lost" promise holds of the WORKTREE and
// is no longer contingent on the caller. The returned snapshotHash is that commit ("" when
// the worktree had no tracked changes); callers surface it so the user can recover the
// preserved edits. The target is resolved to a concrete hash BEFORE snapshotting, so a
// relative targetRev like "HEAD~1" stays correct even though the snapshot advances HEAD.
//
// It applies ONLY the tracked HEAD↔target delta (the FileChanges from Plan) to the
// worktree — it deliberately does NOT use go-git's HardReset. Unlike `git reset
// --hard`, go-git's HardReset enumerates and DELETES every untracked and gitignored
// worktree file; a revert sold as safe rollback must never destroy the user's own
// scratch files. Because only the diffed paths are touched, any untracked or
// gitignored file the user dropped into the dir survives byte-for-byte and stays
// untracked.
//
// If a failure strikes AFTER the snapshot advanced HEAD (mid delta-apply or at the
// final commit), the returned error carries a recovery hint naming the pre-revert HEAD
// and the snapshot commit (restoreFailureHint) so a half-applied worktree has a clear
// path back. A pre-flight refusal mutates no WORKTREE file and returns its own
// actionable error without that hint — but the safety snapshot above may already
// have advanced HEAD (it runs before the pre-flight, deliberately: refusing must
// never cost the user their uncommitted edits), so callers surface snapshotHash
// even on error.
func (r *Repo) Restore(targetRev, message string, id Identity) (revertHash, snapshotHash string, err error) {
	// Resolve the target to a CONCRETE hash up front — the snapshot below advances
	// HEAD, after which a relative ref like "HEAD~1" would resolve elsewhere.
	targetStr, err := r.Resolve(targetRev)
	if err != nil {
		return "", "", err
	}
	// Remember the pre-revert HEAD so a partial-failure hint can name it for recovery.
	origHash, err := r.headHash()
	if err != nil {
		return "", "", err
	}
	orig := origHash.String()
	// Snapshot uncommitted TRACKED edits so the delta-apply can't lose them; untracked
	// files are never touched (#128's contract, preserved below).
	snapshotHash, err = r.SnapshotDirtyTracked(snapshotMessage, id)
	if err != nil {
		return "", "", err
	}
	_, changes, err := r.Plan(targetStr)
	if err != nil {
		return "", snapshotHash, err
	}
	if len(changes) == 0 {
		return "", snapshotHash, nil
	}
	targetTree, err := r.commitTree(plumbing.NewHash(targetStr))
	if err != nil {
		return "", snapshotHash, err
	}
	wt, err := r.repo.Worktree()
	if err != nil {
		return "", snapshotHash, fmt.Errorf("worktree for %s: %w", r.dir, err)
	}
	// Pre-flight the STRUCTURAL conflicts a create/modify can't resolve without
	// destroying a file agentsync doesn't manage, and refuse BEFORE mutating the
	// worktree — so no unmanaged file is ever deleted and these conflicts fail up
	// front rather than half-way through the delete pass. We inspect the worktree
	// filesystem directly (NOT git status, which omits gitignored files):
	//   (1) a path that is a file in the target but CURRENTLY a directory whose
	//       contents aren't all being deleted (it holds untracked/gitignored files)
	//       — replacing it with a file would delete them;
	//  (1b) a CREATE path that is currently occupied by any non-directory entry —
	//       a regular file, a symlink to one, or a DANGLING symlink — since a
	//       create means HEAD doesn't track this path, that entry is the user's own
	//       (untracked/gitignored); overwriting + committing it would violate the
	//       untouched-files promise. os.Lstat (not os.Stat) so a symlink is seen as
	//       itself: a dangling link Stat-errors and would slip past both arms, only
	//       for restoreFileFromTree to silently Remove it and write over it;
	//   (2) a create whose ancestor is an existing unmanaged FILE — MkdirAll would
	//       fail over it.
	// A non-transactional restore can't guarantee zero partial state for every
	// possible I/O error, but any staged-but-uncommitted delete is git-recoverable
	// and no unmanaged file is ever lost. restoreFileFromTree keeps a defensive
	// backstop for the same dir->file case.
	deleting := make(map[string]bool, len(changes))
	for _, ch := range changes {
		if ch.Kind == "delete" {
			deleting[ch.Path] = true
		}
	}
	for _, ch := range changes {
		if ch.Kind == "delete" {
			// Deletes get the ancestor-symlink check only (below): removing a
			// tracked path THROUGH a symlinked parent would resolve outside the
			// repo and destroy the user's file at the link target. The leaf
			// checks don't apply — the leaf itself is tracked content.
			if parent, isLink := symlinkAncestor(r.dir, ch.Path); isLink {
				return "", snapshotHash, fmt.Errorf("cannot restore: %q would be deleted through the symlink %q agentsync does not manage; move or remove it, then re-run revert", ch.Path, parent)
			}
			continue
		}
		abs := filepath.Join(r.dir, filepath.FromSlash(ch.Path))
		// Lstat, not Stat: a symlink at the path — even a DANGLING one, which Stat
		// reports as nonexistent — is a user-owned entry the restore must refuse to
		// destroy, not something to silently delete and overwrite.
		info, statErr := os.Lstat(abs)
		switch {
		case statErr == nil && info.IsDir():
			blocker, werr := firstUnmanagedEntryUnder(r.dir, abs, deleting)
			if werr != nil {
				return "", snapshotHash, fmt.Errorf("scanning %s during revert in %s: %w", ch.Path, r.dir, werr)
			}
			if blocker != "" {
				return "", snapshotHash, fmt.Errorf("cannot restore %q to a file: the directory holds entries agentsync does not manage (e.g. %q); move or remove them, then re-run revert", ch.Path, blocker)
			}
		case statErr == nil && ch.Kind == "create":
			// (1b) a create over an existing regular file: it is the user's own
			// untracked/gitignored file (HEAD doesn't track this path) — refuse
			// rather than overwrite and commit it.
			return "", snapshotHash, fmt.Errorf("cannot restore %q: a file agentsync does not manage already exists there; move or remove it, then re-run revert", ch.Path)
		}
		if parent, isLink := symlinkAncestor(r.dir, ch.Path); isLink {
			// Lstat-based: a SYMLINKED ancestor Stat-reports as a real
			// directory, and billy's chroot fs doesn't resolve symlinks either —
			// so writing "through" it would land managed content (including
			// resolved cleartext secrets) OUTSIDE the repo, at wherever the
			// user's link points. Refuse it like every other structural conflict.
			return "", snapshotHash, fmt.Errorf("cannot restore %q: its parent %q is a symlink agentsync does not manage — restoring through it would write outside the backup; move or remove it, then re-run revert", ch.Path, parent)
		}
		for parent := path.Dir(ch.Path); parent != "." && parent != "/"; parent = path.Dir(parent) {
			pinfo, statErr := os.Lstat(filepath.Join(r.dir, filepath.FromSlash(parent)))
			if statErr != nil {
				continue // doesn't exist yet — MkdirAll will create it
			}
			if pinfo.IsDir() {
				break // a real directory — fine
			}
			if !deleting[parent] {
				return "", snapshotHash, fmt.Errorf("cannot restore %q: its parent %q is a file agentsync does not manage; move or remove it, then re-run revert", ch.Path, parent)
			}
			break // parent is a file being deleted — the delete pass clears it first
		}
	}
	// Apply the delta, then commit. Any failure here is AFTER the snapshot advanced
	// HEAD, so wrap it with a recovery hint naming orig + the snapshot commit.
	if err := r.applyRestoreDelta(wt, targetTree, changes); err != nil {
		return "", snapshotHash, restoreFailureHint(r.dir, orig, snapshotHash, err)
	}
	sig := signature(id)
	h, err := wt.Commit(message, &gogit.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		return "", snapshotHash, restoreFailureHint(r.dir, orig, snapshotHash,
			fmt.Errorf("recording revert commit in %s: %w", r.dir, err))
	}
	return h.String(), snapshotHash, nil
}

// applyRestoreDelta applies the tracked HEAD↔target delta to the worktree path-by-path
// and stages each change. HEAD never moves here, so the commit in Restore is parented
// on the CURRENT HEAD automatically — the snapshot commit when one was taken, otherwise
// the pre-revert HEAD — and the intervening commits (and the bad apply) stay reachable,
// keeping revert append-only.
//
// Deletes are applied in a FIRST pass, before any create/modify, so a path that swaps
// between a file and a directory across the two checkpoints cannot abort mid-restore.
// E.g. going back to a target where "a" is a file while HEAD has "a/b": the diff is
// {delete "a/b", create "a"}; removing "a/b" first empties dir "a" so restoreFileFromTree
// can replace it with the file "a". The reverse (delete file "a", then create "a/b")
// likewise needs the delete first so MkdirAll("a") succeeds. Delete and create paths are
// otherwise disjoint (a tree diff never both deletes and creates the same path), so the
// two passes are order-independent within themselves. Between the passes,
// pruneEmptiedDirs clears the directories the deletes emptied (wt.Remove leaves them
// behind, unlike `git reset --hard`) without ever touching a dir that still has entries.
func (r *Repo) applyRestoreDelta(wt *gogit.Worktree, targetTree *object.Tree, changes []FileChange) error {
	for _, ch := range changes {
		if ch.Kind != "delete" {
			continue
		}
		// Mutation-time ancestor re-check (TOCTOU): the pre-flight already
		// refused symlinked ancestors, but a link planted BETWEEN pre-flight
		// and this pass would still redirect the syscall outside the repo —
		// the prune got exactly this defense-in-depth gate, the mutating
		// passes need it too. A mid-restore refusal here is wrapped by
		// restoreFailureHint upstream, so the user still gets a recovery path.
		if parent, isLink := symlinkAncestor(r.dir, ch.Path); isLink {
			return fmt.Errorf("refusing to remove %s during revert in %s: its parent %q became a symlink mid-restore", ch.Path, r.dir, parent)
		}
		// wt.Remove both deletes the worktree file and stages the deletion.
		if _, err := wt.Remove(ch.Path); err != nil {
			return fmt.Errorf("removing %s during revert in %s: %w", ch.Path, r.dir, err)
		}
	}
	pruneEmptiedDirs(wt, changes)
	for _, ch := range changes {
		if ch.Kind == "delete" {
			continue // "create" | "modify"
		}
		// Same mutation-time re-check for the write path: without it, managed
		// content (resolved cleartext secrets included) would land wherever a
		// freshly planted ancestor link points.
		if parent, isLink := symlinkAncestor(r.dir, ch.Path); isLink {
			return fmt.Errorf("refusing to restore %s during revert in %s: its parent %q became a symlink mid-restore", ch.Path, r.dir, parent)
		}
		if err := restoreFileFromTree(wt, targetTree, ch.Path); err != nil {
			return fmt.Errorf("restoring %s during revert in %s: %w", ch.Path, r.dir, err)
		}
		// wt.Add re-READS the path: a symlink raced in between the write above
		// and this staging would stage outside-repo bytes into the local revert
		// commit. Re-verify the leaf is still the regular file we just wrote.
		// (The remaining un-closable window is the Lstat->syscall gap inside
		// each check — closing it needs openat2/O_NOFOLLOW dirfds go-git does
		// not expose; that is the PR's sole accepted TOCTOU residual.)
		if info, lerr := wt.Filesystem.Lstat(ch.Path); lerr != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to stage %s during revert in %s: the just-restored file changed underneath the revert (lstat err=%v)", ch.Path, r.dir, lerr)
		}
		if _, err := wt.Add(ch.Path); err != nil {
			return fmt.Errorf("staging %s during revert in %s: %w", ch.Path, r.dir, err)
		}
	}
	return nil
}

// pruneEmptiedDirs removes parent directories the delete pass left EMPTY. go-git's
// wt.Remove deletes only the file itself — unlike `git reset --hard`, which also
// clears the directories a delete empties — so reverting away skill/deep/SKILL.md
// would otherwise strand skill/ and skill/deep/ as empty dirs. Only ancestors of the
// delta's own deleted paths are considered: each deleted file's parent chain is
// pruned upward, stopping at the repo root and at the FIRST directory that still has
// entries (an untracked user file in the same dir keeps its whole chain alive — a
// dir with entries is NEVER removed). Best-effort by design: git does not track
// directories, so a stray empty dir can never make the restored tree wrong, and a
// readdir/remove hiccup must not fail a revert whose tracked delta already applied.
// It runs BEFORE the create pass, so a dir it prunes that the target repopulates is
// simply recreated by restoreFileFromTree's MkdirAll.
func pruneEmptiedDirs(wt *gogit.Worktree, changes []FileChange) {
	fs := wt.Filesystem
	for _, ch := range changes {
		if ch.Kind != "delete" {
			continue
		}
		for parent := path.Dir(ch.Path); parent != "." && parent != "/"; parent = path.Dir(parent) {
			// Lstat first: billy's ReadDir/Remove FOLLOW symlinks, so if the
			// user exposed a managed dir via a symlink (~/.claude/skills ->
			// /elsewhere), "pruning" the emptied dir would unlink the USER'S
			// SYMLINK — a user-owned entry the restore contract leaves alone
			// (the same class the create-path Lstat refusal guards). A symlink
			// (or anything that is not a real directory) ends the chain.
			if info, lerr := fs.Lstat(parent); lerr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				break
			}
			entries, err := fs.ReadDir(parent)
			if err != nil || len(entries) > 0 {
				break // already gone (a sibling pruned it), unreadable, or still holds entries — keep it and everything above
			}
			if err := fs.Remove(parent); err != nil {
				break
			}
		}
	}
}

// symlinkAncestor walks rel's parent chain (slash-separated, repo-relative)
// and reports the first ancestor that is a symlink, if any. Every mutation the
// restore performs — create, modify, delete, prune — resolves paths through
// the OS (billy's chroot fs does not resolve symlinks itself but the syscalls
// under it do), so any symlinked ancestor redirects the mutation OUTSIDE the
// repo. Two layers use this walk: the pre-flight refuses up front
// (all-or-nothing, nothing mutated), and applyRestoreDelta re-checks at
// mutation time so a link planted between the two cannot slip through
// (pruneEmptiedDirs carries its own Lstat gate). The walk never inspects the
// repo root itself, so a user whose whole config dir is a symlink is
// unaffected.
func symlinkAncestor(root, rel string) (string, bool) {
	for parent := path.Dir(rel); parent != "." && parent != "/"; parent = path.Dir(parent) {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(parent)))
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return parent, true
		}
	}
	return "", false
}

// restoreFailureHint wraps a failure that struck once the worktree delta-apply had begun
// — after Restore's snapshot may already have advanced HEAD — with an actionable
// recovery pointer. It names the pre-revert HEAD (orig) and, when one was taken, the
// snapshot commit that preserved the user's uncommitted tracked edits, so a half-applied
// worktree still has a clear path back. A pre-flight refusal does NOT go through
// here (no worktree file was mutated, so there is nothing to recover; the
// pre-refusal snapshot, when one was taken, is surfaced by the caller).
func restoreFailureHint(dir, orig, snapshot string, err error) error {
	if snapshot != "" {
		return fmt.Errorf("revert of %s failed partway; nothing is lost — your uncommitted edits are preserved in snapshot %s and the pre-revert checkpoint is %s. Recover with `git -C %s reset --hard %s` (keep your edits) or `git -C %s reset --hard %s` (discard them): %w",
			dir, Short(snapshot), Short(orig), dir, Short(snapshot), dir, Short(orig), err)
	}
	return fmt.Errorf("revert of %s failed partway; the pre-revert checkpoint is %s. Recover with `git -C %s reset --hard %s`: %w",
		dir, Short(orig), dir, Short(orig), err)
}

// firstUnmanagedEntryUnder walks absDir and returns the first entry — as a
// repoDir-relative slash path — that the delete pass will NOT clear away: a
// file (untracked or gitignored) not in the `deleting` set, or a SUBDIRECTORY
// with no deleted descendant. The delete pass removes only the files in
// `deleting`, and pruneEmptiedDirs then prunes only the emptied ancestor chains
// of those deleted paths — so a subdir holding no deleted file survives both
// passes. Notably an EMPTY subdir: it holds no file for the file check to flag,
// yet it makes the later removal of the whole directory fail mid-restore with
// the misleading concurrent-change (TOCTOU) wording. Flagging it here keeps the
// refusal loud, early, and all-or-nothing. Returns "" when the directory holds
// nothing agentsync isn't already removing.
func firstUnmanagedEntryUnder(repoDir, absDir string, deleting map[string]bool) (string, error) {
	var found string
	err := filepath.WalkDir(absDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == absDir {
			return nil // the directory being replaced itself
		}
		rel, rerr := filepath.Rel(repoDir, p)
		if rerr != nil {
			return rerr
		}
		slash := filepath.ToSlash(rel)
		if d.IsDir() {
			if hasDeletedDescendant(deleting, slash) {
				// The delete pass empties it and pruneEmptiedDirs prunes it (it
				// is an ancestor of a deleted path); keep walking inside it for
				// nested blockers the deletes don't cover.
				return nil
			}
			found = slash
			return filepath.SkipAll // survives the delete pass — a blocker
		}
		if !deleting[slash] {
			found = slash
			return filepath.SkipAll // stop at the first unmanaged file
		}
		return nil
	})
	return found, err
}

// hasDeletedDescendant reports whether any path in deleting lies strictly under
// the slash-relative directory dir.
func hasDeletedDescendant(deleting map[string]bool, dir string) bool {
	prefix := dir + "/"
	for p := range deleting {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// restoreFileFromTree writes the target tree's blob for slash-path p into the
// worktree filesystem, preserving the blob's file mode (notably the exec bit).
// It writes through wt.Filesystem so paths resolve under the worktree root even
// for folded/rerooted repos. The file is removed first so OpenFile's perm applies
// even when p already exists (a "modify"), since OpenFile only honors perm on
// creation.
//
// A symlink-mode blob (git mode 120000) is written as a regular file here, not
// recreated as a symlink; agentsync writes only regular files into the versioned
// dirs, so a tracked symlink blob does not arise in practice.
func restoreFileFromTree(wt *gogit.Worktree, tree *object.Tree, p string) error {
	fs := wt.Filesystem
	// Leaf-symlink TOCTOU backstop: the pre-flight refuses a symlink at a
	// CREATE path (1b) and the ancestor rechecks cover parents, but a link
	// planted at the leaf AFTER the pre-flight (or at a modify leaf, which the
	// pre-flight doesn't inspect — the leaf is tracked content) would be
	// silently unlinked by the fs.Remove below and overwritten. That never
	// escapes the repo (unlink doesn't follow), but it destroys the user's
	// link — refuse like every other structural conflict instead. The narrow
	// legitimate loss: a TRACKED symlink being restored to a regular file now
	// refuses too (move the link and re-run), which errs on the never-destroy-
	// a-user-link side.
	if info, lerr := fs.Lstat(p); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cannot restore %q: it is currently a symlink agentsync does not manage; move or remove it, then re-run revert", p)
	}
	f, err := tree.File(p)
	if err != nil {
		return fmt.Errorf("loading blob %s from target checkpoint: %w", p, err)
	}
	mode, err := f.Mode.ToOSFileMode()
	if err != nil {
		return fmt.Errorf("resolving file mode of %s: %w", p, err)
	}
	reader, err := f.Reader()
	if err != nil {
		return fmt.Errorf("reading blob %s: %w", p, err)
	}
	defer reader.Close()

	// 0o755 matches agentsync's own writers (iox.AtomicWrite creates parents at
	// 0o755), so a revert that re-creates a pruned parent chain yields the same
	// directory modes an apply would.
	if dir := path.Dir(p); dir != "." && dir != "/" {
		if err := fs.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating parent dir of %s: %w", p, err)
		}
	}
	// Drop any existing file so the target mode takes effect on (re)create. Restore's
	// up-front pre-flight already refuses a dir->file replacement that would destroy
	// unmanaged files, so reaching here with p still a non-empty directory means the
	// filesystem changed AFTER that scan (a concurrent write) — a TOCTOU backstop.
	// Distinct wording ("still holds ... after the pre-flight") so a test can tell it
	// apart from the pre-flight's refusal; we still never delete the unmanaged files.
	if err := fs.Remove(p); err != nil && !os.IsNotExist(err) {
		if info, statErr := fs.Stat(p); statErr == nil && info.IsDir() {
			return fmt.Errorf("cannot restore %q to a file: its directory still holds unmanaged files after the pre-flight (a concurrent change?); move or remove your own files under %q, then re-run revert", p, p)
		}
		return fmt.Errorf("replacing %s: %w", p, err)
	}
	dst, err := fs.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("opening %s for write: %w", p, err)
	}
	if _, err := io.Copy(dst, reader); err != nil {
		_ = dst.Close()
		return fmt.Errorf("writing %s: %w", p, err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", p, err)
	}
	return nil
}

// UntrackedPaths returns the sorted slash-relative paths of files in the worktree
// that git is not tracking (a `?` status) — the user's own scratch files that a
// revert leaves untouched. Note go-git's status does not enumerate gitignored
// files, so those (also preserved by revert) do not appear here.
func (r *Repo) UntrackedPaths() ([]string, error) {
	wt, err := r.repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("worktree for %s: %w", r.dir, err)
	}
	st, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("git status in %s: %w", r.dir, err)
	}
	var out []string
	for p, fs := range st {
		if isUntracked(fs) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// commitTree loads the tree of a commit hash.
func (r *Repo) commitTree(h plumbing.Hash) (*object.Tree, error) {
	c, err := r.repo.CommitObject(h)
	if err != nil {
		return nil, fmt.Errorf("loading commit %s in %s: %w", Short(h.String()), r.dir, err)
	}
	t, err := c.Tree()
	if err != nil {
		return nil, fmt.Errorf("loading tree of %s in %s: %w", Short(h.String()), r.dir, err)
	}
	return t, nil
}
