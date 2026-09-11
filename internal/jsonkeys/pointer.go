package jsonkeys

import "strings"

// RFC 6901 reference-token escaping lives here and ONLY here.
//
// Before #235 there were five hand-rolled copies of these four lines — in this
// package, in internal/render (twice, over a hand-written replaceAll), and in
// three internal/cli helpers. Each copy had to get the ORDER right in both
// directions, and the order is not symmetric: escaping does '~' first, decoding
// does "~1" first. Get either backwards and "~01" decodes to "/" instead of the
// "~1" the user wrote — a managed MCP server id containing a '/' or '~' then
// looks up a key that does not exist and reports phantom drift forever. Five
// chances to get it wrong, one place to fix it.

// EscapeToken encodes one string as an RFC 6901 §3 reference token: '~' becomes
// "~0" and '/' becomes "~1".
//
// ORDER IS LOAD-BEARING: '~' must be escaped FIRST. Escaping '/' first would
// turn "a/b" into "a~1b" and the subsequent '~' pass would re-escape that
// introduced tilde into "a~01b", which decodes back to "a~1b" — a silently
// corrupted key.
func EscapeToken(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
}

// UnescapeToken decodes one RFC 6901 §3 reference token: "~1" is '/' and "~0"
// is '~'.
//
// ORDER IS LOAD-BEARING, and it is the MIRROR of EscapeToken's: "~1" must be
// decoded FIRST. Decoding "~0" first would turn the literal token "~01" into
// "~1" and the subsequent pass would decode that into "/" — where the token
// means the two characters "~1".
func UnescapeToken(tok string) string {
	return strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
}

// SplitPointer splits a JSON pointer into its decoded reference tokens. A
// leading "/" is optional; the empty pointer (the whole document) yields no
// tokens.
func SplitPointer(ptr string) []string {
	ptr = strings.TrimPrefix(ptr, "/")
	if ptr == "" {
		return nil
	}
	raw := strings.Split(ptr, "/")
	out := make([]string, len(raw))
	for i, s := range raw {
		out[i] = UnescapeToken(s)
	}
	return out
}

// Get resolves ptr against m, walking only through objects. The bool is false
// when any segment is absent or when an intermediate value is not an object —
// distinct from a present value that happens to be null, which returns
// (nil, true). The empty pointer names the whole document and returns (m, true).
//
// This is the one pointer resolver. internal/render's RecordOpsState relies on
// the presence signal to avoid recording a pointer that never landed on disk;
// the CLI's drift diagnostics discard it and treat absent as nil.
//
// `/` yields no tokens and names the WHOLE document, not the `""` key RFC 6901
// assigns it. The two CLI resolvers this replaced answered `/` with the `""`
// key; `/` never comes from CollectPointers, only from a hand-edited state key.
func Get(m map[string]any, ptr string) (any, bool) {
	var cur any = m
	for _, p := range SplitPointer(ptr) {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, present := mm[p]
		if !present {
			return nil, false
		}
		cur = v
	}
	return cur, true
}
