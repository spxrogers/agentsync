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
// (An earlier version of this comment justified it differently, by claiming
// AGENTSYNC_ALLOW_SYMLINK_DEST=1 would break. That was wrong — the variable is
// read only in internal/iox, on the WRITE path, so a read gate cannot affect
// it. The real consequence of the split is worse and is worth naming: under
// that supported setup apply writes THROUGH the link, yet hashFile refuses
// links unconditionally, so `status` reports drift that no apply can ever
// clear. #229 owns that too.)
// TestHashFileSentinels asserts both halves of the split.
//
// What a caller DOES with the refusal is its own business, and two of them
// currently swallow it: `diff` (internal/cli/diff.go) leaves the destination
// text empty and `readDestFile` decodes to an empty map, so a refused
// destination renders as "every byte / every key removed" — indistinguishable
// from an absent one. That is a poorer diagnosis than the shape error deserves,
// and it is the same silent-drop shape this repo's own rules warn about; it is
// left as-is here because surfacing it means changing what those two commands
// print, which belongs with the drift-walk unification in #229. `status`,
// `doctor` and `reconcile`'s write-back all name the shape correctly.
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
