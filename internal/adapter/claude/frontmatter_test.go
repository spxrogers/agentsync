package claude_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter/claude"
)

func TestParseFrontmatter_Standard(t *testing.T) {
	input := []byte(`---
name: my-skill
description: Does the thing
disable-model-invocation: true
---
This is the body.

Multiple lines.
`)
	fm, body, err := claude.ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	if fm["name"] != "my-skill" {
		t.Fatalf("name = %v", fm["name"])
	}
	if fm["disable-model-invocation"] != true {
		t.Fatalf("disable-model-invocation = %v", fm["disable-model-invocation"])
	}
	if body != "This is the body.\n\nMultiple lines.\n" {
		t.Fatalf("body mismatch: %q", body)
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	fm, body, err := claude.ParseFrontmatter([]byte("plain markdown\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fm) != 0 {
		t.Fatalf("fm should be empty: %+v", fm)
	}
	if body != "plain markdown\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestEncodeFrontmatter_Roundtrip(t *testing.T) {
	fm := map[string]any{"name": "x", "description": "y"}
	out, err := claude.EncodeFrontmatter(fm, "body")
	if err != nil {
		t.Fatal(err)
	}
	fm2, body2, err := claude.ParseFrontmatter(out)
	if err != nil {
		t.Fatal(err)
	}
	if fm2["name"] != "x" || fm2["description"] != "y" {
		t.Fatalf("roundtrip lost data: %+v", fm2)
	}
	if body2 != "body" {
		t.Fatalf("body = %q", body2)
	}
}

// TestParseFrontmatter_CRLF is the regression for skill/subagent/command .md
// files saved by a Windows editor with CRLF line endings. The parser matched
// only the literal "---\n" delimiter, so a "---\r\n" header was treated as no
// frontmatter — the entire file became body and description/model/mode
// silently vanished on ingest/import.
func TestParseFrontmatter_CRLF(t *testing.T) {
	input := []byte("---\r\nname: my-skill\r\ndescription: Does the thing\r\n---\r\nBody line one\r\n")
	fm, body, err := claude.ParseFrontmatter(input)
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if fm["description"] != "Does the thing" {
		t.Fatalf("description lost on CRLF input: %+v", fm)
	}
	if fm["name"] != "my-skill" {
		t.Fatalf("name lost on CRLF input: %+v", fm)
	}
	if body == string(input) {
		t.Fatalf("frontmatter not stripped; whole file returned as body: %q", body)
	}
}
