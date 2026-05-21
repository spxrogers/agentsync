// Package paths centralizes filesystem path resolution honoring AGENTSYNC_HOME
// and AGENTSYNC_TARGET_ROOT. Production code MUST use this package; lint forbids
// os.UserHomeDir in *_test.go files.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// Env abstracts environment-variable lookup so tests can inject a fake.
type Env interface {
	Get(key string) string
}

// OSEnv reads the live process environment.
type OSEnv struct{}

func (OSEnv) Get(key string) string { return os.Getenv(key) }

// MapEnv is a fake Env backed by a map (for tests).
type MapEnv map[string]string

func (m MapEnv) Get(key string) string { return m[key] }

// HomeDir returns the effective home dir. AGENTSYNC_TARGET_ROOT takes precedence
// (used by tests to redirect away from the real $HOME); otherwise falls back to
// $HOME.
func HomeDir(e Env) string {
	if root := e.Get("AGENTSYNC_TARGET_ROOT"); root != "" {
		return root
	}
	return e.Get("HOME")
}

// AgentsyncHome returns the directory where agentsync stores its source repo.
// Resolution order:
//  1. $AGENTSYNC_HOME (explicit override; absolute path)
//  2. <HomeDir>/.agentsync
func AgentsyncHome(e Env) string {
	if h := e.Get("AGENTSYNC_HOME"); h != "" {
		return h
	}
	return filepath.Join(HomeDir(e), ".agentsync")
}

// HomeRelative converts an absolute destination path into the portable
// form stored in state files: "${HOME}/.claude.json" instead of the
// machine-specific absolute "/Users/alice/.claude.json". Paths that do
// not live under home are returned unchanged.
//
// Without this normalization, state.Files / state.Keys keys would embed
// the absolute path that existed on the machine that wrote them, so a
// state file synced via chezmoi from /Users/alice/ to /home/alice/ would
// have every key prefix change and every native file would reclassify
// as ForeignCollision on the next apply.
//
// HomeRelative uses forward-slash separators in the stored form so the
// same key is produced on POSIX and Windows when home is the equivalent
// path.
func HomeRelative(home, abs string) string {
	if home == "" || abs == "" {
		return abs
	}
	rel, err := filepath.Rel(home, abs)
	if err != nil {
		return abs
	}
	// filepath.Rel may return "../something" — reject because that means
	// the path is outside home.
	if rel == ".." || hasParentPrefix(rel) {
		return abs
	}
	return "${HOME}/" + filepath.ToSlash(rel)
}

// FromHomeRelative is the inverse of HomeRelative: it expands a leading
// "${HOME}" / "${HOME}/" in a stored state path back to an absolute path
// rooted at userHome. Paths stored absolute (because they were outside home
// when recorded) are returned unchanged. Callers that turn a stored state
// key back into a real filesystem path (e.g. `agent disable --purge`) MUST
// route through this so they don't operate on the literal "${HOME}/..."
// string.
func FromHomeRelative(userHome, stored string) string {
	const tok = "${HOME}"
	if stored == tok {
		return userHome
	}
	if rest, ok := strings.CutPrefix(stored, tok+"/"); ok {
		return filepath.Join(userHome, filepath.FromSlash(rest))
	}
	return stored
}

// hasParentPrefix returns true when s starts with ".." as a path segment.
func hasParentPrefix(s string) bool {
	if len(s) < 2 || s[:2] != ".." {
		return false
	}
	if len(s) == 2 {
		return true
	}
	return s[2] == '/' || s[2] == '\\'
}
