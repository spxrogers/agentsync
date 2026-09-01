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

// readDestBytes reads a destination file's bytes, refusing any path that is
// present but not a regular file BEFORE the open.
//
// The guard is not defensive tidying. os.ReadFile on a FIFO does not fail — it
// BLOCKS in the open, waiting for a writer that never comes, so no error path
// below the read ever runs and the command never returns. Measured on the
// unguarded code: a FIFO at a managed destination wedged `diff` and `reconcile`
// via their whole-file reads, and a FIFO-shaped key-merge destination (a
// ~/.claude.json, say) wedged `status` — which is advertised as read-only —
// through the shared readDestFile.
//
// render.hashFile already applied exactly this rule, and said so: "Shares
// render's predicate so the destination-read guards cannot disagree about what
// is safe to read." That was true of the HASH and false of every other
// destination read, which is the gap this closes. Same predicate, so the
// statement is now true of all of them.
//
// An ABSENT path is not refused: render.IsRegularOrAbsent passes it through to
// os.ReadFile, whose ENOENT is the truthful answer and is what every caller
// already handles. Manufacturing a shape error for a file that is not there
// would name the wrong problem.
func readDestBytes(path string) ([]byte, error) {
	if !render.IsRegularOrAbsent(path) {
		return nil, errDestNotRegular
	}
	return os.ReadFile(path)
}
