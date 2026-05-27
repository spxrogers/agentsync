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
