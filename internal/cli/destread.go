package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/render"
)

// errDestNotRegular is returned by readDestBytes for a destination path that is
// present but is not a regular file. It is a sentinel so callers can match it
// with errors.Is; it deliberately carries NO path, because every caller either
// discards it or wraps it with the path itself, and embedding one here would
// double it in the wrapped message.
var errDestNotRegular = errors.New("not a regular file")

// errDestUnstattable is returned when the destination cannot be STAT'd for a
// reason other than absence — EACCES on a parent, ELOOP, ENOTDIR. It wraps the
// real errno.
//
// It is separate from errDestNotRegular because the two are not the same claim
// and one caller shows its sentinel to a user: reconcile's write-back refusal
// says "remove or replace the non-regular file at that path", which is false
// for a permission problem. hashFile, whose sentinels are opaque tokens
// compared only for equality, deliberately treats both alike — see its comment.
var errDestUnstattable = errors.New("cannot stat destination")

// pathlessStatErr strips the redundant path from a *fs.PathError, mirroring
// secrets.pathlessErr. errors.Is still matches the underlying errno.
func pathlessStatErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

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
// paths (#241, #242). `doctor` is not merely exposed there: with a FIFO at
// ~/.claude/settings.json it hangs outright (measured, rc=124, wedged after
// printing "Plugins"), through claude.IngestPlugins.
//
// Three things to know about the shape check:
//
//   - The check is a stat, so it is racy against a reshape between stat and
//     open. That window needs write access to the destination's own directory,
//     which is already game over; closing it properly needs O_RDONLY|O_NONBLOCK
//     plus fstat.
//   - A destination that cannot be stat'd comes back as errDestUnstattable
//     wrapping the real errno, NOT as a shape error. "Present and the wrong
//     shape" and "shape unknown" are different facts, and one of them reaches a
//     user.
//   - Symlinks are FOLLOWED here, deliberately: this is also the key-merge
//     read (readDestFile) and import's state-seeding read, where the symlink
//     policy does not apply — a key-merge destination is decoded through the
//     link on every surface, as apply writes through it. The WHOLE-FILE reads
//     (hashFile, destModePerm, readDestText) apply the policy first, through
//     destReadPath below, and hand this function a symlink's resolved target
//     only when AGENTSYNC_ALLOW_SYMLINK_DEST=1 opted into it.
//     TestHashFileSentinels asserts both halves.
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
	// render.IsRegularOrAbsent stays the single authority on SHAPE, and it is
	// asked FIRST so the ordinary read costs exactly one stat. It answers false
	// for two different situations, though — a path that is present and the
	// wrong shape, and one that cannot be stat'd at all — so the refusal path
	// pays a second stat to tell them apart. Collapsing them was a real defect:
	// reconcile's refusal names errDestNotRegular and told someone with a
	// permission problem to "remove or replace the non-regular file".
	if !render.IsRegularOrAbsent(path) {
		if _, serr := os.Stat(path); serr != nil {
			// Pathless, for the reason errDestNotRegular carries no path: the
			// caller supplies it, and a *fs.PathError would make reconcile print
			// "read dest X: cannot stat destination: stat X: ...". Unwrapping to
			// the bare errno keeps errors.Is matching BOTH this sentinel and the
			// underlying syscall error.
			return nil, fmt.Errorf("%w: %w", errDestUnstattable, pathlessStatErr(serr))
		}
		return nil, errDestNotRegular
	}
	return os.ReadFile(path)
}

// symlinkRefusal is why destReadPath would not look through a symlink.
type symlinkRefusal int

const (
	symlinkNone         symlinkRefusal = iota // not a symlink, or resolved through it
	symlinkRefusedByEnv                       // switch unset: apply would not write through it either
	symlinkUnresolvable                       // opted in, but the link does not resolve (dangling, loop): apply fails on it too
)

// destReadPath applies the destination SYMLINK policy and answers the path the
// whole-file destination reads should actually look at. why is symlinkNone when
// resolved can be read; otherwise each caller answers its own "cannot read"
// value (hashFile a sentinel per reason, destModePerm (0, false), readDestText
// ""), and the two reasons are kept apart so the advice a user sees is right:
// "set the switch" is wrong advice for a link that would not resolve anyway.
//
// It mirrors iox.resolveSymlinkDest, the write side, through the shared
// iox.SymlinkDestAllowed, so the read side and apply cannot disagree about
// whether a symlinked destination is supported. The mirror is a POLICY one, not
// a literal prediction: apply only errors when the CONTENT differs (its
// convergence read follows the link), so with the env unset this answers "a
// managed regular file became a link you have not opted into — that is drift"
// rather than "apply would fail here".
//
// The policy is scoped to WHOLE-FILE facts; a key-merge destination is decoded
// through the link on every surface, as apply does (docs/architecture.md §6
// has the reasoning).
func destReadPath(path string) (resolved string, why symlinkRefusal) {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return path, symlinkNone
	}
	if !iox.SymlinkDestAllowed() {
		return "", symlinkRefusedByEnv
	}
	r, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", symlinkUnresolvable
	}
	return r, symlinkNone
}

// readDestText is the whole-file destination text read behind the drift walk:
// the destination's bytes, or "" for a refused symlink, a refused shape, or any
// read error — the three collapse because no caller distinguishes them.
func readDestText(path string) string {
	p, why := destReadPath(path)
	if why != symlinkNone {
		return ""
	}
	b, err := readDestBytes(p)
	if err != nil {
		return ""
	}
	return string(b)
}
