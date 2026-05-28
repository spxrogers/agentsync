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
// input as body. Returns an error on any YAML decode failure; callers that
// want to accept Claude Code's looser "key: value-with-colons" frontmatter
// should use ParseFrontmatterWithReport instead.
func ParseFrontmatter(data []byte) (map[string]any, string, error) {
	fm, body, _, err := parseFrontmatter(data, false)
	return fm, body, err
}

// ParseFrontmatterWithReport is the lenient-fallback variant. It first tries
// strict YAML; on decode failure it falls back to a line-oriented "key: rest-
// of-line" parser that accepts bare colon-space inside values. lenient is true
// iff the strict parse failed but the lenient one succeeded — Ingest callers
// surface a warning in that case so the user knows their SKILL.md (or other
// component .md) is not strict YAML.
//
// The lenient parser exists because Claude Code itself reads frontmatter that
// way: a SKILL.md whose description contains an unquoted "Triggers on: X, Y"
// parses fine in claude.ai but breaks sigs.k8s.io/yaml ("mapping values are
// not allowed in this context"). Without this fallback, the silent `continue`
// in adapter Ingest dropped any skill with such a description.
func ParseFrontmatterWithReport(data []byte) (fm map[string]any, body string, lenient bool, err error) {
	return parseFrontmatter(data, true)
}

func parseFrontmatter(data []byte, allowLenient bool) (fm map[string]any, body string, lenient bool, err error) {
	// Normalize CRLF → LF so a .md saved by a Windows editor ("---\r\n") is
	// recognized as having frontmatter. Without this the literal "---\n" check
	// fails, the whole file is treated as body, and description/model/mode
	// silently vanish on ingest/import. agentsync re-renders bodies with LF,
	// so the normalization is lossless in practice.
	if bytes.IndexByte(data, '\r') >= 0 {
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	}
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return map[string]any{}, string(data), false, nil
	}
	rest := data[len("---\n"):]
	yml, bodyBytes, ok := splitFrontmatterBody(rest)
	if !ok {
		return nil, "", false, fmt.Errorf("unterminated frontmatter")
	}
	// An empty frontmatter block ("---\n---\n…") is a valid empty mapping.
	if len(bytes.TrimSpace(yml)) == 0 {
		return map[string]any{}, string(bodyBytes), false, nil
	}
	fm, strictErr := jsonkeys.DecodeYAML(yml)
	if strictErr == nil {
		return fm, string(bodyBytes), false, nil
	}
	if !allowLenient {
		return nil, "", false, fmt.Errorf("parse yaml frontmatter: %w", strictErr)
	}
	lfm, lerr := parseFrontmatterLenient(yml)
	if lerr != nil {
		// Both parsers failed — return the strict error since it's typically
		// more informative ("mapping values are not allowed in this context"
		// vs. our generic lenient error).
		return nil, "", false, fmt.Errorf("parse yaml frontmatter: %w", strictErr)
	}
	return lfm, string(bodyBytes), true, nil
}

// parseFrontmatterLenient parses YAML frontmatter as a flat string-string map
// using "key: rest-of-line" semantics: the first ": " on each line splits the
// key from the value, the value is taken verbatim to end-of-line, and any
// further colons (the failure mode strict YAML rejects) are preserved.
//
// This is intentionally narrow — it does NOT support nested mappings, arrays,
// multi-line scalars, or quoting. The lenient path exists only to recover skill
// frontmatter whose description contains bare colon-space, which is by far the
// dominant real-world breakage. Anything more structured belongs in strict YAML.
func parseFrontmatterLenient(yml []byte) (map[string]any, error) {
	out := map[string]any{}
	lines := bytes.Split(yml, []byte("\n"))
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' || bytes.Equal(trimmed, []byte("---")) {
			continue
		}
		idx := bytes.Index(line, []byte(": "))
		if idx < 0 {
			// A line ending with ": " (no value) is rare but valid YAML for an
			// empty value; accept it.
			if bytes.HasSuffix(bytes.TrimRight(line, " \t"), []byte(":")) {
				key := strings.TrimSpace(string(bytes.TrimSuffix(bytes.TrimRight(line, " \t"), []byte(":"))))
				if key == "" {
					return nil, fmt.Errorf("lenient parse: malformed line %q", string(line))
				}
				out[key] = ""
				continue
			}
			return nil, fmt.Errorf("lenient parse: no key/value separator on line %q", string(line))
		}
		key := strings.TrimSpace(string(line[:idx]))
		if key == "" {
			return nil, fmt.Errorf("lenient parse: empty key on line %q", string(line))
		}
		val := strings.TrimRight(string(line[idx+2:]), " \t")
		out[key] = val
	}
	return out, nil
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
