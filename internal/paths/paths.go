// Package paths centralizes filesystem path resolution honoring AGENTSYNC_HOME,
// AGENTSYNC_TARGET_ROOT, and third-party agent home overrides (GROK_HOME), and
// holds the two directory-containment predicates (lexical ContainsDir; the
// symlink-resolving, and therefore filesystem-reading, ContainsDirResolved /
// SameDirResolved). Production code MUST use this package; lint forbids
// os.UserHomeDir in *_test.go files.
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

// ContainsDir reports whether parent is child itself or one of its ancestors,
// comparing the paths as SPELLED (cleaned, no filesystem access). Use it for
// destination-root TOPOLOGY — the git-backup de-nesting pass and its owner map
// — where the declared spelling is what git backup inits, opens and stages by;
// see denestRoots in internal/cli for why resolving symlinks there is wrong.
// For directory IDENTITY (is this GROK_HOME really $HOME under another name?)
// use ContainsDirResolved / SameDirResolved; a guard whose miss would be
// costly tests both (the never-at-$HOME guard drops a root that contains the
// home by spelling OR by identity). The two agree whenever no symlink or case
// difference is involved.
func ContainsDir(parent, child string) bool {
	return containsNormalized(filepath.Clean(parent), filepath.Clean(child))
}

// ContainsDirResolved is ContainsDir on directory IDENTITY rather than spelling:
// both paths are cleaned, resolved through symlinks, and compared ignoring case
// on the case-insensitive platforms (macOS, Windows). It backs the guards whose
// cost of a miss is a repo at or above $HOME — the central never-at-or-above-
// $HOME check in git backup and Grok's GROK_HOME refusal — where
// `GROK_HOME=/Users/Alice`, or a symlink to the home directory, must not walk
// past a check written for `$HOME=/users/alice` (issue #270). Those guards
// test BOTH predicates and refuse if either says contained: identity catches
// the aliased spelling, and the lexical check still catches an ancestor of the
// home's own spelling when the home itself is a symlink elsewhere
// (`$HOME=/home/alice → /data/alice`, root `/home`).
//
// Resolution goes through the deepest EXISTING ancestor and re-appends the
// rest of the path unchanged, so a path that does not exist yet and one that
// does normalize into the same tree (see resolveExistingPrefix). The answer
// therefore depends on filesystem state and can change once a pending path is
// created — acceptable for the identity checks above ($HOME always exists),
// and the reason this predicate is NOT used for de-nesting.
func ContainsDirResolved(parent, child string) bool {
	return containsNormalized(normalizeDir(parent), normalizeDir(child))
}

// SameDirResolved reports whether a and b name the same directory under
// ContainsDirResolved's normalization.
func SameDirResolved(a, b string) bool {
	return normalizeDir(a) == normalizeDir(b)
}

// containsNormalized is the shared Rel-based containment over two paths already
// brought to the same canonical form: parent contains child when child is
// parent or lies beneath it with no ".." escape. Sibling prefixes
// (`/home/alice` vs `/home/alice-evil`) are not containment.
func containsNormalized(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// normalizeDir is the canonical form behind ContainsDirResolved and
// SameDirResolved: clean, symlinks resolved through the deepest existing
// ancestor, case-folded where the platform's filesystem is.
func normalizeDir(p string) string {
	p = resolveExistingPrefix(filepath.Clean(p))
	if caseInsensitiveFS {
		p = strings.ToLower(p)
	}
	return p
}

// resolveExistingPrefix returns cleaned p with its deepest existing ancestor
// (possibly p itself) passed through filepath.EvalSymlinks and the non-existent
// tail re-appended. For an ABSOLUTE path every candidate ancestor is a
// component-wise prefix of p, so the tail is the remainder past that prefix. A
// relative path is returned unchanged (apart from Clean) once the walk reaches
// ".", which is not a component prefix of anything but itself: the callers only
// ever pass absolute paths, and silently resolving a relative one against the
// cwd would be a different contract.
func resolveExistingPrefix(p string) string {
	for cur := p; ; {
		if !isComponentPrefix(p, cur) {
			return p // relative path walked up to "." — nothing to anchor on
		}
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(filepath.Clean(resolved), p[len(cur):])
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p // no resolvable ancestor (unreadable root or volume)
		}
		cur = parent
	}
}

// isComponentPrefix reports whether prefix is p itself or an ancestor spelling
// of p that ends exactly on a path component — so "/" and "/a" are prefixes of
// "/a/b", but "." is not a prefix of ".claude" and "/a" is not one of "/ab".
func isComponentPrefix(p, prefix string) bool {
	if prefix == p {
		return true
	}
	if !strings.HasPrefix(p, prefix) {
		return false
	}
	return strings.HasSuffix(prefix, string(filepath.Separator)) || p[len(prefix)] == filepath.Separator
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
