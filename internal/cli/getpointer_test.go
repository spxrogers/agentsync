package cli

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestGetPointerValue_DecodesRFC6901 is the regression for status/diff
// misclassifying drift on a managed item whose id contains '~' or '/'.
// CollectPointers escapes those (~→~0, /→~1), but getPointerValue split the
// pointer without decoding, so it looked up the literal "foo~0bar" key instead
// of the real "foo~bar" key → nil source value → phantom drift forever. Every
// other pointer getter decoded; this one didn't. All of them now share
// jsonkeys.Get.
func TestGetPointerValue_DecodesRFC6901(t *testing.T) {
	m := map[string]any{
		"mcpServers": map[string]any{
			"foo~bar": map[string]any{"command": "x"},
			"a/b":     map[string]any{"command": "y"},
		},
	}
	if got := getPointerValue(m, "/mcpServers/foo~0bar"); got == nil {
		t.Fatalf("did not decode ~0: nil for /mcpServers/foo~0bar")
	}
	if got := getPointerValue(m, "/mcpServers/a~1b"); got == nil {
		t.Fatalf("did not decode ~1: nil for /mcpServers/a~1b")
	}
}

// TestHashAtPointer_AbsentVsNull verifies the import seed distinguishes an
// ABSENT pointer (return "" so the seed loop skips it, matching
// render.RecordOpsState) from a present-but-null value (which hashes). Before
// the fix, an absent pointer hashed sha256("null") and got seeded as a phantom.
func TestHashAtPointer_AbsentVsNull(t *testing.T) {
	m := map[string]any{"mcpServers": map[string]any{"github": map[string]any{"command": "x"}}}
	if h := hashAtPointer(m, "/mcpServers/github"); h == "" {
		t.Fatal("present pointer must hash non-empty")
	}
	if h := hashAtPointer(m, "/mcpServers/absent"); h != "" {
		t.Fatalf("absent pointer must return the empty sentinel, got %q", h)
	}
	mn := map[string]any{"k": nil}
	if h := hashAtPointer(mn, "/k"); h == "" {
		t.Fatal("present-null must hash, not be treated as absent")
	}
}

// pointerEscapeCall matches RFC 6901 token escaping spelled by hand: a
// ReplaceAll — strings.ReplaceAll, bytes.ReplaceAll, or the hand-written
// replaceAll internal/render once carried — whose operands are the '~'/'/' ↔
// "~0"/"~1" pairs, in either direction.
var pointerEscapeCall = regexp.MustCompile(`(?i)replaceall\([^()]*,\s*(?:\[\]byte\()?"(~0|~1|~|/)"\)?\s*,\s*(?:\[\]byte\()?"(~0|~1|~|/)"\)?\s*\)`)

// pointerEscapeSites reports whether src contains such a call. Factored out of
// the repo walk so the guard can be exercised against a synthetic source (see
// the negative control).
func pointerEscapeSites(src string) bool { return pointerEscapeCall.MatchString(src) }

// TestPointerEscapingIsInOnePlace pins that RFC 6901 token escaping has ONE
// definition: jsonkeys.EscapeToken / jsonkeys.UnescapeToken.
//
// Seven copies existed before #235, across internal/cli, internal/render and
// internal/jsonkeys, and each had to get a non-symmetric order right ('~' first
// when escaping, "~1" first when decoding). The escape feeds
// render.CollectPointers, which mints the state file's keys, so an eighth copy
// with the order backwards would report phantom drift forever for any id
// holding a '/' or a '~'. This is the mirror of
// TestEnabledAgentExtractionIsInOnePlace for the higher-stakes of the two.
//
// LIMIT: the pattern is call-shaped. A hand-written byte loop, or a ReplaceAll
// whose operands are variables rather than literals, slips past. It catches
// the four-line copy-paste that actually happened seven times, not every
// spelling.
func TestPointerEscapingIsInOnePlace(t *testing.T) {
	repoRoot := repoRootFromCaller(t)

	allowed := map[string]string{
		"internal/jsonkeys/pointer.go": "EscapeToken/UnescapeToken — the one RFC 6901 escape",
	}

	var unexpected []string
	seen := map[string]bool{}
	if err := walkRepoGoFiles(repoRoot, func(rel, src string) {
		if !pointerEscapeSites(src) {
			return
		}
		if _, ok := allowed[rel]; ok {
			seen[rel] = true
			return
		}
		unexpected = append(unexpected, rel)
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		t.Errorf("a hand-rolled RFC 6901 escape outside internal/jsonkeys/pointer.go:\n  %s\n\n"+
			"Call jsonkeys.EscapeToken / jsonkeys.UnescapeToken instead — the order is not symmetric and is pinned there.",
			strings.Join(unexpected, "\n  "))
	}
	for rel, reason := range allowed {
		if !seen[rel] {
			t.Errorf("%s is allowlisted (%s) but no longer contains the escape — drop it from the allowlist", rel, reason)
		}
	}

	// NEGATIVE CONTROL — a guard that has stopped biting must fail HERE rather
	// than pass silently.
	t.Run("negative control", func(t *testing.T) {
		for _, reintroduced := range []string{
			`s = strings.ReplaceAll(s, "~", "~0")`,
			`s = strings.ReplaceAll(s, "/", "~1")`,
			`p = strings.ReplaceAll(p, "~1", "/")`,
			`return strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")`,
			`s = replaceAll(s, "~", "~0")`,
			`b = bytes.ReplaceAll(b, []byte("~0"), []byte("~"))`,
		} {
			if !pointerEscapeSites(reintroduced) {
				t.Fatalf("the guard must flag a reintroduced escape: %s", reintroduced)
			}
		}
		for _, unrelated := range []string{
			`s = strings.ReplaceAll(s, "a", "b")`,
			`s = strings.ReplaceAll(s, "~", "-")`,
			`s = strings.ReplaceAll(s, "\\", "/")`,
			`// escapes ("~0" for "~", "~1" for "/") are supported.`,
		} {
			if pointerEscapeSites(unrelated) {
				t.Fatalf("the guard must not flag an unrelated ReplaceAll or a comment: %s", unrelated)
			}
		}
	})
}
