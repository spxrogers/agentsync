package state_test

import (
	"encoding/json"
	"testing"

	"github.com/spxrogers/agentsync/internal/state"
)

// TestKey_EncodingIsExact pins the WIRE FORMAT byte-for-byte. targets.json is an
// on-disk artifact: asserting only that the struct round-trips would let the
// encoding change silently and orphan every existing state file.
func TestKey_EncodingIsExact(t *testing.T) {
	tests := []struct {
		name string
		key  state.Key
		want string
	}{
		{
			name: "user-scope whole file",
			key:  state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude/settings.json"},
			want: "as1|6:claude|4:user|0:|29:${HOME}/.claude/settings.json|0:",
		},
		{
			name: "project-scope whole file",
			key: state.Key{
				Agent: "claude", Scope: "project",
				Project: "${HOME}/work/app", Path: "${HOME}/work/app/.mcp.json",
			},
			want: "as1|6:claude|7:project|16:${HOME}/work/app|26:${HOME}/work/app/.mcp.json|0:",
		},
		{
			name: "colon-bearing project root, with a pointer",
			key: state.Key{
				Agent: "claude", Scope: "project",
				Project: "${HOME}/work/app:staging",
				Path:    "${HOME}/work/app:staging/.mcp.json",
				Pointer: "/mcpServers/github",
			},
			want: "as1|6:claude|7:project|24:${HOME}/work/app:staging|34:${HOME}/work/app:staging/.mcp.json|18:/mcpServers/github",
		},
		{
			// The length prefix is a BYTE count, and only a multibyte field can
			// say so: every other row here is ASCII, where len and rune count
			// agree, so a utf8.RuneCountInString implementation would satisfy
			// them all. "héllo.md" is 8 runes but 9 bytes, and "日本" is 2 runes
			// but 6 — the wanted prefixes below are the byte counts.
			name: "multibyte fields are length-prefixed in BYTES, not runes",
			key: state.Key{
				Agent: "claude", Scope: "project",
				Project: "${HOME}/日本", Path: "${HOME}/日本/héllo.md", Pointer: "/mcpServers/héllo",
			},
			want: "as1|6:claude|7:project|14:${HOME}/日本|24:${HOME}/日本/héllo.md|18:/mcpServers/héllo",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.key.String(); got != tc.want {
				t.Fatalf("String() =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// TestKey_RoundTripsAdversarialFields proves ParseKey is a left inverse of
// String for ANY byte content — which is what makes String injective. The v1
// ':'-joined format failed exactly here.
func TestKey_RoundTripsAdversarialFields(t *testing.T) {
	tests := []struct {
		name string
		key  state.Key
	}{
		{"zero value", state.Key{}},
		{"colons everywhere", state.Key{Agent: "a:b", Scope: "user:x", Project: "p:q", Path: "x:y", Pointer: "/p:q"}},
		{"separator byte in fields", state.Key{Agent: "a|b", Project: "|", Path: "p|q|", Pointer: "|"}},
		{"pointer-lookalike inside a path", state.Key{Path: "a:/b", Pointer: "/c"}},
		{"field that looks like its own prefix", state.Key{Agent: "12:abc", Path: "0:"}},
		{"NUL and invalid utf-8", state.Key{Agent: "a\x00b", Path: "\xff\xfe"}},
		{"portable home path", state.Key{Agent: "codex", Scope: "user", Path: "${HOME}/.codex/config.toml", Pointer: "/mcp_servers/gh"}},
		// Valid multibyte UTF-8, where len(f) and utf8.RuneCountInString(f)
		// DIVERGE. Every other row is ASCII or invalid UTF-8 ("\xff\xfe" has
		// rune count 2 as well as length 2), so without this one a rune-counting
		// String would round-trip the whole table.
		{"valid multibyte utf-8", state.Key{Agent: "héllo", Scope: "user", Project: "日本", Path: "${HOME}/héllo/日本.md", Pointer: "/mcpServers/héllo"}},
		// A field whose CONTENT is itself a well-formed encoded key: the decoder
		// must consume it as opaque bytes, never re-enter the grammar.
		{"field holding a valid encoded key", state.Key{Agent: "as1|1:a|1:b|0:|0:|0:", Path: "as1|1:a|1:b|0:|0:|0:"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := state.ParseKey(tc.key.String())
			if err != nil {
				t.Fatalf("ParseKey(%q): %v", tc.key.String(), err)
			}
			if got != tc.key {
				t.Fatalf("round-trip lost data: got %+v, want %+v", got, tc.key)
			}
		})
	}
}

// TestKey_HistoricalCollisionIsGone is the regression for issue #227. Under the
// v1 format these two keys were
//
//	claude:project:${HOME}/work/app:${HOME}/work/app/.mcp.json
//	claude:project:${HOME}/work/app:staging:${HOME}/work/app:staging/.mcp.json
//
// and the second STRING-PREFIX-matched the PRUNE PREFIX an apply of the first
// project scoped its state sweep with ("claude:project:${HOME}/work/app:"), so
// applying the first project deleted the second project's ownership.
//
// The v2 counterpart of that prune prefix is InTree — no consumer builds a key
// prefix any more — so InTree is what the assertions below pin. (Asserting the
// two v2 ENCODINGS do not prefix one another would be decorative: length
// prefixes make the encoding injective for any bytes, and the encodings of
// these two keys happen not to prefix each other under v1 either. What went
// wrong was the prune prefix, not the key-to-key relation.)
func TestKey_HistoricalCollisionIsGone(t *testing.T) {
	p1 := state.Key{Agent: "claude", Scope: "project", Project: "${HOME}/work/app", Path: "${HOME}/work/app/.mcp.json"}
	p2 := state.Key{Agent: "claude", Scope: "project", Project: "${HOME}/work/app:staging", Path: "${HOME}/work/app:staging/.mcp.json"}

	if p1.String() == p2.String() {
		t.Fatal("distinct destinations must not share an encoding")
	}
	if !p1.InTree("claude", "project", "${HOME}/work/app") {
		t.Fatal("p1 must be in its own tree")
	}
	if p2.InTree("claude", "project", "${HOME}/work/app") {
		t.Fatal("p2 must NOT be in the sibling project's tree — this is the bug")
	}
}

func TestKey_ParseRejectsMalformed(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"a v1 legacy key", "claude:user::${HOME}/.claude.json"},
		{"wrong marker", "as2|6:claude|4:user|0:|1:x|0:"},
		// The marker itself, not just its bytes: every other row here also trips
		// the '|' / length-prefix checks, so none of them discriminates the
		// marker. This one is perfectly framed and differs ONLY by the missing
		// "as1" — swapping ParseKey's strings.CutPrefix for strings.TrimPrefix
		// leaves the rest of the suite green but fails this row.
		{"well-framed but marker-less", "|6:claude|4:user|0:|1:x|0:"},
		{"missing separator", "as16:claude|4:user|0:|1:x|0:"},
		{"missing length prefix", "as1|claude|4:user|0:|1:x|0:"},
		{"non-canonical length", "as1|06:claude|4:user|0:|1:x|0:"},
		{"negative length", "as1|-1:claude|4:user|0:|1:x|0:"},
		{"truncated payload", "as1|6:clau"},
		{"too few fields", "as1|6:claude|4:user|0:|1:x"},
		{"trailing bytes", "as1|6:claude|4:user|0:|1:x|0:junk"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := state.ParseKey(tc.in); err == nil {
				t.Fatalf("ParseKey(%q) should fail; got %+v", tc.in, got)
			}
		})
	}
}

// TestKey_ConstructorsPortabilize pins that the constructors — the ONLY place
// paths.HomeRelative is called for a state key — produce the portable form, and
// that AbsPath is its inverse.
func TestKey_ConstructorsPortabilize(t *testing.T) {
	const userHome = "/home/alice"
	k := state.NewFileKey(userHome, "claude", "user", "", "/home/alice/.claude.json")
	want := state.Key{Agent: "claude", Scope: "user", Project: "", Path: "${HOME}/.claude.json"}
	if k != want {
		t.Fatalf("NewFileKey = %+v, want %+v", k, want)
	}
	if got := k.AbsPath(userHome); got != "/home/alice/.claude.json" {
		t.Fatalf("AbsPath = %q, want %q", got, "/home/alice/.claude.json")
	}
	// A path OUTSIDE the user's home stays absolute (paths.HomeRelative's contract).
	out := state.NewPointerKey(userHome, "claude", "project", "/srv/repo", "/srv/repo/.mcp.json", "/mcpServers/gh")
	wantOut := state.Key{
		Agent: "claude", Scope: "project", Project: "/srv/repo",
		Path: "/srv/repo/.mcp.json", Pointer: "/mcpServers/gh",
	}
	if out != wantOut {
		t.Fatalf("NewPointerKey = %+v, want %+v", out, wantOut)
	}
}

// TestKey_IsAJSONObjectKey pins that a map[Key]V marshals to an ordinary
// string-keyed JSON object and unmarshals back — the property that lets
// targets.json keep its shape while the Go type gets stricter.
func TestKey_IsAJSONObjectKey(t *testing.T) {
	k := state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude.json"}
	in := map[state.Key]string{k: "v"}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"` + k.String() + `":"v"}`; string(data) != want {
		t.Fatalf("Marshal = %s, want %s", data, want)
	}
	var out map[state.Key]string
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out[k] != "v" {
		t.Fatalf("round-trip lost the entry: %+v", out)
	}
	// A legacy key must FAIL the unmarshal rather than decode to something wrong.
	if err := json.Unmarshal([]byte(`{"claude:user::x":"v"}`), &out); err == nil {
		t.Fatal("a v1 key must not unmarshal into a map[Key]V")
	}
}
