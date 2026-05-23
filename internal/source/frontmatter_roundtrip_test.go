package source_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestWriteSubagent_PreservesListAndMapFrontmatter is the regression for the
// import write-back corruption: renderFrontmatter emitted Go fmt syntax (%v)
// for non-string values, so a subagent's `tools: [Read, Write]` list and any
// nested map were serialized as "[Read Write]" / "map[a:1]" and re-parsed as a
// single mangled string — corrupting the tool allowlist on `import
// claude:agent:<name>`.
func TestWriteSubagent_PreservesListAndMapFrontmatter(t *testing.T) {
	home := t.TempDir()
	sa := source.Subagent{
		Name: "reviewer",
		Frontmatter: map[string]any{
			"description": "rev",
			"tools":       []any{"Read", "Write"},
			"nested":      map[string]any{"a": "one", "b": "two"},
		},
		Body: "body\n",
	}
	if err := source.WriteSubagent(home, sa); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "agents", "reviewer.md"))
	if err != nil {
		t.Fatal(err)
	}
	fm, _, err := source.ParseFrontmatter(data)
	if err != nil {
		t.Fatalf("re-parse frontmatter: %v\nraw:\n%s", err, data)
	}
	tools, ok := fm["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("tools list corrupted on write/read round-trip: %#v\nraw:\n%s", fm["tools"], data)
	}
	if _, ok := fm["nested"].(map[string]any); !ok {
		t.Fatalf("nested map corrupted on write/read round-trip: %#v\nraw:\n%s", fm["nested"], data)
	}
}
