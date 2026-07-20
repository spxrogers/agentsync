package adapter

import "os"

// ReadFileOptional reads the file at path while distinguishing a genuinely-absent
// file from one that is present but unreadable, so an ingest read never conflates
// the two. It returns:
//
//   - (data, true, nil)  when the file exists and is readable;
//   - (nil, false, nil)  when the file is absent (os.IsNotExist) — the only
//     legitimate "component not present" case, which callers skip silently;
//   - (nil, false, err)  for any OTHER read error (permission, EISDIR from a
//     directory sitting where a file is expected, transient I/O), which callers
//     MUST surface — return it (or, for a per-entry read inside a loop, warn) —
//     rather than treat as "absent".
//
// The hazard this guards against: an ingest read that acts only on the err==nil
// branch of os.ReadFile reads an unreadable-but-present file identically to an
// absent one, yielding an empty component (empty Memory.Body, no commands, no
// hooks). Drift/capture/reconcile then misread that empty component as "the user
// cleared it" and can destructively write nothing back over the canonical source
// (often a committed dotfiles repo). Only os.IsNotExist is a benign skip; every
// other error is loud.
func ReadFileOptional(path string) (data []byte, present bool, err error) {
	data, err = os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
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
