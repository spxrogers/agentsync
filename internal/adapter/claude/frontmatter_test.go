package claude_test

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter/claude"
)

// TestParseFrontmatter_LargeIntPrecision is the regression for YAML frontmatter
// integers > 2^53 losing precision: sigs.k8s.io/yaml decodes every number as
// float64, so 9007199254740993 (2^53+1) became 9007199254740992 on render and
// on source write-back. The decode must preserve the integer exactly.
func TestParseFrontmatter_LargeIntPrecision(t *testing.T) {
	const big = "9007199254740993" // 2^53 + 1: first int float64 can't represent
	in := []byte("---\nmax_tokens: " + big + "\nname: x\n---\nbody\n")
	fm, body, err := claude.ParseFrontmatter(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := claude.EncodeFrontmatter(fm, body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), big) {
		t.Fatalf("large int corrupted on round-trip; want %s in:\n%s", big, out)
	}
}

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

// TestParseFrontmatterWithReport_BadYAMLDescription is the regression for skill
// SKILL.md files whose unquoted description contains a "Triggers on: X, Y" colon-
// space sequence — that bare ": " makes sigs.k8s.io/yaml bail with "mapping
// values are not allowed in this context", and Ingest's silent `continue`
// dropped the whole skill. The lenient fallback succeeds on these files, returns
// the full description as a single string, and reports Lenient=true so callers
// can warn.
func TestParseFrontmatterWithReport_BadYAMLDescription(t *testing.T) {
	// Verbatim frontmatter from ~/.claude/skills/gltf-transform/SKILL.md.
	input := []byte(`---
name: gltf-transform
description: Optimize and post-process GLB/glTF 3D models. Use when compressing models for web delivery, reducing file size, simplifying geometry, inspecting model stats, merging models, or converting textures. Triggers on: optimize GLB, compress model, reduce file size, simplify mesh, draco compression, meshopt, webp textures, inspect model, merge GLB, model optimization.
---
body text
`)
	// Strict parser MUST still fail (backward-compat: callers that don't opt in
	// to lenient parsing get the same behavior they had before).
	if _, _, err := claude.ParseFrontmatter(input); err == nil {
		t.Fatalf("ParseFrontmatter accepted bad-YAML description; lenient fallback must be opt-in")
	}
	fm, body, lenient, err := claude.ParseFrontmatterWithReport(input)
	if err != nil {
		t.Fatalf("ParseFrontmatterWithReport: %v", err)
	}
	if !lenient {
		t.Fatalf("Lenient must be true when strict YAML fails and lenient succeeds")
	}
	if fm["name"] != "gltf-transform" {
		t.Fatalf("name not parsed leniently: %+v", fm)
	}
	desc, ok := fm["description"].(string)
	if !ok {
		t.Fatalf("description not a string: %T %v", fm["description"], fm["description"])
	}
	// The lenient parser must keep the FULL description (including the colon-
	// space that broke strict YAML). Truncating at the colon would silently
	// corrupt the field — the very failure mode we're trying to stop.
	if !strings.Contains(desc, "Triggers on: optimize GLB") {
		t.Fatalf("lenient description truncated at colon-space: %q", desc)
	}
	if !strings.Contains(desc, "model optimization.") {
		t.Fatalf("lenient description missing tail: %q", desc)
	}
	if body != "body text\n" {
		t.Fatalf("body mismatch: %q", body)
	}
}

// TestParseFrontmatterWithReport_StrictSuccess: a clean YAML frontmatter must
// parse via the strict path (Lenient=false), so we don't silently mask bugs
// in callers that only want strict input.
func TestParseFrontmatterWithReport_StrictSuccess(t *testing.T) {
	input := []byte(`---
name: x
description: simple value
---
body
`)
	fm, body, lenient, err := claude.ParseFrontmatterWithReport(input)
	if err != nil {
		t.Fatal(err)
	}
	if lenient {
		t.Fatalf("Lenient must be false when strict YAML succeeds")
	}
	if fm["name"] != "x" || fm["description"] != "simple value" {
		t.Fatalf("strict parse data lost: %+v", fm)
	}
	if body != "body\n" {
		t.Fatalf("body = %q", body)
	}
}

// TestParseFrontmatterWithReport_UnterminatedFrontmatter: an unterminated "---"
// block is a structural error the lenient parser cannot recover from — it must
// still return an error.
func TestParseFrontmatterWithReport_UnterminatedFrontmatter(t *testing.T) {
	input := []byte("---\nname: x\nbody without closing fence\n")
	if _, _, _, err := claude.ParseFrontmatterWithReport(input); err == nil {
		t.Fatalf("unterminated frontmatter must error")
	}
}

// TestParseFrontmatterWithReport_NoFrontmatter: a file with no leading "---"
// is returned as body, identical to the strict ParseFrontmatter behavior.
func TestParseFrontmatterWithReport_NoFrontmatter(t *testing.T) {
	fm, body, lenient, err := claude.ParseFrontmatterWithReport([]byte("plain\n"))
	if err != nil {
		t.Fatal(err)
	}
	if lenient {
		t.Fatalf("no-frontmatter is not a lenient parse")
	}
	if len(fm) != 0 || body != "plain\n" {
		t.Fatalf("unexpected: fm=%v body=%q", fm, body)
	}
}
