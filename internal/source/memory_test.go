package source_test

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestExpandMemoryImports_Recursive is the regression for the single-pass
// expander: a fragment that itself @imports another left the nested directive
// as a literal line in the rendered memory file.
func TestExpandMemoryImports_Recursive(t *testing.T) {
	body := "Top\n@import ./fragments/a.md\n"
	frags := map[string]string{
		"a.md": "Content A\n@import ./fragments/b.md",
		"b.md": "Content B",
	}
	got := source.ExpandMemoryImports(body, frags)
	if !strings.Contains(got, "Content A") || !strings.Contains(got, "Content B") {
		t.Fatalf("nested fragment not expanded: %q", got)
	}
	if strings.Contains(got, "@import") {
		t.Fatalf("literal @import directive leaked into output: %q", got)
	}
}

// TestExpandMemoryImports_Cycle ensures a fragment cycle terminates and does
// not stack-overflow; the cyclic directive is left literal.
func TestExpandMemoryImports_Cycle(t *testing.T) {
	body := "@import ./fragments/a.md\n"
	frags := map[string]string{
		"a.md": "A\n@import ./fragments/b.md",
		"b.md": "B\n@import ./fragments/a.md",
	}
	got := source.ExpandMemoryImports(body, frags) // must not hang/overflow
	if !strings.Contains(got, "A") || !strings.Contains(got, "B") {
		t.Fatalf("cycle expansion dropped content: %q", got)
	}
}

// TestExpandMemoryImports_UnknownFragmentLeftLiteral keeps a directive for a
// missing fragment so the user notices.
func TestExpandMemoryImports_UnknownFragmentLeftLiteral(t *testing.T) {
	got := source.ExpandMemoryImports("@import ./fragments/missing.md\n", map[string]string{})
	if !strings.Contains(got, "@import ./fragments/missing.md") {
		t.Fatalf("unknown fragment directive should be preserved: %q", got)
	}
}
