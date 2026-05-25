package cli

import "testing"

// TestGetPointerValue_DecodesRFC6901 is the regression for status/diff
// misclassifying drift on a managed item whose id contains '~' or '/'.
// CollectPointers escapes those (~→~0, /→~1), but getPointerValue split the
// pointer without decoding, so it looked up the literal "foo~0bar" key instead
// of the real "foo~bar" key → nil source value → phantom drift forever. Every
// other pointer getter (getJSONPointer, render.getPointerOK, jsonkeys
// splitPointer) decodes; this one didn't.
func TestGetPointerValue_DecodesRFC6901(t *testing.T) {
	m := map[string]any{
		"mcpServers": map[string]any{
			"foo~bar": map[string]any{"command": "x"},
			"a/b":     map[string]any{"command": "y"},
		},
	}
	if got := getPointerValue(m, "/mcpServers/foo~0bar"); got == nil {
		t.Fatalf("did not decode ~0: nil for /mcpServers/foo~0bar")
	}
	if got := getPointerValue(m, "/mcpServers/a~1b"); got == nil {
		t.Fatalf("did not decode ~1: nil for /mcpServers/a~1b")
	}
}

// TestHashAtPointer_AbsentVsNull verifies the import seed distinguishes an
// ABSENT pointer (return "" so the seed loop skips it, matching
// render.RecordOpsState) from a present-but-null value (which hashes). Before
// the fix, an absent pointer hashed sha256("null") and got seeded as a phantom.
func TestHashAtPointer_AbsentVsNull(t *testing.T) {
	m := map[string]any{"mcpServers": map[string]any{"github": map[string]any{"command": "x"}}}
	if h := hashAtPointer(m, "/mcpServers/github"); h == "" {
		t.Fatal("present pointer must hash non-empty")
	}
	if h := hashAtPointer(m, "/mcpServers/absent"); h != "" {
		t.Fatalf("absent pointer must return the empty sentinel, got %q", h)
	}
	mn := map[string]any{"k": nil}
	if h := hashAtPointer(mn, "/k"); h == "" {
		t.Fatal("present-null must hash, not be treated as absent")
	}
}
