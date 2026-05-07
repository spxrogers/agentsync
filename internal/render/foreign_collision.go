package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/state"
)

// CollisionReport describes one foreign-collision the apply guard found.
// Apply may surface these to the user before (or instead of) overwriting,
// depending on policy.
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

// ApplyWithCollisionGuard wraps Apply with foreign-collision detection.
// For each write op:
//
//   - File-level (replace strategy): if state has no FileEntry for this op
//     AND the destination exists with non-zero content AND that content
//     differs from what we are about to write, copy the existing file to
//     <home>/.state/backups/<ts>/<rel> before letting the adapter overwrite.
//
//   - Key-level (merge-json-keys / merge-jsonc-keys): for each pointer
//     ours has, if state has no KeyEntry for that pointer AND existing
//     has a value at that pointer AND it differs from ours, treat the
//     containing file as a collision; back the whole file up once, then
//     let the adapter merge. (Per-key backup is overkill — the file is
//     small and the user wants the original.)
//
// Returns the list of collisions detected (for the caller to surface) and
// any error from the underlying Apply.
//
// home is the agentsync repo root (~/.agentsync); the backup directory is
// created lazily under <home>/.state/backups/<ts>/.
func ApplyWithCollisionGuard(
	p RenderPlan,
	reg *adapter.Registry,
	st *state.Targets,
	home string,
	scope adapter.Scope,
	project string,
) ([]CollisionReport, error) {
	var reports []CollisionReport
	ts := time.Now().UTC().Format("20060102T150405Z")
	backupRoot := filepath.Join(home, ".state", "backups", ts)
	backedUp := map[string]bool{} // path → already-backed-up

	for agentName, res := range p.PerAgent {
		for _, op := range res.Ops {
			if op.Action != "" && op.Action != "write" {
				continue
			}
			rs, err := detectCollisions(agentName, scope, project, op, st, backupRoot, backedUp)
			if err != nil {
				return reports, err
			}
			reports = append(reports, rs...)
		}
	}
	_ = reg // reserved for future per-adapter policy overrides
	return reports, Apply(p, reg)
}

// detectCollisions handles one op. May write 0 or 1 backup file. Returns
// a report per detected collision (one for whole-file, multiple for
// per-pointer collisions in the same file).
func detectCollisions(
	agentName string,
	scope adapter.Scope,
	project string,
	op adapter.FileOp,
	st *state.Targets,
	backupRoot string,
	backedUp map[string]bool,
) ([]CollisionReport, error) {
	switch op.MergeStrategy {
	case "merge-json-keys", "merge-jsonc-keys":
		return detectKeyCollisions(agentName, scope, project, op, st, backupRoot, backedUp)
	default:
		return detectFileCollision(agentName, scope, project, op, st, backupRoot, backedUp)
	}
}

func detectFileCollision(
	agentName string,
	scope adapter.Scope,
	project string,
	op adapter.FileOp,
	st *state.Targets,
	backupRoot string,
	backedUp map[string]bool,
) ([]CollisionReport, error) {
	stateKey := fmt.Sprintf("%s:%s:%s:%s", agentName, scope.String(), project, op.Path)
	if _, known := st.Files[stateKey]; known {
		return nil, nil // we own this file; not a collision
	}
	existing, err := os.ReadFile(op.Path)
	if err != nil || len(existing) == 0 {
		return nil, nil // dest absent → not a collision, just a fresh write
	}
	if bytesEqual(existing, op.Content) {
		return nil, nil // content matches anyway — converged-on-arrival
	}
	if backedUp[op.Path] {
		return nil, nil
	}
	dest, err := backupFile(op.Path, backupRoot)
	if err != nil {
		return nil, fmt.Errorf("backup %s: %w", op.Path, err)
	}
	backedUp[op.Path] = true
	return []CollisionReport{{Agent: agentName, Path: op.Path, BackupTo: dest}}, nil
}

func detectKeyCollisions(
	agentName string,
	scope adapter.Scope,
	project string,
	op adapter.FileOp,
	st *state.Targets,
	backupRoot string,
	backedUp map[string]bool,
) ([]CollisionReport, error) {
	existing, err := os.ReadFile(op.Path)
	if err != nil || len(existing) == 0 {
		return nil, nil
	}
	var existingMap map[string]any
	if err := json.Unmarshal(existing, &existingMap); err != nil {
		// JSONC: best-effort. The opencode adapter standardizes via hujson
		// before MergeKeys; we don't replicate that here. If we can't parse
		// the file we skip collision detection rather than refuse to apply.
		return nil, nil
	}
	if existingMap == nil {
		return nil, nil
	}
	var ours map[string]any
	if err := json.Unmarshal(op.Content, &ours); err != nil {
		return nil, nil
	}
	var reports []CollisionReport
	for _, ptr := range CollectPointers(ours, "") {
		stateKey := fmt.Sprintf("%s:%s:%s:%s:%s", agentName, scope.String(), project, op.Path, ptr)
		if _, known := st.Keys[stateKey]; known {
			continue
		}
		ev := getPointer(existingMap, ptr)
		if ev == nil {
			continue
		}
		ov := getPointer(ours, ptr)
		if hashAny(ev) == hashAny(ov) {
			continue
		}
		// Collision found; ensure file backup exists exactly once.
		if !backedUp[op.Path] {
			dest, err := backupFile(op.Path, backupRoot)
			if err != nil {
				return reports, fmt.Errorf("backup %s: %w", op.Path, err)
			}
			backedUp[op.Path] = true
			reports = append(reports, CollisionReport{
				Agent: agentName, Path: op.Path, Pointer: ptr, BackupTo: dest,
			})
		} else {
			reports = append(reports, CollisionReport{
				Agent: agentName, Path: op.Path, Pointer: ptr,
				BackupTo: backupPathFor(op.Path, backupRoot),
			})
		}
	}
	return reports, nil
}

// backupFile copies src to <backupRoot>/<rel>, where <rel> is the original
// path with leading slashes stripped so the structure mirrors the native
// layout: ~/.claude.json → <backupRoot>/<home>/.claude.json. Uses
// iox.AtomicWrite so a crash mid-backup doesn't leave a half-written copy.
func backupFile(src, backupRoot string) (string, error) {
	dest := backupPathFor(src, backupRoot)
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	data, err := io.ReadAll(in)
	if err != nil {
		return "", err
	}
	if err := iox.AtomicWrite(dest, data, 0o600); err != nil {
		return "", err
	}
	return dest, nil
}

// backupPathFor computes the deterministic backup destination for src.
func backupPathFor(src, backupRoot string) string {
	clean := strings.TrimLeft(src, string(os.PathSeparator))
	return filepath.Join(backupRoot, clean)
}

// bytesEqual returns true iff a and b have identical contents. Avoids a
// bytes.Equal import to keep the package's import surface small.
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

// hashAnyJSONValue is a small wrapper used by tests; it mirrors hashAny
// (which is unexported) so external test code can still reason about the
// expected per-key hash format.
func hashAnyJSONValue(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
