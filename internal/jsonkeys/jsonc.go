package jsonkeys

import (
	"encoding/json"
	"fmt"

	"github.com/tailscale/hujson"
)

// MergeJSONC merges ours into existing JSONC content, removing ownedPointers
// no longer present in ours. Foreign keys and values are preserved. It is the
// shared engine behind the "merge-jsonc-keys" strategy (OpenCode's
// opencode.json, Gemini's settings.json, and the generic breadth tier's
// commented settings files).
//
// Strategy: parse existing JSONC (hujson, tolerating comments + trailing
// commas) -> Standardize to strict JSON -> MergeKeys -> emit via
// json.MarshalIndent. v1 trade-off: this Standardize+marshal path discards ALL
// comments and trailing-comma formatting (not just touched sections). The
// file is never corrupted and no data keys are lost; only JSONC formatting is.
// Comment-preserving mutation is deferred to v1.x.
//
// Unparseable existing content is a returned error, never treated as empty —
// merging against an empty map would clobber every foreign key in the file.
func MergeJSONC(existing []byte, ours map[string]any, ownedPointers []string) ([]byte, error) {
	if len(existing) == 0 {
		existing = []byte("{}")
	}
	val, err := hujson.Parse(existing)
	if err != nil {
		return nil, fmt.Errorf("parse jsonc: %w", err)
	}
	val.Standardize()
	// Decode preserving json.Number so a foreign integer > 2^53 in the user's
	// file isn't rounded when the merged file is re-marshalled.
	existingMap, err := DecodeObject(val.Pack())
	if err != nil {
		return nil, fmt.Errorf("standardize jsonc: %w", err)
	}
	merged, _, _ := MergeKeys(existingMap, ours, ownedPointers)
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal merged: %w", err)
	}
	return append(out, '\n'), nil
}

// DecodeJSONC parses JSONC content (comments + trailing commas tolerated) into
// a map, preserving json.Number. Plain JSON parses identically. Used by ingest
// paths whose native file is JSONC.
func DecodeJSONC(data []byte) (map[string]any, error) {
	val, err := hujson.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse jsonc: %w", err)
	}
	val.Standardize()
	return DecodeObject(val.Pack())
}
