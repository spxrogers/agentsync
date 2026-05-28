package source_test

import (
	"os"
	"path/filepath"
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

// TestWriteMemory_RefusesWhenFragmentsExist guards the silent flatten-and-orphan
// hazard: when canonical memory is composed of fragments, the value handed to
// WriteMemory is the fully expanded memory (ingest can't de-resolve it), so
// overwriting AGENTS.md would inline every @import and strand the fragment
// files. WriteMemory must refuse and leave the source untouched.
func TestWriteMemory_RefusesWhenFragmentsExist(t *testing.T) {
	home := t.TempDir()
	memDir := filepath.Join(home, "memory")
	fragDir := filepath.Join(memDir, "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "# Memory\n@import ./fragments/style.md\n"
	if err := os.WriteFile(filepath.Join(memDir, "AGENTS.md"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "style.md"), []byte("Be concise.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !source.MemoryHasFragments(home) {
		t.Fatal("MemoryHasFragments should be true")
	}

	err := source.WriteMemory(home, source.Memory{Body: "# Memory\nBe concise.\n"})
	if err == nil {
		t.Fatal("WriteMemory must refuse to overwrite fragment-composed memory")
	}
	// Source must be untouched: AGENTS.md still has the @import, fragment intact.
	got, _ := os.ReadFile(filepath.Join(memDir, "AGENTS.md"))
	if string(got) != orig {
		t.Fatalf("AGENTS.md was modified despite refusal: %q", got)
	}
	if _, err := os.Stat(filepath.Join(fragDir, "style.md")); err != nil {
		t.Fatalf("fragment was orphaned/removed: %v", err)
	}
}

// TestWriteMemory_WritesWhenNoFragments confirms the guard does not block the
// normal (no-fragments) write.
func TestWriteMemory_WritesWhenNoFragments(t *testing.T) {
	home := t.TempDir()
	if source.MemoryHasFragments(home) {
		t.Fatal("MemoryHasFragments should be false on an empty home")
	}
	if err := source.WriteMemory(home, source.Memory{Body: "# Memory\n"}); err != nil {
		t.Fatalf("WriteMemory should succeed without fragments: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(home, "memory", "AGENTS.md"))
	if string(got) != "# Memory\n" {
		t.Fatalf("memory not written: %q", got)
	}
}

// TestMemoryMarkers_RoundTrip proves expansion is reversible: a fragmented
// memory expands with boundary markers, and CollapseMemoryMarkers reconstructs
// AGENTS.md (with @import restored) and the fragment content byte-for-byte.
func TestMemoryMarkers_RoundTrip(t *testing.T) {
	body := "# Memory\n\n@import ./fragments/style.md\n"
	frags := map[string]string{"style.md": "Be concise.\n"}

	expanded := source.ExpandMemoryImports(body, frags)
	if !strings.Contains(expanded, "<!-- agentsync:fragment style.md -->") ||
		!strings.Contains(expanded, "<!-- /agentsync:fragment style.md -->") {
		t.Fatalf("markers not emitted: %q", expanded)
	}
	if strings.Contains(expanded, "@import") {
		t.Fatalf("literal @import leaked into rendered output: %q", expanded)
	}

	mem, had, err := source.CollapseMemoryMarkers(expanded)
	if err != nil || !had {
		t.Fatalf("collapse: had=%v err=%v", had, err)
	}
	if mem.Body != body {
		t.Fatalf("body round-trip: got %q want %q", mem.Body, body)
	}
	if mem.Fragments["style.md"] != "Be concise.\n" {
		t.Fatalf("fragment round-trip: got %q", mem.Fragments["style.md"])
	}
}

// TestMemoryMarkers_Nested covers a fragment that itself @imports another: the
// inner block is restored as an @import inside the outer fragment, not inlined.
func TestMemoryMarkers_Nested(t *testing.T) {
	body := "# M\n@import ./fragments/outer.md\n"
	frags := map[string]string{
		"outer.md": "Outer top\n@import ./fragments/inner.md\nOuter bottom\n",
		"inner.md": "Inner\n",
	}
	mem, had, err := source.CollapseMemoryMarkers(source.ExpandMemoryImports(body, frags))
	if err != nil || !had {
		t.Fatalf("collapse: had=%v err=%v", had, err)
	}
	if mem.Body != body {
		t.Fatalf("body: got %q", mem.Body)
	}
	if mem.Fragments["outer.md"] != frags["outer.md"] {
		t.Fatalf("outer fragment: got %q want %q", mem.Fragments["outer.md"], frags["outer.md"])
	}
	if mem.Fragments["inner.md"] != frags["inner.md"] {
		t.Fatalf("inner fragment: got %q", mem.Fragments["inner.md"])
	}
}

// TestCollapseMemoryMarkers_Errors covers the refuse-not-guess cases.
func TestCollapseMemoryMarkers_Errors(t *testing.T) {
	cases := map[string]string{
		"unbalanced": "# M\n<!-- agentsync:fragment a.md -->\nx\n",
		"mismatched": "<!-- agentsync:fragment a.md -->\nx\n<!-- /agentsync:fragment b.md -->\n",
		"traversal":  "<!-- agentsync:fragment ../evil -->\nx\n<!-- /agentsync:fragment ../evil -->\n",
		"ambiguous":  "<!-- agentsync:fragment a.md -->\nx\n<!-- /agentsync:fragment a.md -->\n<!-- agentsync:fragment a.md -->\ny\n<!-- /agentsync:fragment a.md -->\n",
	}
	for name, dest := range cases {
		_, had, err := source.CollapseMemoryMarkers(dest)
		if !had || err == nil {
			t.Fatalf("%s: expected (had=true, err!=nil), got had=%v err=%v", name, had, err)
		}
	}
}

// TestCollapseMemoryMarkers_NoMarkers returns had=false so callers take the
// plain (or guard) path.
func TestCollapseMemoryMarkers_NoMarkers(t *testing.T) {
	_, had, err := source.CollapseMemoryMarkers("# Memory\nplain body\n")
	if had || err != nil {
		t.Fatalf("expected had=false err=nil, got had=%v err=%v", had, err)
	}
}

// TestExpandMemoryImports_MarkerCollision: a fragment whose content already
// contains the marker token disables markers entirely (plain expansion), so a
// reverse parse can't be corrupted.
func TestExpandMemoryImports_MarkerCollision(t *testing.T) {
	body := "@import ./fragments/a.md\n"
	frags := map[string]string{"a.md": "see <!-- agentsync:fragment x -->\n"}
	expanded := source.ExpandMemoryImports(body, frags)
	if strings.Contains(expanded, "<!-- agentsync:fragment a.md -->") {
		t.Fatalf("markers must be suppressed on collision: %q", expanded)
	}
	_, had, _ := source.CollapseMemoryMarkers(expanded)
	_ = had // content token may still trip detection; the write-back guard covers safety
}
