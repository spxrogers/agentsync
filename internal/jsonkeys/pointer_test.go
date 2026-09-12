package jsonkeys_test

import (
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// TestEscapeUnescapeToken pins the ORDER of the two ReplaceAlls in both
// directions — the whole reason RFC 6901 escaping belongs in one place.
//
// Escaping must do '~' first: doing '/' first turns "a/b" into "a~1b", and the
// tilde pass then re-escapes the tilde IT just introduced into "a~01b", which
// decodes back to the literal "a~1b" rather than "a/b".
//
// Decoding must do "~1" first, the mirror: decoding "~0" first turns the token
// "~01" into "~1", and the next pass turns that into "/" — where the token
// means the two characters "~1".
//
// This table absorbed internal/cli's TestUnescapeJSONPointer, which pinned the
// decode half against the third of four copies of the same four lines.
func TestEscapeUnescapeToken(t *testing.T) {
	t.Run("unescape", func(t *testing.T) {
		for _, tc := range []struct{ in, want string }{
			{"plain", "plain"},
			{"with~1slash", "with/slash"},
			{"with~0tilde", "with~tilde"},
			{"~01", "~1"},
			{"~1~0", "/~"},
			{"", ""},
		} {
			if got := jsonkeys.UnescapeToken(tc.in); got != tc.want {
				t.Errorf("UnescapeToken(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})

	t.Run("escape", func(t *testing.T) {
		for _, tc := range []struct{ in, want string }{
			{"plain", "plain"},
			{"with/slash", "with~1slash"},
			{"with~tilde", "with~0tilde"},
			{"~1", "~01"},
			{"/~", "~1~0"},
			{"", ""},
		} {
			if got := jsonkeys.EscapeToken(tc.in); got != tc.want {
				t.Errorf("EscapeToken(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})

	// Round-trip: every key an agent config can hold must survive
	// escape→unescape unchanged. An order mistake in EITHER direction breaks
	// this for the adversarial keys, which is exactly the phantom-drift bug a
	// managed MCP server id containing '~' or '/' used to produce.
	t.Run("round trip", func(t *testing.T) {
		for _, k := range []string{
			"", "plain", "a/b", "a~b", "~1", "~0", "~01", "a~1b", "a~0b",
			"~", "/", "//", "~~", "mcpServers", "amp.mcpServers", "a/b~c/~1d",
		} {
			if got := jsonkeys.UnescapeToken(jsonkeys.EscapeToken(k)); got != k {
				t.Errorf("round trip of %q: escaped %q, decoded back to %q",
					k, jsonkeys.EscapeToken(k), got)
			}
		}
	})
}

// TestSplitPointerAndGet pins the resolver's contract, including the two edges
// every previous copy spelled differently: the whole-document pointer, and the
// difference between an absent pointer and a present null.
func TestSplitPointerAndGet(t *testing.T) {
	t.Run("split", func(t *testing.T) {
		for _, tc := range []struct {
			in   string
			want []string
		}{
			{"", nil},
			{"/", nil},
			{"/a", []string{"a"}},
			{"a", []string{"a"}}, // the leading slash is optional here
			{"/a/b", []string{"a", "b"}},
			{"/mcpServers/a~1b", []string{"mcpServers", "a/b"}},
			{"/mcpServers/a~0b", []string{"mcpServers", "a~b"}},
			// An empty reference token is legal (RFC 6901 §3): "/a//b" names
			// the "" key under "a".
			{"/a//b", []string{"a", "", "b"}},
		} {
			if got := jsonkeys.SplitPointer(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SplitPointer(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		}
	})

	doc := map[string]any{
		"mcpServers": map[string]any{
			"a/b":    map[string]any{"command": "x"},
			"a~b":    "scalar",
			"nulled": nil,
		},
		"scalar": "s",
	}

	t.Run("get", func(t *testing.T) {
		for _, tc := range []struct {
			ptr     string
			want    any
			present bool
		}{
			{"", doc, true}, // the whole document
			// A lone "/" is the whole document too (no tokens), NOT the "" key
			// RFC 6901 assigns it — the contract the two CLI resolvers moved to.
			{"/", doc, true},
			{"/scalar", "s", true},
			{"/mcpServers/a~1b/command", "x", true},
			{"/mcpServers/a~0b", "scalar", true},
			// Present with a null value: (nil, TRUE). RecordOpsState relies on
			// this to hash a key that landed as null rather than skip it.
			{"/mcpServers/nulled", nil, true},
			// Absent: (nil, FALSE).
			{"/mcpServers/missing", nil, false},
			{"/missing", nil, false},
			// Cannot descend through a scalar.
			{"/scalar/deeper", nil, false},
			// The literal escaped key must NOT resolve — that is the phantom
			// drift bug: looking up "a~1b" verbatim finds nothing forever.
			{"/mcpServers/a~1b~1c", nil, false},
		} {
			got, present := jsonkeys.Get(doc, tc.ptr)
			if present != tc.present {
				t.Errorf("Get(%q) presence = %v, want %v", tc.ptr, present, tc.present)
				continue
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Get(%q) = %#v, want %#v", tc.ptr, got, tc.want)
			}
		}
	})
}
