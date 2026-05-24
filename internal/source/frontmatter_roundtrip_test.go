package source_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestWriteSubagent_PreservesLargeIntFrontmatter is the source-side regression
// for YAML frontmatter integers > 2^53 losing precision (the YAML twin of the
// jsonkeys large-int fix). renderFrontmatter + ParseFrontmatter must preserve
// the exact integer rather than round it through float64.
func TestWriteSubagent_PreservesLargeIntFrontmatter(t *testing.T) {
	home := t.TempDir()
	const big = int64(9007199254740993) // 2^53 + 1: first int float64 can't represent
	sa := source.Subagent{
		Name:        "big",
		Frontmatter: map[string]any{"description": "d", "max_tokens": big},
		Body:        "body\n",
	}
	if err := source.WriteSubagent(home, sa); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "agents", "big.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "9007199254740993") {
		t.Fatalf("large int corrupted on write:\n%s", data)
	}
	fm, _, err := source.ParseFrontmatter(data)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := fm["max_tokens"].(int64); !ok || got != big {
		t.Fatalf("large int corrupted on read: %#v (want int64 %d)", fm["max_tokens"], big)
	}
}

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
