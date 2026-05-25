package claude

import (
	"bytes"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// ParseFrontmatter extracts the YAML frontmatter and the markdown body. If
// the input doesn't begin with "---\n", returns an empty map and the entire
// input as body.
func ParseFrontmatter(data []byte) (map[string]any, string, error) {
	// Normalize CRLF → LF so a .md saved by a Windows editor ("---\r\n") is
	// recognized as having frontmatter. Without this the literal "---\n" check
	// fails, the whole file is treated as body, and description/model/mode
	// silently vanish on ingest/import. agentsync re-renders bodies with LF,
	// so the normalization is lossless in practice.
	if bytes.IndexByte(data, '\r') >= 0 {
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	}
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return map[string]any{}, string(data), nil
	}
	rest := data[len("---\n"):]
	yml, body, ok := splitFrontmatterBody(rest)
	if !ok {
		return nil, "", fmt.Errorf("unterminated frontmatter")
	}
	// An empty frontmatter block ("---\n---\n…") is a valid empty mapping.
	if len(bytes.TrimSpace(yml)) == 0 {
		return map[string]any{}, string(body), nil
	}
	fm, err := jsonkeys.DecodeYAML(yml)
	if err != nil {
		return nil, "", fmt.Errorf("parse yaml frontmatter: %w", err)
	}
	return fm, string(body), nil
}

// splitFrontmatterBody splits the bytes AFTER the opening "---\n" into YAML
// frontmatter and body, accepting the closing "---" fence mid-file ("\n---\n"),
// at end-of-file ("\n---", no trailing newline), or — for an empty mapping — as
// the first line ("---\n…" or just "---"). ok is false only with no closing
// fence. Mirrors source.splitFrontmatterBody; without it a component .md whose
// fence sits at EOF was silently dropped on ingest.
func splitFrontmatterBody(rest []byte) (yml, body []byte, ok bool) {
	switch {
	case bytes.HasPrefix(rest, []byte("---\n")):
		return nil, rest[len("---\n"):], true
	case bytes.Equal(rest, []byte("---")):
		return nil, nil, true
	}
	if i := bytes.Index(rest, []byte("\n---\n")); i >= 0 {
		return rest[:i], rest[i+len("\n---\n"):], true
	}
	if bytes.HasSuffix(rest, []byte("\n---")) {
		return rest[:len(rest)-len("\n---")], nil, true
	}
	return nil, nil, false
}

// EncodeFrontmatter writes "---\n<yaml>\n---\n<body>" with the keys in fm.
// fm is empty: returns just the body.
func EncodeFrontmatter(fm map[string]any, body string) ([]byte, error) {
	if len(fm) == 0 {
		return []byte(body), nil
	}
	yml, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("encode yaml: %w", err)
	}
	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(yml)
	buf.WriteString("---\n")
	buf.WriteString(body)
	return []byte(buf.String()), nil
}
