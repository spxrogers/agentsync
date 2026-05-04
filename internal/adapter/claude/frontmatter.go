package claude

import (
	"bytes"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// ParseFrontmatter extracts the YAML frontmatter and the markdown body. If
// the input doesn't begin with "---\n", returns an empty map and the entire
// input as body.
func ParseFrontmatter(data []byte) (map[string]any, string, error) {
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return map[string]any{}, string(data), nil
	}
	rest := data[len("---\n"):]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		return nil, "", fmt.Errorf("unterminated frontmatter")
	}
	yml := rest[:end]
	body := rest[end+len("\n---\n"):]

	var fm map[string]any
	if err := yaml.Unmarshal(yml, &fm); err != nil {
		return nil, "", fmt.Errorf("parse yaml frontmatter: %w", err)
	}
	if fm == nil {
		fm = map[string]any{}
	}
	return fm, string(body), nil
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
