package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseColorMode(t *testing.T) {
	tests := []struct {
		in      string
		want    ColorMode
		wantErr bool
	}{
		{"", ColorAuto, false},
		{"auto", ColorAuto, false},
		{"always", ColorAlways, false},
		{"never", ColorNever, false},
		{"bogus", ColorAuto, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseColorMode(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseColorMode(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("ParseColorMode(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// A *bytes.Buffer is never a terminal, so auto must resolve to no-color — this
// is the property that keeps every captured-output test byte-stable.
func TestColorResolution(t *testing.T) {
	var buf bytes.Buffer

	if p := New(&buf, &buf, ColorAuto); p.Color() {
		t.Error("auto color on a non-terminal writer should be off")
	}
	if p := New(&buf, &buf, ColorAlways); !p.Color() {
		t.Error("always should force color even on a non-terminal")
	}
	if p := New(&buf, &buf, ColorNever); p.Color() {
		t.Error("never should disable color")
	}
}

// NO_COLOR (any value, even empty) disables auto color, but an explicit
// --color=always still wins.
func TestNoColorEnv(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("NO_COLOR", "")
	if p := New(&buf, &buf, ColorAuto); p.Color() {
		t.Error("NO_COLOR set should disable auto color")
	}
	if p := New(&buf, &buf, ColorAlways); !p.Color() {
		t.Error("--color=always should override NO_COLOR")
	}
}

func TestStyleHelpers(t *testing.T) {
	var buf bytes.Buffer

	plain := New(&buf, &buf, ColorNever)
	if got := plain.Green("ok"); got != "ok" {
		t.Errorf("plain Green(ok) = %q, want unchanged", got)
	}
	if got := plain.Bold(""); got != "" {
		t.Errorf("empty string must never be wrapped, got %q", got)
	}

	colored := New(&buf, &buf, ColorAlways)
	got := colored.Red("drift")
	if !strings.HasPrefix(got, codeRed) || !strings.HasSuffix(got, codeReset) || !strings.Contains(got, "drift") {
		t.Errorf("colored Red(drift) = %q, want wrapped in red + reset", got)
	}
}

func TestSection(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, &buf, ColorNever).Section("Source repo")
	if got := buf.String(); got != "Source repo\n" {
		t.Errorf("plain Section = %q, want %q", got, "Source repo\n")
	}
}

// Pad must count display columns (runes), not bytes, so a multi-byte glyph
// doesn't blow the alignment of the following column.
func TestPad(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"drift", 10, "drift     "},
		{"clean", 5, "clean"},
		{"toolong", 3, "toolong"},
		{"✓ ok", 6, "✓ ok  "}, // 4 runes (glyph counts as one) padded to 6
	}
	for _, tc := range tests {
		got := Pad(tc.in, tc.width)
		if got != tc.want {
			t.Errorf("Pad(%q,%d) = %q, want %q", tc.in, tc.width, got, tc.want)
		}
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"clean ascii passes through", "atlassian@anthropic", "atlassian@anthropic"},
		{"empty", "", ""},
		{"non-ascii letters preserved", "café-名前", "café-名前"},
		{"ordinary spaces preserved", "a b  c", "a b  c"},
		{"ESC + CSI introducer stripped, params left inert", "\x1b[31mRED\x1b[0m", "[31mRED[0m"},
		{"carriage return stripped", "before\rafter", "beforeafter"},
		{"newline and tab stripped", "a\nb\tc", "abc"},
		{"BEL and backspace stripped", "x\x07\x08y", "xy"},
		{"DEL stripped", "a\x7fb", "ab"},
		{"C1 control (U+009B CSI) stripped", "a\u009bb", "ab"},
		{"OSC title-set neutralized (ESC and BEL gone)", "\x1b]0;pwned\x07", "]0;pwned"},
		{"pure control string collapses to empty", "\x1b\r\n\t", ""},
		{"invalid UTF-8 byte normalized to U+FFFD", "a\xffb", "a\ufffdb"},
		{"pre-existing U+FFFD preserved (rebuild is idempotent)", "a\ufffdb", "a\ufffdb"},
		// Explicit bidi formatting controls (Trojan Source / CVE-2021-42574).
		// Cover both fences of the override range: U+202A (LRE) and U+202E (RLO).
		{"LRE (U+202A) bidi embedding stripped (range lower fence)", "a\u202ab", "ab"},
		{"RLO (U+202E) bidi override stripped (range upper fence)", "user\u202egpj.evil", "usergpj.evil"},
		{"LRO (U+202D) bidi override stripped", "a\u202db", "ab"},
		{"bidi embedding pair (U+202B/U+202C) stripped", "a\u202bb\u202cc", "abc"},
		{"bidi isolates (U+2066\u2013U+2069) stripped", "a\u2066b\u2069c\u2067d\u2068e", "abcde"},
		// Zero-width / invisible format runes.
		{"zero-width space (U+200B) stripped", "a\u200bb", "ab"},
		{"ZWNJ/ZWJ (U+200C/U+200D) stripped", "a\u200cb\u200dc", "abc"},
		{"zero-width no-break space / BOM (U+FEFF) stripped", "a\ufeffb", "ab"},
		// Mixed strip+preserve: prove a legitimate RTL letter survives WHILE the
		// explicit override embedded between letters is removed (non-vacuous).
		{"RTL letters survive while embedded override stripped", "\u0645\u202e\u0631", "\u0645\u0631"},
		// Legitimate non-Latin names survive byte-for-byte: implicit RTL/CJK is
		// not an explicit formatting control, so it is preserved.
		{"Arabic name preserved", "\u0645\u0631\u062d\u0628\u0627", "\u0645\u0631\u062d\u0628\u0627"},
		{"Hebrew name preserved", "\u05e9\u05dc\u05d5\u05dd", "\u05e9\u05dc\u05d5\u05dd"},
		{"CJK name preserved", "\u540d\u524d-\u30d7\u30e9\u30b0\u30a4\u30f3", "\u540d\u524d-\u30d7\u30e9\u30b0\u30a4\u30f3"},
		{"ordinary RTL letters with implicit bidi preserved", "abc-\u0645\u0631\u062d\u0628\u0627", "abc-\u0645\u0631\u062d\u0628\u0627"},
		// Boundary preservation: runes adjacent to / just outside the stripped
		// ranges, and benign format runes, must NOT be over-stripped. These guard
		// the exact range fences in isDeceptiveFormat against an accidental widening.
		{"implicit marks U+200E/U+200F (LRM/RLM) preserved", "a\u200eb\u200fc", "a\u200eb\u200fc"},
		{"U+200A (hair space, below ZW range) preserved", "a\u200ab", "a\u200ab"},
		{"U+200E adjacent above ZW range preserved", "a\u200eb", "a\u200eb"},
		{"U+2029 (below override range) preserved", "a\u2029b", "a\u2029b"},
		{"U+202F (above override range) preserved", "a\u202fb", "a\u202fb"},
		{"U+2065 (below isolate range) preserved", "a\u2065b", "a\u2065b"},
		{"U+206A (above isolate range) preserved", "a\u206ab", "a\u206ab"},
		{"U+2060 (word joiner) preserved", "a\u2060b", "a\u2060b"},
		{"U+00A0 (NBSP) preserved", "a\u00a0b", "a\u00a0b"},
		{"U+FEFE (below BOM) preserved", "a\ufefeb", "a\ufefeb"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.in); got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
