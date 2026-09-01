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
// unguarded code: a FIFO at a managed destination wedged `diff` and `reconcile`
// via their whole-file reads, and a FIFO-shaped key-merge destination (a
// ~/.claude.json, say) wedged `status` — which is advertised as read-only —
// through the shared readDestFile.
//
// This gate covers THIS PACKAGE only. `apply`, `apply --dry-run` and
// `import <agent>` still hang on the same fixture; their reads live in
// internal/render and the adapter Ingest paths (#241, #242).
//
// cli.hashFile already applied exactly this rule, and said so: "Shares render's
// predicate so the destination-read guards cannot disagree about what is safe
// to read." That was true of the HASH and false of every other destination
// read. hashFile now calls this function instead of holding a second copy, so
// within this package that sentence describes a property rather than a
// coincidence.
//
// It does NOT hold across the symlink axis, and this comment previously claimed
// it did. hashFile Lstats first and refuses a symlink outright with its own
// sentinel; this function does not, so a read that reaches os.ReadFile follows
// the link. `status` therefore calls a symlinked destination drifted while
// `diff` reads through it and compares the target. That divergence predates the
// gate — every one of these reads was a bare os.ReadFile, which follows links
// too — and is deliberately left alone, because AGENTSYNC_ALLOW_SYMLINK_DEST=1
// is a documented, supported setup in which apply writes THROUGH the link, so
// refusing links here would break it. Reconciling the two is a behavior
// decision, tracked with the drift-walk unification in #229.
//
// An ABSENT path is not refused: render.IsRegularOrAbsent reports absent as
// acceptable, so os.ReadFile runs and its ENOENT reaches the caller unchanged.
// Manufacturing a shape error for a file that is not there would name the wrong
// problem. Note the predicate also answers false for a stat failure that is NOT
// ENOENT (EACCES on a parent, ELOOP), so those surface as errDestNotRegular
// rather than as themselves — imprecise, but in the safe direction, and it
// keeps this function's answer identical to the one hashFile gave before it was
// folded in.
func readDestBytes(path string) ([]byte, error) {
	if !render.IsRegularOrAbsent(path) {
		return nil, errDestNotRegular
	}
	return os.ReadFile(path)
}
