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

// pointerEscapeCall matches RFC 6901 token escaping spelled by hand as a
// ReplaceAll or Replace — strings.ReplaceAll, bytes.ReplaceAll, the older
// strings.Replace(…, -1), or the hand-written replaceAll internal/render once
// carried — whose operands are one of the four escape pairs ('~'→"~0",
// '/'→"~1", "~1"→'/', "~0"→'~'). Only the exact pairs match: a ReplaceAll that
// flattens '/' to '~' for a cache key is not an escape. The first operand may
// itself be a call one level deep.
var pointerEscapeCall = regexp.MustCompile(
	`(?i)replace(?:all)?\((?:[^()]|\([^()]*\))*,\s*(?:` +
		`(?:\[\]byte\()?"~"\)?\s*,\s*(?:\[\]byte\()?"~0"\)?|` +
		`(?:\[\]byte\()?"/"\)?\s*,\s*(?:\[\]byte\()?"~1"\)?|` +
		`(?:\[\]byte\()?"~1"\)?\s*,\s*(?:\[\]byte\()?"/"\)?|` +
		`(?:\[\]byte\()?"~0"\)?\s*,\s*(?:\[\]byte\()?"~"\)?` +
		`)(?:\s*,\s*-?\d+)?\s*\)`,
)

// pointerEscapeReplacer matches the other complete spelling of the same thing:
// a strings.NewReplacer whose operands are WHOLE escape pairs, e.g.
// strings.NewReplacer("~", "~0", "/", "~1"). A NewReplacer("/", "~") is the
// same cache-key flattening pointerEscapeCall declines to flag.
var pointerEscapeReplacer = regexp.MustCompile(
	`(?i)newreplacer\((?:\s*` + pointerEscapePair + `\s*,)*\s*` + pointerEscapePair + `\s*\)`,
)

const pointerEscapePair = `(?:"~"\s*,\s*"~0"|"/"\s*,\s*"~1"|"~1"\s*,\s*"/"|"~0"\s*,\s*"~")`

// pointerEscapeSite reports the first hand-rolled escape in src — the matched
// call text and which shape it is — ignoring FULL-LINE // comments, so a doc
// comment may quote the call it warns against. Factored out of the repo walk
// so the guard can be exercised against a synthetic source (see the negative
// control).
func pointerEscapeSite(src string) (string, bool) {
	var code []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code = append(code, line)
	}
	joined := strings.Join(code, "\n")
	if m := pointerEscapeCall.FindString(joined); m != "" {
		return "a ReplaceAll pair: " + m, true
	}
	if m := pointerEscapeReplacer.FindString(joined); m != "" {
		return "a NewReplacer over the pairs: " + m, true
	}
	return "", false
}

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
// LIMIT: the matcher is textual and shape-based. It sees the complete
// spellings a copy-paste produces — a ReplaceAll or Replace per pair, or a
// NewReplacer over the pairs — with literal operands. A hand-written byte
// loop, a rune switch, a ReplaceAll over variables, or a "~"+"0" split
// literal slips past. Only a FULL-LINE // comment is skipped: a trailing
// comment, a /* */ block, or a raw string literal quoting the call is flagged
// (an interpreted string literal is not, its quotes being escaped), and the
// walk never visits _test.go. The repo's go/ast guards (cleanupop_guard_test.go
// in internal/adapter, output_vocabulary_test.go here) would be exact; a few
// lines of regexp are proportionate for a guard whose job is to catch the
// copy that actually happened seven times.
func TestPointerEscapingIsInOnePlace(t *testing.T) {
	repoRoot := repoRootFromCaller(t)

	allowed := map[string]string{
		"internal/jsonkeys/pointer.go": "EscapeToken/UnescapeToken — the one RFC 6901 escape",
	}

	var unexpected []string
	seen := map[string]bool{}
	if err := walkRepoGoFiles(repoRoot, func(rel, src string) {
		site, ok := pointerEscapeSite(src)
		if !ok {
			return
		}
		if _, ok := allowed[rel]; ok {
			seen[rel] = true
			return
		}
		unexpected = append(unexpected, rel+": "+site)
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		t.Errorf("a hand-rolled RFC 6901 escape outside internal/jsonkeys/pointer.go:\n  %s\n\n"+
			"Call jsonkeys.EscapeToken / jsonkeys.UnescapeToken instead — the order is not symmetric and is pinned there. "+
			"A site that genuinely must spell it by hand goes in this test's allowlist, with its reason.",
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
			`k := strings.ReplaceAll(filepath.ToSlash(p), "/", "~1")`,
			`var esc = strings.NewReplacer("~", "~0", "/", "~1")`,
			`var unesc = strings.NewReplacer("~1", "/", "~0", "~")`,
			`s = strings.Replace(s, "~", "~0", -1)`,
			"\t// a comment above the call does not hide it\n\ts = strings.ReplaceAll(s, \"~\", \"~0\")\n",
		} {
			if _, ok := pointerEscapeSite(reintroduced); !ok {
				t.Fatalf("the guard must flag a reintroduced escape: %s", reintroduced)
			}
		}
		for _, unrelated := range []string{
			`s = strings.ReplaceAll(s, "a", "b")`,
			`s = strings.ReplaceAll(s, "~", "-")`,
			`s = strings.ReplaceAll(s, "\\", "/")`,
			`key := strings.ReplaceAll(rel, "/", "~")`,          // flattening a path for a cache key is not an escape
			`key := strings.NewReplacer("/", "~").Replace(rel)`, // the same flattening, spelled the other way
			`r := strings.NewReplacer("/", "-", "~", "_")`,
			`// escapes ("~0" for "~", "~1" for "/") are supported.`,
			`// e.g. strings.ReplaceAll(s, "~", "~0") — do not hand-roll this; call jsonkeys.EscapeToken.`,
		} {
			if site, ok := pointerEscapeSite(unrelated); ok {
				t.Fatalf("the guard must not flag an unrelated replace or a comment quoting the call: %s (matched %s)", unrelated, site)
			}
		}
	})
}
