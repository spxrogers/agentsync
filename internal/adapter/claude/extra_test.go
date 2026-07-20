package claude

import "testing"

// TestMergeExtra_SkipsReservedKeys pins the review-loop Round-1 fix: a
// "__"-prefixed Extra key is agentsync-INTERNAL round-trip metadata (e.g.
// continuedev's __block_version / __block_schema block header), not a verbatim
// native field. Because Extra is a SHARED canonical field, MergeExtra must never
// project such a key into a destination object — otherwise one adapter's
// synthetic keys leak into every OTHER agent's native config (and re-ingest would
// recapture them as if native). A plain (non-prefixed) passthrough key must still
// merge, and a modeled key the renderer already set must still win.
func TestMergeExtra_SkipsReservedKeys(t *testing.T) {
	spec := map[string]any{"command": "npx"} // modeled key already set by the renderer
	extra := map[string]any{
		"timeout":         30,               // ordinary native passthrough — must merge
		"command":         "SHADOW",         // collides with a modeled key — must NOT clobber
		"__block_version": "v1",             // reserved internal metadata — must be skipped
		"__block_schema":  map[string]any{}, // reserved internal metadata — must be skipped
	}

	MergeExtra(spec, extra)

	if got := spec["timeout"]; got != 30 {
		t.Errorf("ordinary passthrough key dropped: spec[timeout]=%v, want 30", got)
	}
	if got := spec["command"]; got != "npx" {
		t.Errorf("modeled key clobbered by Extra: spec[command]=%v, want npx", got)
	}
	if _, ok := spec["__block_version"]; ok {
		t.Errorf("reserved key __block_version leaked into destination: %v", spec)
	}
	if _, ok := spec["__block_schema"]; ok {
		t.Errorf("reserved key __block_schema leaked into destination: %v", spec)
	}
}
