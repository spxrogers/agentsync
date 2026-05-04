// Package opencode implements the OpenCode adapter for agentsync.
//
// settings.go: JSONC-aware merge for opencode.json. Uses tailscale/hujson
// which preserves comments and trailing commas across parse->mutate->serialize
// cycles.
package opencode

import (
	"encoding/json"
	"fmt"

	"github.com/tailscale/hujson"

	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// MergeJSONC merges ours into existing JSONC content, removing ownedPointers
// no longer present in ours. Comments and trailing-comma formatting from
// existing are preserved as much as the hujson AST allows.
//
// Strategy: parse existing JSONC -> standardize to JSON (Pack), MergeKeys,
// then format result as plain JSON. v1 trade-off: trailing comma + comment
// preservation is partial — comments outside touched keys survive; comments
// adjacent to deleted keys are also removed. M2 ships this; comment-position
// fidelity can be tightened later if pain emerges.
func MergeJSONC(existing []byte, ours map[string]any, ownedPointers []string) ([]byte, error) {
	if len(existing) == 0 {
		existing = []byte("{}")
	}
	val, err := hujson.Parse(existing)
	if err != nil {
		return nil, fmt.Errorf("parse jsonc: %w", err)
	}
	val.Standardize()
	var existingMap map[string]any
	if err := json.Unmarshal(val.Pack(), &existingMap); err != nil {
		return nil, fmt.Errorf("standardize jsonc: %w", err)
	}
	if existingMap == nil {
		existingMap = map[string]any{}
	}
	merged, _, _ := jsonkeys.MergeKeys(existingMap, ours, ownedPointers)
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal merged: %w", err)
	}
	return append(out, '\n'), nil
}
