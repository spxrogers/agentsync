package cli

import (
	"errors"
	"os"

	"github.com/spxrogers/agentsync/internal/render"
)

// errDestNotRegular is returned by readDestBytes for a destination path that is
// present but is not a regular file. It is a sentinel so callers can match it
// with errors.Is; it deliberately carries NO path, because every caller either
// discards it or wraps it with the path itself, and embedding one here would
// double it in the wrapped message.
var errDestNotRegular = errors.New("not a regular file")

// readDestBytes reads a destination file's bytes, refusing before the open any
// path whose shape cannot be read as a file.
//
// The guard is not defensive tidying. os.ReadFile on a FIFO does not fail — it
// BLOCKS in the open, waiting for a writer that never comes, so no error path
// below the read ever runs and the command never returns. Measured on the
// unguarded code: a FIFO at a managed destination wedged `diff` and
// `reconcile`, and a FIFO-shaped key-merge destination (a ~/.claude.json, say)
// wedged `status` — which is advertised as read-only — through the shared
// readDestFile.
//
// Every destination read in THIS PACKAGE routes here, cli.hashFile included, so
// they cannot disagree about what is safe to read. Other packages are not
// covered: `apply`, `apply --dry-run`, `reconcile --auto-override` and
// `import <agent>` still hang, and `doctor` is exposed through its plugin
// check, because those reads live in internal/render and the adapter Ingest
// paths (#241, #242).
//
// Two deliberate limits:
//
//   - The check is a stat, so it is racy against a reshape between stat and
//     open. That window needs write access to the destination's own directory,
//     which is already game over; closing it properly needs O_RDONLY|O_NONBLOCK
//     plus fstat.
//   - Symlinks are followed here but refused outright by hashFile, so `status`
//     calls a symlinked destination drifted while `diff` reads through it. The
//     reads this gate replaced followed links too, so changing that is a
//     behavior decision for #229 — which also owns the consequence that under
//     AGENTSYNC_ALLOW_SYMLINK_DEST=1, where apply writes THROUGH the link,
//     `status` reports drift no apply can clear. TestHashFileSentinels asserts
//     both halves.
//
// Callers mostly do not surface the refusal — reconcile's write-back is the
// only one that names the shape today — so it is more a diagnosis available to
// them than one the user sees. Narrowing that means changing what several
// commands print; it is catalogued on #229 rather than here.
//
// An ABSENT path is not refused: os.ReadFile runs and its ENOENT reaches the
// caller unchanged, because manufacturing a shape error for a file that is not
// there would name the wrong problem.
func readDestBytes(path string) ([]byte, error) {
	// A stat failure that is NOT absence — EACCES on a parent, ELOOP — is
	// reported as itself. render.IsRegularOrAbsent answers false for those too,
	// and letting them through as errDestNotRegular would put a false statement
	// in front of the user: reconcile's refusal names that sentinel and would
	// tell someone with a permission problem to "remove or replace the
	// non-regular file at that path". The extra stat costs one syscall on an
	// error path and keeps render's predicate the single authority on SHAPE.
	if _, serr := os.Stat(path); serr != nil && !os.IsNotExist(serr) {
		return nil, serr
	}
	if !render.IsRegularOrAbsent(path) {
		return nil, errDestNotRegular
	}
	return os.ReadFile(path)
}
