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

// TestExtraNativeKeys_SkipsReservedAndModeled pins the CAPTURE side of the "__"
// reserved namespace, symmetric with MergeExtra's render-side skip. A native
// config's modeled keys are excluded (they are represented in the canonical
// model), and a "__"-prefixed key is excluded too — agentsync owns that namespace
// on both sides, so a stray native "__" key can never be captured into the shared
// Extra only to be dropped on the next render (a silent lossy round-trip). Every
// other unmodeled native key is captured verbatim.
func TestExtraNativeKeys_SkipsReservedAndModeled(t *testing.T) {
	raw := map[string]any{
		"command":         "npx",      // modeled — excluded
		"timeout":         30,         // unmodeled native passthrough — captured
		"cwd":             "/srv",     // unmodeled native passthrough — captured
		"__block_version": "v2",       // reserved namespace — excluded
		"__anything":      "internal", // reserved namespace — excluded
	}

	extra := ExtraNativeKeys(raw, "command", "args", "env")

	if got := extra["timeout"]; got != 30 {
		t.Errorf("unmodeled native key dropped: extra[timeout]=%v, want 30", got)
	}
	if got := extra["cwd"]; got != "/srv" {
		t.Errorf("unmodeled native key dropped: extra[cwd]=%v, want /srv", got)
	}
	if _, ok := extra["command"]; ok {
		t.Errorf("modeled key captured into Extra: %v", extra)
	}
	if _, ok := extra["__block_version"]; ok {
		t.Errorf("reserved key __block_version captured into Extra: %v", extra)
	}
	if _, ok := extra["__anything"]; ok {
		t.Errorf("reserved key __anything captured into Extra: %v", extra)
	}
}

// TestExtraNativeKeys_AllReservedOrModeledReturnsNil proves the nil-Extra
// contract survives the "__" skip: if every native key is modeled or reserved,
// ExtraNativeKeys returns nil (not an empty map), so the caller omits the
// canonical [server.extra] table entirely.
func TestExtraNativeKeys_AllReservedOrModeledReturnsNil(t *testing.T) {
	raw := map[string]any{
		"command":         "npx", // modeled
		"__block_version": "v2",  // reserved
	}
	if extra := ExtraNativeKeys(raw, "command"); extra != nil {
		t.Errorf("expected nil Extra when only modeled/reserved keys present, got %v", extra)
	}
}
