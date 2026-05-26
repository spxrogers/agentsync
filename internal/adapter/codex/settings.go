// Package codex implements the Codex CLI adapter for agentsync.
//
// settings.go: TOML-aware per-key merge for ~/.codex/config.toml. agentsync
// owns the `[mcp_servers.*]` section; the user's other keys (model,
// approval_policy, sandbox_mode, [plugins.*], …) are FOREIGN and must survive a
// write. The merge currency is a map[string]any (same as the JSON key-merge):
// the rendered op carries JSON (`{"mcp_servers": {...}}`) so the render
// pipeline's pointer/ownership machinery is format-agnostic, and only the
// on-disk file is TOML — parsed and re-emitted here.
//
// v1 trade-off: TOML comments and key ordering in the original file are NOT
// preserved (the file is decoded to a map and re-marshalled). Foreign keys and
// values are preserved; only formatting/comments are lost. Comment-preserving
// mutation is deferred to v1.x (matches the README "Known limits", same as
// opencode.json).
package codex

import (
	"fmt"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// MergeTOML merges ours (a JSON-decoded owned subtree, e.g. {"mcp_servers":…})
// into existing TOML content, removing ownedPointers no longer present in ours.
// Foreign top-level keys and tables are preserved. The result is re-emitted as
// TOML.
func MergeTOML(existing []byte, ours map[string]any, ownedPointers []string) ([]byte, error) {
	existingMap := map[string]any{}
	if len(existing) > 0 {
		if err := toml.Unmarshal(existing, &existingMap); err != nil {
			return nil, fmt.Errorf("parse config.toml: %w", err)
		}
		if existingMap == nil {
			existingMap = map[string]any{}
		}
	}
	merged, _, _ := jsonkeys.MergeKeys(existingMap, ours, ownedPointers)
	out, err := toml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal merged config.toml: %w", err)
	}
	return out, nil
}
