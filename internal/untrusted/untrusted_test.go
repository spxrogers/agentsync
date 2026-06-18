package untrusted

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestText_StringSanitizes pins the safe-by-default property: printing a Text
// through fmt (which invokes String() for a fmt.Stringer) strips terminal
// escapes, so a new print site cannot reintroduce the #93 escape-injection
// class. Break-verified by returning string(t) from String() (the ESC leaks).
func TestText_StringSanitizes(t *testing.T) {
	hostile := Text("plug\x1b[31m\rid\u202egnp")
	if got := hostile.String(); got != "plug[31midgnp" {
		t.Errorf("String() = %q, want sanitized %q", got, "plug[31midgnp")
	}
	// Every fmt verb that consults Stringer must see the sanitized form.
	for _, verb := range []string{"%s", "%v"} {
		if got := fmt.Sprintf(verb, hostile); got != "plug[31midgnp" {
			t.Errorf("fmt.Sprintf(%q) = %q, want sanitized", verb, got)
		}
	}
	// %q quotes the sanitized String() output (no raw ESC survives the quoting).
	if got := fmt.Sprintf("%q", hostile); got != `"plug[31midgnp"` {
		t.Errorf("fmt %%q = %q, want quoted-sanitized", got)
	}
}

// TestText_Unverified is the explicit raw escape hatch: it returns the bytes
// verbatim (no sanitization), for non-display use only.
func TestText_Unverified(t *testing.T) {
	raw := "ev\x1b[31mil"
	if got := Text(raw).Unverified(); got != raw {
		t.Errorf("Unverified() = %q, want raw %q", got, raw)
	}
}

func TestText_Empty(t *testing.T) {
	if !Text("").Empty() {
		t.Error("Text(\"\").Empty() = false, want true")
	}
	if Text("x").Empty() {
		t.Error("Text(\"x\").Empty() = true, want false")
	}
}

// TestJoin pins the Text-slice join used to build one display string from
// several untrusted values (the status/doctor "undeclared native plugins" list):
// every element is rendered through its sanitizing String(), so terminal escapes
// are stripped per element while the caller's separator is emitted verbatim.
func TestJoin(t *testing.T) {
	ts := []Text{Wrap("demo\x1b[31m"), Wrap("ev\ril"), Wrap("ok")}
	got := Join(ts, ", ")
	want := "demo[31m, evil, ok"
	if got != want {
		t.Errorf("Join = %q, want sanitized %q", got, want)
	}
	if got := Join(nil, ", "); got != "" {
		t.Errorf("Join(nil) = %q, want empty", got)
	}
	if got := Join([]Text{Wrap("solo")}, ", "); got != "solo" {
		t.Errorf("Join(single) = %q, want %q (no trailing separator)", got, "solo")
	}
}

// TestText_Wrap pins the boundary constructor: Wrap stores the bytes verbatim
// (Unverified round-trips them) without sanitizing at construction — sanitization
// happens at the display boundary (String), not at ingestion, so the raw value
// stays available for path/lookup use.
func TestText_Wrap(t *testing.T) {
	raw := "demo\x1b[0m@mp"
	w := Wrap(raw)
	if got := w.Unverified(); got != raw {
		t.Errorf("Wrap(%q).Unverified() = %q, want raw round-trip", raw, got)
	}
	if got := w.String(); got != "demo[0m@mp" {
		t.Errorf("Wrap(%q).String() = %q, want sanitized", raw, got)
	}
}

// TestText_WidthVerb guards the alignment property the CLI relies on: a width
// verb (%-Ns, used for padded list columns) applies its padding to the SANITIZED
// String() output, so a stripped rune never throws off the column. The input is
// the 5-rune "ev<ZWSP>il"; the ZWSP is stripped, leaving the 4-rune "evil",
// which is then padded to width 8 → four trailing spaces. Were padding applied
// to the raw value, the 5-rune input would yield only three spaces.
func TestText_WidthVerb(t *testing.T) {
	if got := fmt.Sprintf("%-8s|", Text("ev\u200bil")); got != "evil    |" {
		t.Errorf("%%-8s on Text = %q, want padding applied to sanitized output", got)
	}
}

// TestText_Serialization guards the named-string design's load-bearing
// serialization properties (the reason Text is a defined string type, not a
// struct): TOML/JSON treat it transparently, `omitempty` still elides an empty
// value (no `version = ""` round-trip regression), and the machine surfaces emit
// the RAW value — the --json/-file consumer owns escaping, exactly as the CLI's
// per-site comments state.
func TestText_Serialization(t *testing.T) {
	type spec struct {
		ID      Text `toml:"id" json:"id"`
		Version Text `toml:"version,omitempty" json:"version,omitempty"`
	}

	t.Run("toml omitempty elides empty", func(t *testing.T) {
		out, err := toml.Marshal(spec{ID: "demo@mp"})
		if err != nil {
			t.Fatal(err)
		}
		if got := string(out); got != "id = 'demo@mp'\n" {
			t.Errorf("TOML = %q, want only id (empty version omitted)", got)
		}
	})

	t.Run("toml round-trip", func(t *testing.T) {
		in := spec{ID: "demo@mp", Version: "1.2.3"}
		out, err := toml.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var got spec
		if err := toml.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if got != in {
			t.Errorf("round-trip = %+v, want %+v", got, in)
		}
	})

	t.Run("json marshals RAW (machine contract)", func(t *testing.T) {
		out, err := json.Marshal(spec{ID: "ev\x1b[31mil", Version: "1.0"})
		if err != nil {
			t.Fatal(err)
		}
		// encoding/json renders the ESC as a \u001b unicode escape: the byte is
		// PRESERVED (not stripped, as String() would) — raw fidelity for the machine consumer.
		want := `{"id":"ev\u001b[31mil","version":"1.0"}`
		if got := string(out); got != want {
			t.Errorf("JSON = %q, want raw-preserving %q", got, want)
		}
	})

	t.Run("json round-trip", func(t *testing.T) {
		in := spec{ID: "demo@mp", Version: "9"}
		out, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var got spec
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if got != in {
			t.Errorf("round-trip = %+v, want %+v", got, in)
		}
	})
}
