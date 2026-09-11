package adapter

import (
	"errors"
	"fmt"
	"os"
)

// ErrNotRegularFile is returned when path exists but is not a regular file
// (FIFO, directory, device, socket). Callers must treat it as a loud error,
// never as "absent": os.ReadFile on a FIFO blocks forever (#242).
var ErrNotRegularFile = errors.New("not a regular file")

// ReadFileOptional reads the file at path while distinguishing a genuinely-absent
// file from one that is present but unreadable, so an ingest read never conflates
// the two. It returns:
//
//   - (data, true, nil)  when the file exists and is readable;
//   - (nil, false, nil)  when the file is absent (os.IsNotExist) — the only
//     legitimate "component not present" case, which callers skip silently;
//   - (nil, false, err)  for any OTHER read error (permission, EISDIR from a
//     directory sitting where a file is expected, a FIFO, transient I/O), which
//     callers MUST surface — return it (or, for a per-entry read inside a loop,
//     warn) — rather than treat as "absent".
//
// The hazard this guards against: an ingest read that acts only on the err==nil
// branch of os.ReadFile reads an unreadable-but-present file identically to an
// absent one, yielding an empty component (empty Memory.Body, no commands, no
// hooks). Drift/capture/reconcile then misread that empty component as "the user
// cleared it" and can destructively write nothing back over the canonical source
// (often a committed dotfiles repo). Only os.IsNotExist is a benign skip; every
// other error is loud.
//
// A FIFO is present-but-unreadable: os.ReadFile does not fail, it blocks in
// open(2). The shape check uses Stat (follows symlinks) so a link to a FIFO
// is refused the same way as a bare one.
func ReadFileOptional(path string) (data []byte, present bool, err error) {
	fi, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, false, nil
		}
		return nil, false, statErr
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%w: %s", ErrNotRegularFile, path)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

// ReadExisting returns dest bytes, or (nil, nil) if the path is absent.
// A present non-regular dest (FIFO, directory) returns ErrNotRegularFile
// without opening the path.
func ReadExisting(path string) ([]byte, error) {
	data, present, err := ReadFileOptional(path)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	return data, nil
}

// ReadDirOptional lists the directory at path with the same absent-vs-error
// discrimination as ReadFileOptional: (entries, true, nil) when the directory
// exists and is readable, (nil, false, nil) when it is absent (os.IsNotExist),
// and (nil, false, err) for any other read error (which callers MUST surface).
// A component directory the user never created stays a silent skip; a present
// directory that cannot be listed (permission, transient I/O) is loud, so it is
// never mistaken for "no skills/commands/subagents".
func ReadDirOptional(path string) (entries []os.DirEntry, present bool, err error) {
	entries, err = os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return entries, true, nil
}
