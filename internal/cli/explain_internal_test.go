package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/ui"
)

// ansiSeq matches an SGR escape (color/bold/faint reset) so a test can measure
// the *visible* layout of styled output independent of the color codes.
var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiSeq.ReplaceAllString(s, "") }

// TestSkipLabel covers all arms of skipLabel: a named skip renders
// "<kind> <name>", an unnamed one (e.g. an unrecognized hook event with no
// name) falls back to the bare kind, and the internal "-frontmatter" suffix is
// stripped so the user-facing label names the component kind plainly.
func TestSkipLabel(t *testing.T) {
	tests := []struct {
		name string
		in   render.SkipDetail
		want string
	}{
		{"named", render.SkipDetail{Component: "lsp", Name: "gopls"}, "lsp gopls"},
		{"unnamed", render.SkipDetail{Component: "hook"}, "hook"},
		{"frontmatter suffix stripped", render.SkipDetail{Component: "subagent-frontmatter", Name: "reviewer"}, "subagent reviewer"},
		{"command-frontmatter stripped", render.SkipDetail{Component: "command-frontmatter", Name: "deploy"}, "command deploy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipLabel(tc.in); got != tc.want {
				t.Errorf("skipLabel(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsReducedSkip pins the classifier that splits a skip into a field-level
// "reduced" (the component still rendered) vs a whole-component "dropped": only
// the adapter "-frontmatter" component strings are reductions; every other
// component kind — and an empty/unknown one — is a drop.
func TestIsReducedSkip(t *testing.T) {
	cases := map[string]bool{
		"subagent-frontmatter": true,
		"command-frontmatter":  true,
		"subagent":             false,
		"command":              false,
		"hook":                 false,
		"lsp":                  false,
		"mcp":                  false,
		"skill":                false,
		"memory":               false,
		"":                     false,
	}
	for component, want := range cases {
		if got := isReducedSkip(component); got != want {
			t.Errorf("isReducedSkip(%q) = %v, want %v", component, got, want)
		}
	}
}

// TestSkipTailNote pins the inline tally wording end-to-end: the empty case
// yields no note; a single kind omits the zero side; the mixed case joins
// "reduced" THEN "dropped" with " · " regardless of input order and wraps the
// whole thing in parens. ColorNever makes the yellow wrap a no-op so the bytes
// are exact.
func TestSkipTailNote(t *testing.T) {
	var buf bytes.Buffer
	p := ui.New(&buf, &buf, ui.ColorNever)
	reduced := render.SkipDetail{Component: "subagent-frontmatter", Name: "a"}
	dropped := render.SkipDetail{Component: "lsp", Name: "x"}
	tests := []struct {
		name  string
		skips []render.SkipDetail
		want  string
	}{
		{"empty", nil, ""},
		{"reduced only", []render.SkipDetail{reduced, reduced}, "(2 reduced)"},
		{"dropped only", []render.SkipDetail{dropped}, "(1 dropped)"},
		{"mixed, input order does not matter", []render.SkipDetail{dropped, reduced}, "(1 reduced · 1 dropped)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipTailNote(p, tc.skips); got != tc.want {
				t.Errorf("skipTailNote(%+v) = %q, want %q", tc.skips, got, tc.want)
			}
		})
	}
}

// TestComponentInventory pins the descriptive count tail: every non-zero kind is
// listed in a stable order, with correct singular/plural (the mcp/lsp
// abbreviations stay invariant), and a row that hosts nothing for the agent reads
// "no components".
func TestComponentInventory(t *testing.T) {
	tests := []struct {
		name string
		row  render.PluginRow
		want string
	}{
		{"empty", render.PluginRow{}, "no components"},
		{"one mcp invariant", render.PluginRow{MCP: 1}, "1 mcp"},
		{"two mcp invariant", render.PluginRow{MCP: 2}, "2 mcp"},
		{"one lsp invariant", render.PluginRow{LSP: 1}, "1 lsp"},
		{"two lsp invariant", render.PluginRow{LSP: 2}, "2 lsp"},
		{"command singular", render.PluginRow{Commands: 1}, "1 command"},
		{"commands plural", render.PluginRow{Commands: 3}, "3 commands"},
		{"subagent singular", render.PluginRow{Subagents: 1}, "1 subagent"},
		{"subagents plural", render.PluginRow{Subagents: 2}, "2 subagents"},
		{"skill/hook singular", render.PluginRow{Skills: 1, Hooks: 1}, "1 skill · 1 hook"},
		{"skills/hooks plural", render.PluginRow{Skills: 2, Hooks: 2}, "2 skills · 2 hooks"},
		{
			"order and join",
			render.PluginRow{MCP: 1, Commands: 2, Skills: 3, Subagents: 1, Hooks: 1, LSP: 1},
			"1 mcp · 2 commands · 3 skills · 1 subagent · 1 hook · 1 lsp",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := componentInventory(tc.row); got != tc.want {
				t.Errorf("componentInventory(%+v) = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}

// TestEmitSkipDetails_EmptyIsNoOp asserts a row with no skips emits nothing —
// no stray bullet, no blank line.
func TestEmitSkipDetails_EmptyIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	emitSkipDetails(&buf, ui.New(&buf, &buf, ui.ColorNever), "codex", nil)
	if buf.Len() != 0 {
		t.Errorf("emitSkipDetails(nil) wrote %q, want nothing", buf.String())
	}
}

// TestEmitSkipDetails_UnnamedComponentOnly exercises the empty-Name skip
// through the full emitSkipDetails formatting path (not just skipLabel): a skip
// with no name must render as a "<bullet> <kind>  <status>  <reason>" line with
// no dangling separator, beneath the framing header. A bare hook event with no
// native target is a "dropped", not a "reduced", skip. ColorNever pins the bytes.
func TestEmitSkipDetails_UnnamedComponentOnly(t *testing.T) {
	var buf bytes.Buffer
	emitSkipDetails(&buf, ui.New(&buf, &buf, ui.ColorNever), "codex", []render.SkipDetail{
		{Component: "hook", Reason: "unknown event"},
	})
	got := buf.String()
	want := "      " + ui.GlyphArrow + " codex couldn't fully translate — reduced = rendered without some fields; dropped = not emitted:\n" +
		"        " + ui.GlyphInfo + " hook  dropped  unknown event\n"
	if got != want {
		t.Errorf("emitSkipDetails(unnamed) = %q, want %q", got, want)
	}
}

// TestEmitSkipDetails_ColumnsAlignUnderColor is the regression guard for the
// padding-before-color contract: the reason column must start at the same
// visible offset for every skip line even though the lines carry ANSI (a yellow
// bullet, a faint reason) and the labels differ wildly in width. If padding were
// ever computed on the *styled* string, the escape bytes would be counted as
// width and the columns would skew — stripping ANSI and comparing the reason
// offsets catches that.
func TestEmitSkipDetails_ColumnsAlignUnderColor(t *testing.T) {
	skips := []render.SkipDetail{
		{Component: "lsp", Name: "x", Reason: "no LSP concept"},
		{Component: "subagent-frontmatter", Name: "reviewer", Reason: "dropped tools allowlist"},
	}
	var buf bytes.Buffer
	emitSkipDetails(&buf, ui.New(&buf, &buf, ui.ColorAlways), "codex", skips)

	raw := buf.String()
	if !strings.Contains(raw, "\x1b[") {
		t.Fatalf("ColorAlways produced no ANSI; the colored path is untested:\n%q", raw)
	}

	// Drop the framing header line; only the itemized skip lines are alignment-
	// sensitive. The "reduced"/"dropped" status words are equal width, so the
	// reason column must still land at the same offset on every item line.
	allLines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(allLines) != 3 {
		t.Fatalf("expected a header + 2 skip lines, got %d:\n%q", len(allLines), raw)
	}
	lines := allLines[1:]
	offsets := make([]int, len(lines))
	for i, line := range lines {
		plain := stripANSI(line)
		reason := skips[i].Reason
		idx := strings.Index(plain, reason)
		if idx < 0 {
			t.Fatalf("line %d missing reason %q after stripping ANSI: %q", i, reason, plain)
		}
		// The shorter label must be padded so its reason lands in the same
		// column as the longer one's.
		if !strings.Contains(plain, skipLabel(skips[i])) {
			t.Errorf("line %d missing label %q: %q", i, skipLabel(skips[i]), plain)
		}
		offsets[i] = idx
	}
	if offsets[0] != offsets[1] {
		t.Errorf("reason columns not aligned: offsets %v\n%q", offsets, raw)
	}
}
