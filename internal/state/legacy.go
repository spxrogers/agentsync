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
//
// A key refused by the enumeration cap is NOT this error — see
// errLegacyEnumerationCapped, which makes no claim about how many readings its
// key has.
var errAmbiguousLegacyKey = errors.New("legacy state key has more than one possible reading")

// errLegacyEnumerationCapped marks a v1 key refused for reaching
// maxLegacyReadings while its candidates were still being ENUMERATED.
//
// It is a separate sentinel from errAmbiguousLegacyKey deliberately, because it
// is not an ambiguity verdict. The cap fires mid-count — before the reading set
// is complete and before containment has scored any of it — so a key refused
// this way may have had several readings, exactly ONE, or NONE at all.
// parseLegacyKey never finds out, which is the whole point of stopping. Two
// examples, both refused here and both pinned by
// TestParseLegacyKey_CapIsNotAnAmbiguityVerdict, which certifies each reading
// count by parsing the same key shape just UNDER the cap:
//
//	"claude:project:a:b:/c" + strings.Repeat(":", 63) // 84 bytes, ONE reading
//	"claude:project:" + strings.Repeat("a:", 70)      // 155 bytes, NO reading
//
// Reporting either as ambiguous would hand the user the wrong remedy: migrate's
// ambiguity paragraph explains why agentsync will not pick BETWEEN readings,
// which is not what happened. migrate branches on the two sentinels separately
// and prints a paragraph for each. The fix is the same (delete the entry; the
// next apply re-adopts the destination); the explanation is not.
var errLegacyEnumerationCapped = errors.New("legacy state key has too many candidate readings to enumerate")

// maxLegacyReadings bounds how many candidates parseLegacyKey will enumerate
// for one v1 key before refusing it outright.
//
// The enumeration is a PRODUCT, and the tie-break walks its result: at project
// scope every ':' in the remainder is a candidate project/tail boundary, and in
// a pointer key every ":/" in each of those tails is a candidate path/pointer
// boundary — so a key holding n ":/" pairs yields O(n²) readings, each costing
// a containment test linear in the key. That is O(n³) byte work driven by a
// hand-editable file that `status`, `apply`, `diff` and `doctor` all read.
// Unbounded it is a denial of service from a few KB of targets.json: measured
// on `"claude:project:" + strings.Repeat(":/", n)`, a 215-byte key cost 15 ms
// and 20 MiB, an 815-byte key 0.9 s and 1.3 GiB, and a 1615-byte key 4.7 s and
// 10 GiB — enough to OOM-kill the process a little above 4 KB.
//
// The cap is a deliberate, documented narrowing, and its cost is WIDER than
// "keys that were ambiguous anyway": it bounds ENUMERATION, not ambiguity.
// Counting stops the moment the cap is reached, so a key past it is refused
// whether it would have had many readings, exactly one, or none at all — see
// errLegacyEnumerationCapped, which is the sentinel both sites wrap and which
// carries a measured example of each. What it takes to get there: at project
// scope, a 65th ':' anywhere in the remainder — project, path and, in a pointer
// key, pointer all contribute; or, in a pointer key at either scope, a 65th
// candidate reading, which a ":/"-dense key reaches on FEWER colons than that
// (see the fourth row of the cap test). The refusal is the same
// fail-closed answer an ambiguous key already gets, with the same cheap remedy
// (see migrate), and TestParseLegacyKey_ReadingCapBoundsWork pins both sites.
const maxLegacyReadings = 64

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
//     Steps 4 and 5 multiply, so both are capped at maxLegacyReadings: a key
//     past the cap is refused (errLegacyEnumerationCapped) without enumerating
//     the rest — and therefore without ever learning how many readings it had.
//  6. Exactly one candidate reading: take it.
//  7. Several: prefer the readings whose path lies WITHIN the project root.
//     Every adapter's project-scope ResolvePaths joins its destinations onto the
//     project root, so the reading agentsync ACTUALLY WROTE is always contained
//     — projectRoot.contains is normalized so that stays true for any bytes,
//     separator or dot segment. A false reading can therefore only ADD to the
//     contained set, never displace the true one: at worst it pushes the count
//     to two and turns an acceptance into the refusal at step 8. Containment is
//     only ever a tie-break, never a filter — a lone candidate is accepted at
//     step 6 whether or not it is contained.
//     TestLegacyContainmentProperties pins that invariant over a generated
//     corpus; it is prose everywhere else.
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
			if remainder[i] != ':' {
				continue
			}
			if len(splits) == maxLegacyReadings {
				return Key{}, fmt.Errorf("%w: %q has more than %d candidate project/path splits",
					errLegacyEnumerationCapped, s, maxLegacyReadings)
			}
			splits = append(splits, split{project: remainder[:i], tail: remainder[i+1:]})
		}
	}

	var all, contained []Key
	for _, sp := range splits {
		// The candidate root is parsed ONCE per project/tail split rather than
		// once per reading — every reading this split produces shares its project
		// field. With the cap above that keeps a key's containment work
		// proportional to its LENGTH instead of to the square of its ':' count.
		root := newProjectRoot(sp.project)
		start := len(all)
		if !pointered {
			all = append(all, Key{Agent: agent, Scope: scope, Project: sp.project, Path: sp.tail})
		} else {
			for i := 0; i+1 < len(sp.tail); i++ {
				if sp.tail[i] != ':' || sp.tail[i+1] != '/' {
					continue
				}
				if len(all) == maxLegacyReadings {
					return Key{}, fmt.Errorf("%w: %q has more than %d possible readings",
						errLegacyEnumerationCapped, s, maxLegacyReadings)
				}
				all = append(all, Key{
					Agent:   agent,
					Scope:   scope,
					Project: sp.project,
					Path:    sp.tail[:i],
					Pointer: sp.tail[i+1:],
				})
			}
		}
		for _, k := range all[start:] {
			if root.contains(k.Path) {
				contained = append(contained, k)
			}
		}
	}

	switch len(all) {
	case 0:
		return Key{}, fmt.Errorf("legacy state key %q: no valid reading", s)
	case 1:
		return all[0], nil
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
	return Key{}, fmt.Errorf("%w: %q (%d readings)", errAmbiguousLegacyKey, s, n)
}

// projectRoot is one candidate reading's project field, parsed into the
// component form contains compares destination paths against. parseLegacyKey
// builds ONE per project/tail split and reuses it for every reading that split
// produces.
type projectRoot struct {
	// roots reports whether the field names a directory at all; when false,
	// contains is always false. See newProjectRoot.
	roots  bool
	rooted bool
	parts  []string
}

// newProjectRoot parses a candidate reading's project field.
//
// A key with no project field roots nothing: "" is not a directory, so it must
// not be reported as containing anything. The guard is load-bearing, not
// belt-and-braces: "" normalizes to the EMPTY component list, which is a prefix
// of every relative path, so without it a candidate whose project field is empty
// would vacuously contain any non-rooted destination and silently settle a tie
// ("claude:project::a:b/x" in legacy_test.go).
//
// "" is the only field treated this way — deliberately. Other fields also
// normalize to zero components ("/", "\", ".", "./."), and those DO vacuously
// contain every path of matching rootedness; contains documents why excluding
// them would be a regression rather than a fix.
func newProjectRoot(project string) projectRoot {
	if project == "" {
		return projectRoot{}
	}
	rooted, parts := containmentParts(project)
	return projectRoot{roots: true, rooted: rooted, parts: parts}
}

// contains reports whether destination path p lies inside r.
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
// TestLegacyContainmentProperties enforces that over a generated corpus of
// (root, tail) pairs, end to end through parseLegacyKey: no generated key ever
// decodes under a project other than the one it was built from.
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
//
// # Zero-component roots are deliberately still contained
//
// "/" normalizes to the empty component list (so do "\", "." and "./.", which a
// stored project field cannot actually hold — paths.HomeRelative emits
// "${HOME}/." for $HOME itself, one component, and an absolute root verbatim).
// An empty component list is a prefix of everything, so such a root vacuously
// contains every path of matching rootedness and can never discriminate.
//
// Excluding it looks like a pure win: a FALSE split of "/" then stops padding
// the contained set, and keys whose true root merely starts with "/:" are
// recovered instead of refused. It is not a win, because "/" is also a legal
// TRUE project root, and excluding it drops that true reading out of the
// contained set — leaving a false reading alone in it, which then wins silently
// under the wrong project. A sweep of 79,632 generated project-scope keys found
// zero misrecoveries as written and 24 with the exclusion, e.g.
// `claude:project:/://:/:` (true root "/", path "//:/:"), which decodes
// correctly today and under project "/://" with it. Over-refusing a key whose
// root begins "/:" is the cheap failure; misrecovering one rooted at "/" is the
// expensive one, so the vacuous containment stays.
func (r projectRoot) contains(p string) bool {
	if !r.roots {
		return false
	}
	destAbs, dest := containmentParts(p)
	if r.rooted != destAbs || len(dest) < len(r.parts) {
		return false
	}
	for i := range r.parts {
		if dest[i] != r.parts[i] {
			return false
		}
	}
	return true
}

// containmentParts splits s into the components projectRoot.contains compares:
// rooted reports a leading separator, and parts are the separator-delimited
// fields with the empty ("a//b") and "." fields dropped. Dropping "." is what
// handles the "${HOME}/." form paths.HomeRelative produces when the project root
// IS the user's $HOME (paths.HomeRelative calls filepath.Rel(home, home), which
// returns "."). TestParseLegacyKey pins that with a "${HOME}/." row; without the
// "." drop the root's components are ["${HOME}", "."] and no destination under
// it is contained.
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
// ".." is deliberately NOT collapsed; see projectRoot.contains for why that is
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
