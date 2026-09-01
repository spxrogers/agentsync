package cli

import (
	"errors"
	"os"
	"path/filepath"
)

// sourceInitState classifies a canonical source root — the user's
// ~/.agentsync or a project tree's .agentsync — as `agentsync init` leaves it.
type sourceInitState int

const (
	// sourceInitOK — the root is a directory holding a regular agentsync.toml.
	sourceInitOK sourceInitState = iota
	// sourceInitRootMissing — the root does not exist.
	sourceInitRootMissing
	// sourceInitRootNotDir — the root exists but is not a directory.
	sourceInitRootNotDir
	// sourceInitRootUnreadable — stat of the root failed for another reason.
	sourceInitRootUnreadable
	// sourceInitConfigMissing — the root exists but holds no agentsync.toml.
	sourceInitConfigMissing
	// sourceInitConfigNotFile — agentsync.toml exists but is not a regular file.
	sourceInitConfigNotFile
	// sourceInitConfigUnreadable — stat of agentsync.toml failed for another reason.
	sourceInitConfigUnreadable
)

// probeSourceInit is the SINGLE stat-shaped "is this canonical source root
// initialized" probe. It returns the classification and, for the two
// *Unreadable states, the underlying stat error.
//
// It exists because there were three copies with three different notions of
// "initialized" (issue #228): check's requireInitializedSource never checked
// that the root was a directory, and neither it nor doctor's checkHomeDir
// checked that agentsync.toml was a regular file — so a directory named
// agentsync.toml was reported as an initialized tree by both, with the real
// failure surfacing much later as `read …/agentsync.toml: is a directory` from
// source.Load. This adopts the strictest reading: the root must be a directory
// and agentsync.toml must be a regular file, which is exactly what
// `agentsync init` produces and exactly what source.Load can consume.
//
// Only the PREDICATE is shared. Each caller renders its own message — an error
// for check, a report line for doctor, a bool for the upgrade notice — because
// those surfaces legitimately differ.
//
// Not folded in: loadSecretsConfig (internal/cli/secrets.go) answers the same
// question by os.ReadFile-ing agentsync.toml, because it must PARSE the file
// anyway; its ENOENT arm is that read's error branch, not a separate probe.
// Making it call this first would stat the file and then read it. See the
// sourceinit guard test for the boundary this file is the single home of.
func probeSourceInit(root string) (sourceInitState, error) {
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sourceInitRootMissing, nil
		}
		return sourceInitRootUnreadable, err
	}
	if !info.IsDir() {
		return sourceInitRootNotDir, nil
	}
	cfgInfo, err := os.Stat(filepath.Join(root, "agentsync.toml"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sourceInitConfigMissing, nil
		}
		return sourceInitConfigUnreadable, err
	}
	if !cfgInfo.Mode().IsRegular() {
		return sourceInitConfigNotFile, nil
	}
	return sourceInitOK, nil
}
