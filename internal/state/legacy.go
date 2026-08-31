package state

import (
	"errors"
	"fmt"
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
//     project root, so the reading agentsync ACTUALLY WROTE is always contained
//     — withinProject is normalized so that stays true for any bytes, separator
//     or dot segment. A false reading can therefore only ADD to the contained
//     set, never displace the true one: at worst it pushes the count to two and
//     turns an acceptance into the refusal at step 8. Containment is only ever
//     a tie-break, never a filter — a lone candidate is accepted at step 6
//     whether or not it is contained.
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
	// Report the number of readings the user must actually disambiguate. When
	// containment narrowed the field but did not settle it, the surviving tie is
	// among the CONTAINED readings — quoting the raw candidate count there would
	// overstate it and send the reader looking for splits agentsync has already
	// ruled out.
	n := len(all)
	if len(contained) >= 2 {
		n = len(contained)
	}
	return Key{}, fmt.Errorf("%w: %q has %d readings", errAmbiguousLegacyKey, s, n)
}

// withinProject reports whether destination path p lies inside the project root.
//
// This is parseLegacyKey's step-7 tie-break, and the property it must have is
// not "be accurate" but "never lose the reading agentsync actually wrote". If
// the true reading always survives the filter, a false reading can at most make
// the survivor count two and turn the acceptance into a REFUSAL — it can never
// take the acceptance for itself. That is what makes the tie-break safe, and it
// follows from how containmentParts normalizes:
//
//	agentsync writes a project-scope destination as the project root plus a
//	relative tail, so the true reading's p is byte-for-byte root + separator +
//	tail. containmentParts is a per-component FILTER — it drops only empty and
//	"." components and never rewrites, reorders or removes anything else — so
//	p's components are exactly the root's components followed by the tail's, and
//	the true reading is ALWAYS contained.
//
// The previous implementation (path.Clean over a slash-substituted string) did
// NOT have that property. Clean collapses "..", so a root or destination
// carrying a ".." component lost components the other did not; the true reading
// could drop out of the contained set and leave a FALSE reading alone in it,
// which then won — a silent wrong-project acceptance, exactly the bug class the
// typed key exists to remove (e.g. `claude:project:H/\..:H/\../..:` decoded
// under project `H/\..:H/\../..` instead of `H/\..`, with no error). Hence
// containmentParts leaves ".." alone: in a state key a component is a stored
// string, not a path to resolve.
func withinProject(project, p string) bool {
	// A key with no project field roots nothing: "" is not a directory, so it
	// must not be reported as containing anything. The guard is load-bearing,
	// not belt-and-braces: "" normalizes to the EMPTY component list, which is a
	// prefix of every relative path, so without it a candidate whose project
	// field is empty would vacuously contain any non-rooted destination and
	// silently settle a tie ("claude:project::a:b/x" in legacy_test.go).
	if project == "" {
		return false
	}
	rootAbs, root := containmentParts(project)
	destAbs, dest := containmentParts(p)
	if rootAbs != destAbs || len(dest) < len(root) {
		return false
	}
	for i := range root {
		if dest[i] != root[i] {
			return false
		}
	}
	return true
}

// containmentParts splits s into the components withinProject compares: rooted
// reports a leading separator, and parts are the separator-delimited fields with
// the empty ("a//b") and "." fields dropped. Dropping "." is what handles the
// "${HOME}/." form paths.HomeRelative produces when the project root IS the
// user's $HOME.
//
// Both '\' and '/' are separators, UNCONDITIONALLY — deliberately not
// filepath.Separator, which is compiled against the HOST and would make this a
// slash-only test on POSIX. targets.json is a portable artifact (the
// ${HOME}-relative encoding exists so it can be synced between machines) and
// parseLegacyKey is pure string work, so a Windows-written key must decode
// identically wherever it is read — including in the Linux container this
// package's tests run in. paths.HomeRelative stores a path under $HOME as
// "${HOME}/..." with forward slashes but returns a path OUTSIDE $HOME verbatim,
// so on Windows an ordinary project root outside %USERPROFILE% arrives here as
// `C:\dev\repo`. That drive colon makes the v1 key ambiguous (three candidate
// project/tail splits), and a slash-only containment test finds ZERO contained
// readings, so the tie-break could never fire and the whole state file was
// refused on the first run after upgrade. Treating '\' as a separator is what
// makes it fire.
//
// ".." is deliberately NOT collapsed; see withinProject for why that is
// load-bearing.
func containmentParts(s string) (rooted bool, parts []string) {
	s = strings.ReplaceAll(s, `\`, "/")
	rooted = strings.HasPrefix(s, "/")
	for _, c := range strings.Split(s, "/") {
		if c == "" || c == "." {
			continue
		}
		parts = append(parts, c)
	}
	return rooted, parts
}
