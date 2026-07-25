// Package jsonkeys implements per-key JSON pointer merge used by adapters that
// need to own a subset of keys inside a shared JSON (or JSONC) config file.
package jsonkeys

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// DecodeObject unmarshals JSON object bytes into a map, preserving numbers as
// json.Number rather than coercing to float64. The merge promises to preserve
// FOREIGN keys verbatim, but float64 silently rounds any integer larger than
// 2^53 (snowflake ids, nanosecond timestamps) on re-marshal. json.Number keeps
// the literal digits and re-marshals exactly. nil/empty input yields an empty
// map.
func DecodeObject(data []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(data) == 0 {
		return m, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// DecodeYAML parses YAML (e.g. component frontmatter) into a map, preserving
// integer precision beyond 2^53 (up to int64). A plain yaml.Unmarshal into
// interface{} decodes every number as float64, silently rounding a large
// integer (snowflake id, nanosecond timestamp) — and since yaml.Marshal then
// re-emits the rounded value, the corruption is persisted on render and on
// source write-back. This routes YAML→JSON (which keeps the integer literal),
// decodes with UseNumber, then normalizes through ConvertNumbers so
// yaml.Marshal re-emits a bare integer; see ConvertNumbers' range contract for
// the beyond-int64 and unparseable extremes. Empty/null input yields an empty
// map; a non-mapping document is an error (frontmatter must be a mapping).
func DecodeYAML(yml []byte) (map[string]any, error) {
	jb, err := yaml.YAMLToJSON(yml)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(jb))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return map[string]any{}, nil
	}
	m, ok := ConvertNumbers(raw).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("yaml document is not a mapping")
	}
	return m, nil
}

// ConvertNumbers walks v, replacing every json.Number with a Go numeric,
// recursing through maps and slices and leaving non-numeric values untouched.
// DecodeObject/DecodeJSONC deliberately KEEP json.Number so a merge re-marshals
// foreign JSON keys with their exact digits — but a json.Number that escapes
// into a TOML-persisted field is a hazard: go-toml marshals the underlying
// string type as a TOML *string* (`timeout = '30'`), silently flipping the
// value's type on the next render. Callers that persist decoded values outside
// JSON (the capture dest->source funnel, DecodeYAML) normalize through this
// first.
//
// Range contract: an integer within int64 stays exact (snowflake ids and
// nanosecond timestamps beyond float64's 2^53 survive); an integer BEYOND
// int64 falls to float64 and is lossy — TOML integers are int64, so exactness
// past that bound is unrepresentable in the persisted form anyway; a numeric
// literal that parses as neither int64 nor float64 (e.g. `1e309`, `1e400`)
// falls back to its literal string, persisted as a TOML string — acceptable
// for an unrepresentable extreme, and the capture leak backstop still scans
// strings.
func ConvertNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			x[k] = ConvertNumbers(vv)
		}
		return x
	case []any:
		for i, vv := range x {
			x[i] = ConvertNumbers(vv)
		}
		return x
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	default:
		return v
	}
}

// MergeKeys merges ours into existing, removing ownedPointers that are no
// longer in ours. Returns the merged map plus diagnostic lists.
//
// kept: pointers from ownedPointers that are still present in ours.
// removed: pointers from ownedPointers that are absent from ours and were
// deleted from existing.
//
// JSON pointer syntax: leading "/", "/" separated path segments. RFC 6901
// escapes ("~0" for "~", "~1" for "/") are supported.
func MergeKeys(existing, ours map[string]any, ownedPointers []string) (map[string]any, []string, []string) {
	merged := deepCopyMap(existing)
	if merged == nil {
		merged = map[string]any{}
	}

	// Step 1: overlay ours onto merged, REPLACING at second-level (owned)
	// granularity rather than deep-merging.
	overlayOwned(merged, ours)

	// Step 2: walk ownedPointers; if a pointer is no longer present in `ours`,
	// delete it from merged. If still present, mark kept.
	var kept, removed []string
	for _, p := range ownedPointers {
		if pointerExists(ours, p) {
			kept = append(kept, p)
			continue
		}
		if pointerExists(merged, p) {
			deletePointer(merged, p)
			removed = append(removed, p)
		}
	}
	return merged, kept, removed
}

// overlayOwned overlays ours onto merged at SECOND-LEVEL (owned) granularity:
// agentsync owns a top-level section's child objects wholesale (e.g.
// /mcpServers/<id>), so when both sides hold an object at a top-level key, each
// child key from ours REPLACES the corresponding child in merged — it is not
// deep-merged. This is what makes a removed inner field (a dropped env var, or
// a stale `command` after a stdio→http switch) actually disappear, while still
// preserving FOREIGN sibling children (ids the user added that ours doesn't
// mention). A top-level key whose value is a scalar/array is replaced whole.
// Values are deep-copied so merged shares no mutable structure with ours.
func overlayOwned(merged, ours map[string]any) {
	for k, v := range ours {
		ovv, ok := v.(map[string]any)
		mvv, mok := merged[k].(map[string]any)
		if ok && mok {
			for kk, vv := range ovv {
				mvv[kk] = deepCopyValue(vv)
			}
			continue
		}
		merged[k] = deepCopyValue(v)
	}
}

func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

// deepCopyValue recursively copies maps and slices so the result shares no
// mutable structure with the input. Scalars (string/float64/bool/nil) are
// immutable and returned as-is. Without the []any case a merged result aliased
// the input's arrays (and the objects inside them).
func deepCopyValue(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		return deepCopyMap(vv)
	case []any:
		out := make([]any, len(vv))
		for i, e := range vv {
			out[i] = deepCopyValue(e)
		}
		return out
	default:
		return v
	}
}

func pointerExists(m map[string]any, ptr string) bool {
	parts := splitPointer(ptr)
	var cur any = m
	for _, p := range parts {
		mp, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = mp[p]
		if !ok {
			return false
		}
	}
	return true
}

func deletePointer(m map[string]any, ptr string) {
	parts := splitPointer(ptr)
	if len(parts) == 0 {
		return
	}
	cur := m
	for i, p := range parts {
		if i == len(parts)-1 {
			delete(cur, p)
			return
		}
		next, ok := cur[p].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
}

func splitPointer(ptr string) []string {
	ptr = strings.TrimPrefix(ptr, "/")
	if ptr == "" {
		return nil
	}
	raw := strings.Split(ptr, "/")
	out := make([]string, len(raw))
	for i, s := range raw {
		s = strings.ReplaceAll(s, "~1", "/")
		s = strings.ReplaceAll(s, "~0", "~")
		out[i] = s
	}
	return out
}
