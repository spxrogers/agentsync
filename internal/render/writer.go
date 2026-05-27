package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/state"
)

// DefaultBackupKeep is how many backup-timestamp dirs apply retains.
const DefaultBackupKeep = 20

// PruneBackups removes all but the most recent `keep` timestamp directories
// under <home>/.state/backups. Each backup is a verbatim copy of a
// pre-existing native config file (which may contain secrets), so they must
// not accumulate unbounded — a disk-bloat and credential-lingering concern.
// The dir names are zero-padded, fixed-width timestamps, so lexical sort is
// chronological. Best-effort: a removal error for one dir doesn't abort.
func PruneBackups(home string, keep int) error {
	if keep < 0 {
		return nil
	}
	root := filepath.Join(home, ".state", "backups")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) <= keep {
		return nil
	}
	sort.Strings(dirs) // ascending → oldest first
	for _, d := range dirs[:len(dirs)-keep] {
		_ = os.RemoveAll(filepath.Join(root, d))
	}
	return nil
}

// Writer is THE funnel for native-destination writes. Every adapter's
// Apply method receives one of these and routes its writes through it
// (rather than calling iox.AtomicWrite directly), so the
// foreign-collision backup invariant is enforced at the only place it
// matters: the moment of the write.
//
// Pre-write, Writer checks the (agent, scope, project, path) tuple
// against state. If state does not yet record this destination as owned
// AND the file already exists with content that differs from what we are
// about to write, the existing content is copied to
// <home>/.state/backups/<ts>/<original-path> before the new write
// proceeds. For merge ops, the same check runs at JSON-pointer
// granularity using op.Content (ours pre-merge) and op.OwnedKeys.
//
// The forbidigo lint rule in .golangci.yml fails any direct
// iox.AtomicWrite call outside the allowed packages, so a future
// contributor cannot silently bypass this guard.
type Writer struct {
	state *state.Targets
	// home is the agentsync home (~/.agentsync); it anchors the backup
	// root only. userHome is the user's $HOME (paths.HomeDir) and is the
	// base for HomeRelative state-key normalization — dest files like
	// ~/.claude.json live under userHome, NOT under the agentsync home, so
	// using home here would never normalize them (the cross-machine
	// portability no-op fixed in this change).
	home     string
	userHome string
	scope    adapter.Scope
	project  string
	agent    string

	backupRoot string          // <home>/.state/backups/<ts>; created lazily
	backedUp   map[string]bool // path → already-backed-up this run
	wrote      map[string]bool // path → destination owned/written this run
	unchanged  map[string]bool // path → already held our exact bytes (write skipped)
	reports    []CollisionReport
	// dryRun, when true, skips both the destination write AND the backup
	// write. The reports slice is still populated so callers can preview
	// the foreign-collisions a real apply would produce.
	dryRun bool
}

// CollisionReport describes one foreign-collision the writer detected and
// backed up. Callers can surface these to the user.
type CollisionReport struct {
	Agent    string
	Path     string
	Pointer  string // empty for whole-file collisions
	BackupTo string // absolute path of the backup that was written
}

// String formats a CollisionReport for human output.
func (r CollisionReport) String() string {
	if r.Pointer != "" {
		return fmt.Sprintf("foreign-collision %s#%s (backed up to %s)", r.Path, r.Pointer, r.BackupTo)
	}
	return fmt.Sprintf("foreign-collision %s (backed up to %s)", r.Path, r.BackupTo)
}

// NewWriter constructs a Writer for one (agent, scope, project) tuple.
// The render layer creates one writer per agent during Apply. home is the
// agentsync home (backup root); userHome is the user's $HOME (state-key
// normalization base).
func NewWriter(st *state.Targets, home, userHome string, scope adapter.Scope, project, agent string) *Writer {
	// Include the nanosecond offset so two applies in the same wall-clock
	// second (e.g. a user-scope apply immediately followed by a project-scope
	// one, or a retried apply) don't share a backup root and silently clobber
	// each other's pre-existing-file copies. The global lock serializes
	// applies, so successive runs always land on distinct nanoseconds.
	now := time.Now().UTC()
	ts := fmt.Sprintf("%s-%09d", now.Format("20060102T150405Z"), now.Nanosecond())
	return &Writer{
		state:      st,
		home:       home,
		userHome:   userHome,
		scope:      scope,
		project:    project,
		agent:      agent,
		backupRoot: filepath.Join(home, ".state", "backups", ts),
		backedUp:   map[string]bool{},
		wrote:      map[string]bool{},
		unchanged:  map[string]bool{},
	}
}

// NewPreviewWriter constructs a Writer that records foreign-collision
// reports without performing any disk writes. Used by `apply --dry-run` to
// surface the same backup-and-overwrite events a real apply would produce.
func NewPreviewWriter(st *state.Targets, home, userHome string, scope adapter.Scope, project, agent string) *Writer {
	w := NewWriter(st, home, userHome, scope, project, agent)
	w.dryRun = true
	return w
}

// Reports returns the per-write collision reports accumulated so far.
// Safe to call after Apply completes.
func (w *Writer) Reports() []CollisionReport { return w.reports }

// Wrote returns the set of destination paths this writer actually wrote.
// Used by the apply-error rescue to record state ONLY for files agentsync
// committed this run — a pre-existing foreign file at an op that was never
// attempted must not be recorded as owned (that would suppress its backup).
func (w *Writer) Wrote() map[string]bool { return w.wrote }

// Unchanged returns the set of destination paths that already held exactly the
// bytes apply would write, so the atomic write (and its mtime churn) was
// skipped. apply uses it to report "up to date" instead of "applied: N ops".
func (w *Writer) Unchanged() map[string]bool { return w.unchanged }

// Write satisfies adapter.DestWriter. finalBytes is the post-merge content
// for merge ops, or op.Content for replace ops.
func (w *Writer) Write(op adapter.FileOp, finalBytes []byte) error {
	// Convergence short-circuit: if the destination already holds exactly the
	// bytes we'd write, skip the write so a no-op apply doesn't churn the file's
	// mtime (which misleads mtime-watching tooling and makes a clean re-apply
	// look like real work). The post-condition (dest == finalBytes) already
	// holds, so still mark it owned for state recording; nothing is overwritten,
	// so there is nothing to back up. Skipped under dry-run (no read needed).
	if !w.dryRun {
		if cur, err := os.ReadFile(op.Path); err == nil && bytes.Equal(cur, finalBytes) {
			w.wrote[op.Path] = true
			w.unchanged[op.Path] = true
			return nil
		}
	}
	if err := w.maybeBackup(op, finalBytes); err != nil {
		return err
	}
	mode := os.FileMode(op.Mode)
	if mode == 0 {
		mode = 0o644
	}
	if err := iox.AtomicWrite(op.Path, finalBytes, mode); err != nil {
		return err
	}
	w.wrote[op.Path] = true
	return nil
}

// Delete satisfies adapter.DestWriter. Idempotent on missing files.
//
// Skill-orphan deletes (op.SourceID under "skills/", synthesized by Apply when a
// skill or bundled file is removed from source) get two extra guarantees: a dest
// that drifted from what agentsync last wrote is backed up before removal (the
// never-destroy-unsynced-content invariant the write path enforces), and empty
// skill directories left behind are pruned up to — but never including — the
// agent's skills root. Other delete callers (agent disable --purge, reconcile
// orphan removal) pass an empty SourceID and keep the plain idempotent remove.
func (w *Writer) Delete(op adapter.FileOp) error {
	if w.dryRun {
		return nil
	}
	skillOrphan := strings.HasPrefix(op.SourceID, "skills/")
	if skillOrphan {
		if err := w.backupOrphanIfDrifted(op); err != nil {
			return err
		}
	}
	if err := os.Remove(op.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", op.Path, err)
	}
	if skillOrphan {
		pruneEmptySkillDirs(op.Path, op.SourceID)
	}
	return nil
}

// backupOrphanIfDrifted copies a soon-to-be-deleted skill file to the backup
// root iff its on-disk content is not exactly what agentsync last wrote (i.e.
// the user hand-edited it). A file matching our last-applied hash is our own
// output and is removed without a backup; anything else is preserved first so an
// orphan delete can never silently destroy an unsynced edit.
func (w *Writer) backupOrphanIfDrifted(op adapter.FileOp) error {
	existing, err := os.ReadFile(op.Path)
	if err != nil {
		return nil // already gone (or unreadable): nothing to preserve
	}
	stateKey := fmt.Sprintf("%s:%s:%s:%s", w.agent, w.scope.String(),
		paths.HomeRelative(w.userHome, w.project), paths.HomeRelative(w.userHome, op.Path))
	if entry, owned := w.state.Files[stateKey]; owned {
		sum := sha256.Sum256(existing)
		if hex.EncodeToString(sum[:]) == entry.SHA256 {
			return nil // unchanged since our last apply — safe to delete
		}
	}
	dest, err := w.backup(op.Path, existing)
	if err != nil {
		return err
	}
	w.reports = append(w.reports, CollisionReport{Agent: w.agent, Path: op.Path, BackupTo: dest})
	return nil
}

// pruneEmptySkillDirs removes now-empty directories left after deleting a skill
// file, walking up from the file toward the skills root and stopping at the
// first non-empty directory (os.Remove fails on a non-empty dir) or at the root
// itself. The root is derived from sourceID ("skills/<name>/<rest>"): the dest
// has exactly that many path components below it, so stripping them yields the
// agent's skills directory, which is never removed.
func pruneEmptySkillDirs(absPath, sourceID string) {
	below := len(strings.Split(filepath.ToSlash(sourceID), "/")) - 1
	root := absPath
	for i := 0; i < below; i++ {
		root = filepath.Dir(root)
	}
	for dir := filepath.Dir(absPath); len(dir) > len(root) && dir != root; dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			return // non-empty (holds untracked files) or error: stop
		}
	}
}

// maybeBackup performs the foreign-collision check and conditionally
// writes the backup. It mutates w.backedUp and w.reports.
func (w *Writer) maybeBackup(op adapter.FileOp, finalBytes []byte) error {
	if w.backedUp[op.Path] {
		return nil
	}
	switch op.MergeStrategy {
	case "merge-json-keys", "merge-jsonc-keys", "merge-toml-keys":
		return w.maybeBackupKeyOp(op)
	default:
		return w.maybeBackupFileOp(op, finalBytes)
	}
}

func (w *Writer) maybeBackupFileOp(op adapter.FileOp, finalBytes []byte) error {
	stateKey := fmt.Sprintf("%s:%s:%s:%s", w.agent, w.scope.String(),
		paths.HomeRelative(w.userHome, w.project), paths.HomeRelative(w.userHome, op.Path))
	if _, owned := w.state.Files[stateKey]; owned {
		return nil
	}
	existing, err := os.ReadFile(op.Path)
	if err != nil || len(existing) == 0 {
		return nil
	}
	if bytesEqual(existing, finalBytes) {
		return nil
	}
	dest, err := w.backup(op.Path, existing)
	if err != nil {
		return err
	}
	w.reports = append(w.reports, CollisionReport{Agent: w.agent, Path: op.Path, BackupTo: dest})
	return nil
}

func (w *Writer) maybeBackupKeyOp(op adapter.FileOp) error {
	existing, err := os.ReadFile(op.Path)
	if err != nil || len(existing) == 0 {
		return nil
	}
	existingMap, err := decodeDestObject(op.MergeStrategy, existing)
	if err != nil {
		// JSONC/TOML: best-effort. We don't replicate hujson.Standardize here
		// because the writer must stay neutral on adapter format quirks.
		// If we can't parse the file we fall back to file-level treatment.
		return w.maybeBackupFileOpForJSONCFallback(op)
	}
	if existingMap == nil {
		return nil
	}
	var ours map[string]any
	if err := json.Unmarshal(op.Content, &ours); err != nil {
		return nil
	}

	var backupPath string
	portableProject := paths.HomeRelative(w.userHome, w.project)
	portablePath := paths.HomeRelative(w.userHome, op.Path)

	// Foreign type-mismatch at an owned top-level key. agentsync owns a
	// section (mcpServers/hooks/lspServers/mcp) at child-object granularity,
	// so CollectPointers only yields second-level pointers (/mcpServers/<id>).
	// If the existing file holds a SCALAR or ARRAY at that key instead of an
	// object, overlayOwned (jsonkeys) replaces it wholesale, yet the per-child
	// loop below never fires (getPointer can't descend a non-object) — silently
	// destroying foreign content. An owned key can never be a non-object in
	// state (agentsync always writes objects there), so a non-object existing
	// value is unambiguously foreign: back it up before the overwrite.
	for k, ov := range ours {
		if _, ourObj := ov.(map[string]any); !ourObj {
			continue // scalar/array in ours is covered by the pointer loop
		}
		ev, present := existingMap[k]
		if !present {
			continue
		}
		if _, evObj := ev.(map[string]any); evObj {
			continue // both objects: per-child conflicts handled below
		}
		if backupPath == "" {
			dest, err := w.backup(op.Path, existing)
			if err != nil {
				return err
			}
			backupPath = dest
		}
		w.reports = append(w.reports, CollisionReport{
			Agent: w.agent, Path: op.Path, Pointer: "/" + escapeJSONPointer(k), BackupTo: backupPath,
		})
	}

	for _, ptr := range CollectPointers(ours, "") {
		stateKey := fmt.Sprintf("%s:%s:%s:%s:%s", w.agent, w.scope.String(), portableProject, portablePath, ptr)
		if _, owned := w.state.Keys[stateKey]; owned {
			continue
		}
		ev, present := getPointerOK(existingMap, ptr)
		if !present {
			continue
		}
		ov := getPointer(ours, ptr)
		if hashAny(ev) == hashAny(ov) {
			continue
		}
		// Conflict.
		if backupPath == "" {
			dest, err := w.backup(op.Path, existing)
			if err != nil {
				return err
			}
			backupPath = dest
		}
		w.reports = append(w.reports, CollisionReport{
			Agent: w.agent, Path: op.Path, Pointer: ptr, BackupTo: backupPath,
		})
	}
	return nil
}

// maybeBackupFileOpForJSONCFallback handles the rare case of a key-merge dest
// that fails to decode as its declared format — a JSONC dest whose stripped
// form still fails JSON.Unmarshal, or a merge-toml-keys config.toml the TOML
// decoder rejects. In that case per-pointer collision detection isn't possible,
// so we conservatively back up the whole file once if state has no entries
// claiming the path.
func (w *Writer) maybeBackupFileOpForJSONCFallback(op adapter.FileOp) error {
	stateKeyPrefix := fmt.Sprintf("%s:%s:%s:%s:", w.agent, w.scope.String(),
		paths.HomeRelative(w.userHome, w.project), paths.HomeRelative(w.userHome, op.Path))
	for k := range w.state.Keys {
		if strings.HasPrefix(k, stateKeyPrefix) {
			return nil // state owns at least one pointer here; trust the merge
		}
	}
	existing, err := os.ReadFile(op.Path)
	if err != nil || len(existing) == 0 {
		return nil
	}
	dest, err := w.backup(op.Path, existing)
	if err != nil {
		return err
	}
	w.reports = append(w.reports, CollisionReport{Agent: w.agent, Path: op.Path, BackupTo: dest})
	return nil
}

// backup writes existing to <backupRoot>/<rel-path> via iox.AtomicWrite
// and marks the path as backed-up so subsequent ops don't double-back-up.
// In dry-run mode no disk write happens; only the dest path is computed
// so the caller can surface it in a preview report.
func (w *Writer) backup(path string, existing []byte) (string, error) {
	dest := backupPathFor(path, w.backupRoot)
	if w.dryRun {
		w.backedUp[path] = true
		return dest, nil
	}
	if err := iox.AtomicWrite(dest, existing, 0o600); err != nil {
		return "", fmt.Errorf("backup %s: %w", path, err)
	}
	w.backedUp[path] = true
	return dest, nil
}

// backupPathFor computes the deterministic backup destination for src.
//
// Defense-in-depth: filepath.Join cleans the joined path, so a src that
// contains ".." segments could resolve outside backupRoot. We guarantee
// containment by:
//  1. Cleaning src first so ".." sequences are collapsed.
//  2. Re-rooting via filepath.Rel to drop any prefix that would escape.
//  3. Asserting the result is still under backupRoot.
//
// Today's adapters never produce ".." in destination paths, but the
// guard means a future bug or hostile plugin component path can't turn
// a backup into a write-anywhere primitive.
func backupPathFor(src, backupRoot string) string {
	cleaned := filepath.Clean(src)
	// Drop the leading separator(s) and any drive letter so Join treats
	// the path as relative.
	trimmed := strings.TrimLeft(cleaned, string(os.PathSeparator))
	if vol := filepath.VolumeName(cleaned); vol != "" {
		trimmed = strings.TrimLeft(strings.TrimPrefix(cleaned, vol), string(os.PathSeparator))
	}
	dest := filepath.Join(backupRoot, trimmed)
	// Final containment check — if Clean leaves dest above backupRoot
	// (e.g. trimmed started with ".."), fall back to a hash-style segment
	// so we never escape.
	rel, err := filepath.Rel(backupRoot, dest)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.Join(backupRoot, "_escaped", filepath.Base(cleaned))
	}
	return dest
}

// BackupFile copies the file at path into <home>/.state/backups/<ts>/ verbatim
// (0600) and returns the backup path. It is the standalone analog of the apply
// Writer's per-collision backup, for callers (reconcile's interactive orphan
// delete) that must preserve a file before a destructive action so no choice
// loses content. Returns ("", nil) when path is missing.
func BackupFile(home, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s for backup: %w", path, err)
	}
	now := time.Now().UTC()
	ts := fmt.Sprintf("%s-%09d", now.Format("20060102T150405Z"), now.Nanosecond())
	dest := backupPathFor(path, filepath.Join(home, ".state", "backups", ts))
	if err := iox.AtomicWrite(dest, data, 0o600); err != nil {
		return "", fmt.Errorf("backup %s: %w", path, err)
	}
	return dest, nil
}

// bytesEqual returns true iff a and b have identical contents.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
