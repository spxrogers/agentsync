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
