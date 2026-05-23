package source_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestParseFrontmatter_CRLF is the regression for the source-package twin of
// the frontmatter parser. The adapter twin (claude.ParseFrontmatter) was fixed
// to accept CRLF, but source.ParseFrontmatter — used by source.Load and by
// marketplace plugin projection (which parses externally-fetched .md files,
// very plausibly CRLF) — still matched only "---\n", silently dropping all
// frontmatter from a Windows-authored component.
func TestParseFrontmatter_CRLF(t *testing.T) {
	input := []byte("---\r\nname: my-skill\r\ndescription: Does the thing\r\n---\r\nBody line one\r\n")
	fm, body, err := source.ParseFrontmatter(input)
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if fm["description"] != "Does the thing" || fm["name"] != "my-skill" {
		t.Fatalf("frontmatter lost on CRLF input: %+v", fm)
	}
	if body == string(input) {
		t.Fatalf("frontmatter not stripped; whole file returned as body: %q", body)
	}
}
