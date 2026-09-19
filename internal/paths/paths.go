// Package paths centralizes filesystem path resolution honoring AGENTSYNC_HOME,
// AGENTSYNC_TARGET_ROOT, and third-party agent home overrides (GROK_HOME).
// Production code MUST use this package; lint forbids os.UserHomeDir in
// *_test.go files.
//
// AGENTSYNC_TARGET_ROOT is the sandbox switch: when set, EVERY path this package
// resolves — the effective home, the canonical source, an agent's own home
// override — lives under it, and the user-facing overrides (AGENTSYNC_HOME,
// GROK_HOME) are ignored. That is what lets the test suite run unchanged on a
// machine that actually uses agentsync (issue #270).
package paths

import (
	"os"
	"path/filepath"
	"runtime"
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
//  1. <AGENTSYNC_TARGET_ROOT>/.agentsync — a redirected run is a sandbox: every
//     path, the canonical source included, lives under the redirect root.
//  2. $AGENTSYNC_HOME (the user-facing explicit override; absolute path)
//  3. <HOME>/.agentsync
//
// The redirect outranks AGENTSYNC_HOME deliberately (issue #270). Tests isolate
// themselves by setting AGENTSYNC_TARGET_ROOT to a tmp dir; if an ambient
// AGENTSYNC_HOME — exported by anyone who actually uses agentsync — could beat
// that redirect, every test resolving the canonical source would silently land
// on the developer's real ~/.agentsync tree. AGENTSYNC_TARGET_ROOT is a
// documented testing knob that real users never set, so for them the user-facing
// contract is unchanged: AGENTSYNC_HOME wins over the $HOME default.
func AgentsyncHome(e Env) string {
	if root := e.Get("AGENTSYNC_TARGET_ROOT"); root != "" {
		return filepath.Join(root, ".agentsync")
	}
	if h := e.Get("AGENTSYNC_HOME"); h != "" {
		return h
	}
	return filepath.Join(HomeDir(e), ".agentsync")
}

// ContainsDir reports whether parent is child itself or one of its ancestors.
// It is THE containment predicate for destination roots and home directories
// (issue #270): the git-backup de-nesting pass, the "never init a repo at or
// above $HOME" guard, the grok GROK_HOME refusal and the traversal guard's
// containment assertion all share it, so they cannot disagree.
//
// Both paths are normalized before comparison, because the invariants this
// backs are about the DIRECTORY, not its spelling: each is cleaned, symlinks
// are resolved best-effort (a path that does not exist yet keeps its cleaned
// spelling), and on the case-insensitive platforms (macOS, Windows) the
// comparison ignores case — `/Users/Alice` and `/users/alice` are one
// directory there, and a byte compare would let `GROK_HOME=/Users/Alice` walk
// past a guard written for `$HOME=/users/alice`.
func ContainsDir(parent, child string) bool {
	p, c := normalizeDir(parent), normalizeDir(child)
	rel, err := filepath.Rel(p, c)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// SameDir reports whether a and b name the same directory under ContainsDir's
// normalization (clean, symlinks resolved best-effort, case-insensitive where
// the platform is).
func SameDir(a, b string) bool {
	return normalizeDir(a) == normalizeDir(b)
}

// normalizeDir is the shared canonical form behind ContainsDir and SameDir.
//
// Symlink resolution must be SYMMETRIC for a path that exists and one that does
// not yet: on a first apply the parent root (`~/.claude`) exists but the nested
// one (`~/.claude/skills`) is about to be created, and if only the existing
// side were resolved through a symlinked `~/.claude` the two would normalize
// into different trees and de-nesting would keep both — a repo inside a repo.
// So a path that does not fully exist is resolved through its deepest EXISTING
// ancestor and the remaining components are re-appended unchanged.
func normalizeDir(p string) string {
	p = resolveExistingPrefix(filepath.Clean(p))
	if caseInsensitiveFS {
		p = strings.ToLower(p)
	}
	return p
}

// resolveExistingPrefix returns p with its deepest existing ancestor (possibly
// p itself) passed through filepath.EvalSymlinks and the non-existent tail
// re-appended. A path with no existing ancestor at all (or one that errors for
// any other reason) is returned cleaned but otherwise unchanged.
func resolveExistingPrefix(p string) string {
	var tail []string
	for cur := p; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			parts := append([]string{filepath.Clean(resolved)}, tail...)
			return filepath.Join(parts...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p // reached the root without resolving anything
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
}

// caseInsensitiveFS is true on the platforms whose default filesystems fold
// case (APFS/HFS+ on macOS, NTFS on Windows). A per-path probe would be more
// precise but would have to write to disk; the platform default is what every
// user of those systems actually has. A var, not a const, only so an internal
// test can exercise the fold on Linux.
var caseInsensitiveFS = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// AgentHomeOverride returns the value of a third-party agent's own home-override
// variable (e.g. Grok Build's GROK_HOME), or "" when AGENTSYNC_TARGET_ROOT is set.
// A redirected run must never escape to the agent's real home, so the redirect
// wins outright — the same sandbox rule AgentsyncHome applies to AGENTSYNC_HOME.
// Production code reads such variables ONLY through this helper (never a raw
// os.Getenv), so they are injectable via Env like every other path input.
func AgentHomeOverride(e Env, key string) string {
	if e.Get("AGENTSYNC_TARGET_ROOT") != "" {
		return ""
	}
	return e.Get(key)
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
