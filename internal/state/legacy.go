package state

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// errAmbiguousLegacyKey marks a v1 (schema_version <= 1) state key with more
// than one possible reading. The v1 format joined fields with ':' and both the
// project root and the destination path may legally contain one, so
//
//	claude:project:${HOME}/a:${HOME}/b:${HOME}/c
//
// could name project "${HOME}/a" at path "${HOME}/b:${HOME}/c", or project
// "${HOME}/a:${HOME}/b" at path "${HOME}/c". Guessing would write a Key under
// the wrong project — reproducing the very over-disown bug the typed key exists
// to remove — so migrate refuses instead. See migrate for the user-facing
// remedy.
var errAmbiguousLegacyKey = errors.New("legacy state key has more than one possible reading")

// parseLegacyFileKey decodes a v1 Targets.Files key,
// "<agent>:<scope>:<project>:<path>".
func parseLegacyFileKey(s string) (Key, error) { return parseLegacyKey(s, false) }

// parseLegacyPointerKey decodes a v1 Targets.Keys key,
// "<agent>:<scope>:<project>:<path>:<pointer>".
func parseLegacyPointerKey(s string) (Key, error) { return parseLegacyKey(s, true) }

// parseLegacyKey is the shared body. It NEVER guesses: it enumerates every
// syntactically possible reading and accepts one only when the choice is forced.
//
//  1. Agent is the bytes before the first ':' — agent names come from a fixed
//     registry and never contain one.
//  2. Scope is the next field and must be exactly "user" or "project", the only
//     two values adapter.Scope.String() produces.
//  3. At user scope the project field is always empty, so the remainder must
//     start with ':' and the project/tail split is FORCED — one candidate, no
//     choice to make. That retires the project-boundary ambiguity for the
//     overwhelming majority of every real targets.json. It does NOT make
//     user-scope keys ambiguity-free: a pointer key still splits path from
//     pointer at step 5, so e.g. "claude:user::a:/b:/c" has two readings and is
//     refused at step 8.
//  4. At project scope every ':' in the remainder is a candidate project/tail
//     boundary.
//  5. For a pointer key every ":/" in the tail is a candidate path/pointer
//     boundary — a JSON pointer always begins with '/' (render.CollectPointers).
//  6. Exactly one candidate reading: take it.
//  7. Several: prefer the readings whose path lies WITHIN the project root.
//     Every adapter's project-scope ResolvePaths joins its destinations onto the
//     project root, so this holds for every key agentsync has ever written —
//     provided containment is tested with the separator that key actually uses,
//     which is why withinProject normalizes '\' as well as '/'. It is used only
//     as a tie-breaker, never as a filter — a lone candidate is accepted at step
//     6 whether or not it is contained.
//  8. Still several, or none: errAmbiguousLegacyKey / a parse error.
func parseLegacyKey(s string, pointered bool) (Key, error) {
	agent, rest, ok := strings.Cut(s, ":")
	if !ok || agent == "" {
		return Key{}, fmt.Errorf("legacy state key %q: no agent field", s)
	}
	scope, remainder, ok := strings.Cut(rest, ":")
	if !ok || (scope != "user" && scope != "project") {
		return Key{}, fmt.Errorf("legacy state key %q: second field is %q, want \"user\" or \"project\"", s, scope)
	}

	type split struct{ project, tail string }
	var splits []split
	if scope == "user" {
		tail, ok := strings.CutPrefix(remainder, ":")
		if !ok {
			return Key{}, fmt.Errorf("legacy state key %q: user scope with a non-empty project field", s)
		}
		splits = append(splits, split{project: "", tail: tail})
	} else {
		for i := 0; i < len(remainder); i++ {
			if remainder[i] == ':' {
				splits = append(splits, split{project: remainder[:i], tail: remainder[i+1:]})
			}
		}
	}

	var all []Key
	for _, sp := range splits {
		if !pointered {
			all = append(all, Key{Agent: agent, Scope: scope, Project: sp.project, Path: sp.tail})
			continue
		}
		for i := 0; i+1 < len(sp.tail); i++ {
			if sp.tail[i] == ':' && sp.tail[i+1] == '/' {
				all = append(all, Key{
					Agent:   agent,
					Scope:   scope,
					Project: sp.project,
					Path:    sp.tail[:i],
					Pointer: sp.tail[i+1:],
				})
			}
		}
	}

	switch len(all) {
	case 0:
		return Key{}, fmt.Errorf("legacy state key %q: no valid reading", s)
	case 1:
		return all[0], nil
	}
	var contained []Key
	for _, k := range all {
		if withinProject(k.Project, k.Path) {
			contained = append(contained, k)
		}
	}
	if len(contained) == 1 {
		return contained[0], nil
	}
	return Key{}, fmt.Errorf("%w: %q has %d readings", errAmbiguousLegacyKey, s, len(all))
}

// withinProject reports whether destination path p lies inside the project root.
//
// The two arguments are NOT necessarily slash-form. paths.HomeRelative stores a
// path under $HOME as "${HOME}/..." with forward slashes, but returns a path
// OUTSIDE $HOME verbatim — so on Windows an ordinary project root outside
// %USERPROFILE% arrives here as `C:\dev\repo` with a drive colon and
// backslashes. That drive colon makes the v1 key ambiguous (three candidate
// project/tail splits), and a slash-only containment test finds ZERO contained
// readings, so the tie-break below could never fire and the whole state file was
// refused on the first run after upgrade. toSlashAny is what makes it fire.
//
// After normalization path.Clean is the right normalizer — it also collapses the
// "${HOME}/." form paths.HomeRelative produces when the project root IS the
// user's $HOME.
func withinProject(project, p string) bool {
	if project == "" {
		return false
	}
	root, dest := path.Clean(toSlashAny(project)), path.Clean(toSlashAny(p))
	return dest == root || strings.HasPrefix(dest, root+"/")
}

// toSlashAny rewrites every '\' to '/' UNCONDITIONALLY — deliberately not
// filepath.ToSlash, which is compiled against the HOST separator and is a no-op
// on POSIX. targets.json is a portable artifact (the ${HOME}-relative encoding
// exists so it can be synced between machines), and parseLegacyKey is pure
// string work, so a Windows-written key must decode identically wherever it is
// read — including in the Linux container this package's tests run in.
//
// A POSIX filename may legally contain '\', so this can only make containment
// MORE permissive there (byte-substitution preserves the prefix relation, it
// never breaks one). That is safe by construction: containment is only ever a
// TIE-BREAK among readings already enumerated, never a filter, so the extra
// permissiveness can turn a refusal into an acceptance (the Windows fix) or a
// lone acceptance into a refusal (still fail-closed) — it can never silently
// swap the winning reading for a different one.
func toSlashAny(s string) string { return strings.ReplaceAll(s, `\`, "/") }
