package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
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

// TestHashAtPointerSeesOnlyRootedPointers keeps a now-dead branch dead.
//
// hashAtPointer used to call a private getJSONPointer that answered a pointer
// with no leading "/" by returning the WHOLE document; it now calls
// jsonkeys.Get, which treats a bare "abc" as "/abc". That difference is
// unreachable, and this test is what makes "unreachable" a fact rather than a
// claim: hashAtPointer's only pointer source is collectStateSeedPointers, which
// is render.CollectPointers, which builds every pointer by prefixing "/".
//
// If a future caller feeds hashAtPointer pointers from somewhere else — the
// state file, say, which is user-editable — this test is the place that stops
// being true, and the rooted-pointer guard getPointerValue keeps is the pattern
// to copy.
func TestHashAtPointerSeesOnlyRootedPointers(t *testing.T) {
	// Keys chosen to exercise the escape path, including ones that would
	// produce an unrooted pointer if the "/" prefix were ever dropped.
	dest := map[string]any{
		"mcpServers": map[string]any{"a/b": map[string]any{"command": "x"}, "a~b": "y"},
		"scalar":     "s",
		"":           "empty key",
		"/leading":   "slash key",
	}
	ptrs := collectStateSeedPointers(dest)
	if len(ptrs) == 0 {
		t.Fatal("no pointers collected — the test would pass vacuously")
	}
	for _, p := range ptrs {
		if !strings.HasPrefix(p, "/") {
			t.Errorf("collectStateSeedPointers emitted an unrooted pointer %q; "+
				"hashAtPointer's jsonkeys.Get would resolve it as a top-level key lookup", p)
		}
		// And every one of them must actually resolve, which is the other half
		// of the claim: a pointer whose escaping disagreed with the document's
		// real keys would hash "" forever and report phantom drift.
		if h := hashAtPointer(dest, p); h == "" {
			t.Errorf("pointer %q collected from the document does not resolve in it", p)
		}
	}

	// The fixture's top-level "" key makes collectStateSeedPointers emit the
	// pointer "/" — so "/" CAN come from CollectPointers, and this is where the
	// one contract the unification changed is pinned on the import side: "/"
	// names the WHOLE document (as internal/render always read it), not the ""
	// key the deleted CLI resolver answered with. Requiring the whole-document
	// hash is what makes the assertion load-bearing; "hashes to something" held
	// under both readings.
	if !slices.Contains(ptrs, "/") {
		t.Fatalf("the fixture's top-level %q key must yield the pointer \"/\"; got %q", "", ptrs)
	}
	whole, err := json.Marshal(dest)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(whole)
	if got, want := hashAtPointer(dest, "/"), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("hashAtPointer(dest, \"/\") = %s, want the whole-document hash %s "+
			"(a resolver answering \"/\" with dest[\"\"] hashes \"empty key\" instead)", got, want)
	}
}

// TestGetPointerValueRefusesAnUnrootedPointer pins the one thing
// getPointerValue does that jsonkeys.Get deliberately does not.
//
// jsonkeys.Get is lenient about the leading "/" (RFC 6901 leaves a malformed
// pointer undefined, and the key-merge machinery builds its own pointers). This
// caller cannot be: planwalk resolves pointers read back from the state FILE,
// which a user can hand-edit, and answering `"mcpServers"` — a string that is
// not a pointer — with a top-level key lookup would silently compare the wrong
// value and report clean or drifted on the strength of it.
func TestGetPointerValueRefusesAnUnrootedPointer(t *testing.T) {
	m := map[string]any{
		"mcpServers": map[string]any{"a": "v"},
		"top":        "t",
	}
	for _, ptr := range []string{"mcpServers", "top", "mcpServers/a", ""} {
		if got := getPointerValue(m, ptr); got != nil {
			t.Errorf("getPointerValue(%q) = %#v, want nil — a string with no leading %q is not a pointer", ptr, got, "/")
		}
	}
	// The rooted forms still resolve, so the guard is a guard and not a mute.
	if got := getPointerValue(m, "/top"); got != "t" {
		t.Errorf(`getPointerValue("/top") = %#v, want "t"`, got)
	}
	if got := getPointerValue(m, "/mcpServers/a"); got != "v" {
		t.Errorf(`getPointerValue("/mcpServers/a") = %#v, want "v"`, got)
	}
	// A lone "/" is rooted and names the WHOLE document (jsonkeys.Get yields
	// no tokens for it), not the "" key RFC 6901 assigns it — the one contract
	// the copy this replaced did not share. Pinned so the change is deliberate.
	if got := getPointerValue(m, "/"); !reflect.DeepEqual(got, m) {
		t.Errorf(`getPointerValue("/") = %#v, want the whole document`, got)
	}
}

// TestForeignPointersInOurSections pins the two things unimportedDestPointers'
// walk must get right before its result reaches the user.
//
// First, the section set it filters against is built with the SAME escaping
// collectStateSeedPointers uses for its pointers' first segment. A key holding
// a '/' or a '~' escapes to something other than itself, so a section set
// keyed by the raw key would never match the seed pointer for it, and every
// foreign pointer under that section would go unreported. This exercises the
// PRODUCTION function, not a re-spelling of its section set: drop the escape
// there and the escaped sections below vanish from the result.
//
// Second, the result is sorted. It is printed verbatim as the "exists in the
// destination but agentsync did not capture" list, and the walk underneath
// ranges a map; the fixture has eight foreign pointers so that map order
// cannot come out sorted by accident (1 in 40320) and the expected slice is
// compared WITHOUT sorting it first.
func TestForeignPointersInOurSections(t *testing.T) {
	ours := map[string]any{
		"a~b":   map[string]any{"x": 1},
		"c/d":   map[string]any{"y": 2},
		"plain": map[string]any{"z": 3},
		"~":     map[string]any{"w": 4},
	}
	existing := map[string]any{
		"a~b":       map[string]any{"x": 1, "f2": true, "f1": true},
		"c/d":       map[string]any{"y": 2, "f4": true, "f3": true},
		"plain":     map[string]any{"z": 3, "f6": true, "f5": true},
		"~":         map[string]any{"w": 4, "f8": true, "f7": true},
		"unmodeled": map[string]any{"v": 5}, // a section ours does not render: out of scope
	}
	got := foreignPointersInOurSections(ours, existing)
	want := []string{
		"/a~0b/f1", "/a~0b/f2",
		"/c~1d/f3", "/c~1d/f4",
		"/plain/f5", "/plain/f6",
		"/~0/f7", "/~0/f8",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("foreign pointers = %q, want %q "+
			"(an escaped section gone missing means the section set was keyed by the raw key; "+
			"the right set in another order means the result is no longer sorted)", got, want)
	}
}

// TestImportersSpeakTheirLabelOnANamedMiss pins the label WIRING at the five
// real call sites, which TestMatchImportable cannot: that table hands the
// helper its own (kind, label) pairs, so it proves the helper and not the
// strings each importer passes. Every tail starts with matchImportable, so an
// empty canonical and a name that is not there refuse before home is touched.
func TestImportersSpeakTheirLabelOnANamedMiss(t *testing.T) {
	type importer func(io *importIO, home string, c source.Canonical, name string) ([]string, error)
	for _, tc := range []struct {
		name string
		run  importer
		want string
	}{
		{"mcp", importMCP, `mcp server "nope" not found in native config`},
		{"skill", importSkill, `skill "nope" not found in native config`},
		{"subagent", importSubagent, `subagent "nope" not found in native config`},
		{"command", importCommand, `command "nope" not found in native config`},
		{"lsp", importLSP, `lsp server "nope" not found in native config`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			io, _ := newImportIOForTest()
			_, err := tc.run(io, "", source.Canonical{}, "nope")
			if err == nil {
				t.Fatal("naming an absent item must refuse")
			}
			if err.Error() != tc.want {
				t.Fatalf("err = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}
