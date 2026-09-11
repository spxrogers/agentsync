package cli

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
)

// newImportIOForTest builds an importIO whose out and err both land in one
// buffer, so a test can assert on the interleaved transcript the user sees.
func newImportIOForTest() (*importIO, *bytes.Buffer) {
	var out bytes.Buffer
	p := &ui.Printer{Out: &out, Err: &out}
	return &importIO{p: p, out: &out, err: &out}, &out
}

// TestImportIONotefEqualsNote pins that the two INFO-tier emitters are ONE
// emitter, and that collapsing them changed no byte.
//
// They were independent one-liners over ui.Fdiagf, differing only in that note
// wrapped its message in a "%s" and notef passed the format through. That is a
// distinction WITHOUT a difference: Fdiagf itself Sprintf's, so
// Fdiagf(w, lvl, "%s", Sprintf(f, a...)) and Fdiagf(w, lvl, f, a...) produce
// the same bytes for every input. Two spellings of one tier meant a change to
// that tier — its label, its stream, its prefix — had to be made twice or be
// made inconsistent. This test is the equality the collapse rests on.
//
// NOTE for anyone tempted to "fix" notef so a PREFORMATTED message survives
// verbatim: it does not, it never did, and `go vet` is what stops that from
// mattering — the printf analyzer recognizes notef as a wrapper and rejects a
// non-constant format at the call site. note(msg) is the spelling for an
// already-formatted string, which is why it is the one that survives.
func TestImportIONotefEqualsNote(t *testing.T) {
	cases := []struct {
		name   string
		format string
		args   []any
	}{
		{name: "plain", format: "retired canonical hooks/x.toml", args: nil},
		{name: "with verbs", format: "could not reverse markers (%v) for %q", args: []any{"boom", "PreToolUse"}},
		// A format carrying a literal %% must come out identical from both spellings.
		{name: "percent in the message", format: "50%% done: %s", args: []any{"reading"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ioN, bufN := newImportIOForTest()
			ioF, bufF := newImportIOForTest()

			// note(Sprintf(...)) is what a caller that formats first writes.
			msg := fmt.Sprintf(tc.format, tc.args...)
			ioN.note(msg)
			ioF.notef(tc.format, tc.args...)

			if bufN.String() != bufF.String() {
				t.Fatalf("note and notef must emit the same bytes:\n note:  %q\n notef: %q", bufN.String(), bufF.String())
			}
			if strings.Contains(bufF.String(), "MISSING") || strings.Contains(bufF.String(), "EXTRA") {
				t.Fatalf("notef mangled the message: %q", bufF.String())
			}
		})
	}
}

// TestMatchImportable pins the head all five per-component importers share.
//
// Three behaviours are load-bearing and were previously re-typed five times:
// an empty name means "all"; an empty RESULT with no error is the "nothing to
// do" answer (a bulk import of an agent with no skills is a success); and a
// NAMED item that is absent is a refusal worded with the kind's spoken label,
// which differs from its internal key for exactly the two server kinds.
func TestMatchImportable(t *testing.T) {
	all := []source.Skill{{Name: "alpha"}, {Name: "beta"}}
	key := func(sk source.Skill) string { return sk.Name }

	t.Run("empty name matches all", func(t *testing.T) {
		io, _ := newImportIOForTest()
		got, err := matchImportable(io, "skill", "skill", "", all, key)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if !reflect.DeepEqual(got, all) {
			t.Fatalf("got %v, want %v", got, all)
		}
	})

	t.Run("named match narrows to one", func(t *testing.T) {
		io, _ := newImportIOForTest()
		got, err := matchImportable(io, "skill", "skill", "beta", all, key)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 1 || got[0].Name != "beta" {
			t.Fatalf("got %v, want just beta", got)
		}
	})

	t.Run("bulk with nothing to import is not an error", func(t *testing.T) {
		io, _ := newImportIOForTest()
		got, err := matchImportable(io, "skill", "skill", "", nil, key)
		if err != nil {
			t.Fatalf("an agent with no skills is a successful no-op import; got err %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %v, want nothing", got)
		}
	})

	t.Run("named miss refuses with the spoken label", func(t *testing.T) {
		// The label, not the key: "mcp server %q not found", never "mcp %q".
		for _, tc := range []struct{ kind, label, want string }{
			{"skill", "skill", `skill "nope" not found in native config`},
			{"mcp", "mcp server", `mcp server "nope" not found in native config`},
			{"lsp", "lsp server", `lsp server "nope" not found in native config`},
		} {
			io, _ := newImportIOForTest()
			_, err := matchImportable(io, tc.kind, tc.label, "nope", all, key)
			if err == nil {
				t.Fatalf("%s: naming an absent item must refuse", tc.kind)
			}
			if err.Error() != tc.want {
				t.Fatalf("%s: err = %q, want %q", tc.kind, err.Error(), tc.want)
			}
		}
	})

	// The two plugin-filter subtests run over the MCP kind, whose key ("mcp")
	// and spoken label ("mcp server") DIFFER. skipPluginProvided must receive
	// the KEY — it looks up "mcp/alpha" in the plugin-provided set and speaks
	// `mcp "alpha"` — so a helper that handed it the label would drop nothing
	// here and refuse nothing, which a skill-only fixture (key == label) could
	// never notice.
	servers := []source.MCPServer{{ID: "alpha"}, {ID: "beta"}}
	serverKey := func(m source.MCPServer) string { return m.ID }

	t.Run("plugin-provided entries are dropped in bulk", func(t *testing.T) {
		io, buf := newImportIOForTest()
		io.pluginProvided = map[string]string{"mcp/alpha": "toolkit@mp"}
		got, err := matchImportable(io, "mcp", "mcp server", "", servers, serverKey)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 1 || got[0].ID != "beta" {
			t.Fatalf("got %v, want just beta", got)
		}
		if !strings.Contains(buf.String(), "toolkit@mp") {
			t.Fatalf("the skip must say which plugin provides it; got %q", buf.String())
		}
		if !strings.Contains(buf.String(), `mcp "alpha"`) {
			t.Fatalf("the skip names the item by KIND (`mcp \"alpha\"`), not by label; got %q", buf.String())
		}
	})

	t.Run("naming a plugin-provided item refuses", func(t *testing.T) {
		io, _ := newImportIOForTest()
		io.pluginProvided = map[string]string{"mcp/alpha": "toolkit@mp"}
		_, err := matchImportable(io, "mcp", "mcp server", "alpha", servers, serverKey)
		if err == nil {
			t.Fatal("asking for a plugin-provided item by name must refuse, not silently import nothing")
		}
		if !strings.HasPrefix(err.Error(), `mcp "alpha" is provided by the plugin "toolkit@mp"`) {
			t.Fatalf("the refusal comes from skipPluginProvided and names the item by KIND; got %q", err)
		}
	})

	t.Run("a broken plugin filter fails closed", func(t *testing.T) {
		io, _ := newImportIOForTest()
		io.pluginProvidedErr = errors.New("cache gone")
		if _, err := matchImportable(io, "skill", "skill", "", all, key); err == nil {
			t.Fatal("an unusable plugin filter must refuse the import, not proceed with an empty skip set")
		}
	})
}
