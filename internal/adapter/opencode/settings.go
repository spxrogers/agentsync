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
	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// MergeJSONC merges ours into existing JSONC content, removing ownedPointers
// no longer present in ours. Foreign keys and values are preserved.
//
// Thin wrapper over the shared jsonkeys.MergeJSONC engine (hujson parse →
// Standardize → MergeKeys → plain-JSON emit; comments are NOT preserved — see
// its doc and the README "Known limits").
func MergeJSONC(existing []byte, ours map[string]any, ownedPointers []string) ([]byte, error) {
	return jsonkeys.MergeJSONC(existing, ours, ownedPointers)
}
