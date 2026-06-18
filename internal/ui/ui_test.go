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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.in); got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
