// Package opencode implements the OpenCode adapter for agentsync.
//
// settings.go: JSONC-aware merge for opencode.json. Uses tailscale/hujson to
// PARSE JSONC (comments + trailing commas) so a hand-edited opencode.json is
// not rejected. NOTE: the merged result is re-emitted as plain JSON — comments
// and trailing commas are NOT preserved (see MergeJSONC). Foreign keys/values
// are preserved; only the formatting/comments are lost. Comment-preserving
// mutation is deferred to v1.x (matches the README "Known limits").
package opencode

import (
	"encoding/json"
	"fmt"

	"github.com/tailscale/hujson"

	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// MergeJSONC merges ours into existing JSONC content, removing ownedPointers
// no longer present in ours. Foreign keys and values are preserved.
//
// Strategy: parse existing JSONC (hujson, tolerating comments + trailing
// commas) -> Standardize to strict JSON -> MergeKeys -> emit via
// json.MarshalIndent. v1 trade-off: this Standardize+marshal path discards ALL
// comments and trailing-comma formatting (not just touched sections). The
// file is never corrupted and no data keys are lost; only JSONC formatting is.
// Comment-preserving mutation is deferred to v1.x.
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
	// opencode.json isn't rounded when the merged file is re-marshalled.
	existingMap, err := jsonkeys.DecodeObject(val.Pack())
	if err != nil {
		return nil, fmt.Errorf("standardize jsonc: %w", err)
	}
	merged, _, _ := jsonkeys.MergeKeys(existingMap, ours, ownedPointers)
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal merged: %w", err)
	}
	return append(out, '\n'), nil
}
