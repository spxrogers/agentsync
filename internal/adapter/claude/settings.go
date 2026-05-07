package claude

import "github.com/spxrogers/agentsync/internal/jsonkeys"

// MergeKeys is preserved as a re-export for backward compat with claude
// internal callers; new callers should import internal/jsonkeys directly.
func MergeKeys(existing, ours map[string]any, ownedPointers []string) (map[string]any, []string, []string) {
	return jsonkeys.MergeKeys(existing, ours, ownedPointers)
}
