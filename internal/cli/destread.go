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

// readDestBytes reads a destination file's bytes, refusing any path whose shape
// cannot be read as a file. The check is a stat before the open, so it is
// racy against something reshaping the path in between — that window needs
// write access to the destination's own directory, which is already game over,
// and closing it properly needs O_RDONLY|O_NONBLOCK + fstat.
//
// The guard is not defensive tidying. os.ReadFile on a FIFO does not fail — it
// BLOCKS in the open, waiting for a writer that never comes, so no error path
// below the read ever runs and the command never returns. Measured on the
// unguarded code: a FIFO at a managed destination wedged `diff` and `reconcile`
// via their whole-file reads, and a FIFO-shaped key-merge destination (a
// ~/.claude.json, say) wedged `status` — which is advertised as read-only —
// through the shared readDestFile.
//
// This gate covers THIS PACKAGE only. `apply`, `apply --dry-run`,
// `reconcile --auto-override` (which re-applies through render.Writer.Write)
// and `import <agent>` still hang on the same fixture; their reads live in
// internal/render and the adapter Ingest paths (#241, #242).
//
// Every destination read in this package routes here, cli.hashFile included, so
// they cannot disagree about what is safe to read.
//
// They DO still disagree about symlinks, deliberately. hashFile Lstats first and
// refuses a link outright with its own sentinel; this function does not, so a
// read that reaches os.ReadFile follows it. `status` therefore calls a symlinked
// destination drifted while `diff` reads through it and compares the target.
//
// It is left alone because changing it is a BEHAVIOR decision, not because the
// current split is right: the reads this gate replaced were bare os.ReadFile,
// which follows links too, so refusing links here would change what `diff` and
// `reconcile` have always reported. That belongs with the drift-walk
// unification in #229, where all four walks can be changed together.
//
// The split has a real cost worth naming: under AGENTSYNC_ALLOW_SYMLINK_DEST=1
// apply writes THROUGH a symlinked destination, yet hashFile refuses links
// unconditionally, so `status` reports drift that no apply can ever clear. #229
// owns that too. TestHashFileSentinels asserts both halves of the split.
//
// What a caller DOES with the refusal is its own business, and today almost
// none of them surface it. Measured against a FIFO destination:
//
//   - `reconcile`'s write-back is the ONLY surface that names the shape.
//   - `status` maps it to the opaque not-a-regular-file hash, which exists only
//     to never equal a content hash; statusItem carries no reason, so the user
//     sees a bare "drift".
//   - `diff` leaves the destination text empty and readDestFile decodes to an
//     empty map, so a refused destination renders as "every byte / every key
//     removed" — indistinguishable from an absent one. `explain` inherits that.
//   - `doctor` performs no read through this gate, but its plugin check reaches
//     one anyway, in the adapter Ingest path (`IngestPlugins` -> a bare
//     os.ReadFile of the agent's settings). That read is unguarded, so `doctor`
//     is exposed to the same hang as `import` (#242), not immune to it.
//
// So the refusal is mostly a better DIAGNOSIS available to callers rather than
// one they give the user, and that gap is wider than this gate. Narrowing it
// means changing what several commands print, which belongs with the drift-walk
// unification in #229 rather than here.
//
// An ABSENT path is not refused: render.IsRegularOrAbsent reports absent as
// acceptable, so os.ReadFile runs and its ENOENT reaches the caller unchanged.
// Manufacturing a shape error for a file that is not there would name the wrong
// problem.
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
