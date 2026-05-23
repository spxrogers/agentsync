package secrets

import "testing"

// TestFlatten_RejectsAmbiguousDuplicateKey is the regression for a silent,
// nondeterministic secret-resolution footgun: two distinct TOML constructs —
// a quoted bare key literally named "github.token" and a nested [github]
// table with a token field — both flatten to the dotted key "github.token".
// The previous flatten kept whichever the random map iteration visited last,
// so `${secret:github.token}` could resolve to a different value run to run.
// flatten must reject the collision instead of silently picking one.
func TestFlatten_RejectsAmbiguousDuplicateKey(t *testing.T) {
	m := map[string]any{
		"github.token": "A",
		"github":       map[string]any{"token": "B"},
	}
	if _, err := flatten("", m); err == nil {
		t.Fatal("expected an error for an ambiguous duplicate flattened key, got nil")
	}
}

// TestFlatten_AllowsDistinctKeys confirms the collision guard does not reject
// legitimate, distinct nested keys.
func TestFlatten_AllowsDistinctKeys(t *testing.T) {
	m := map[string]any{
		"github": map[string]any{"token": "A", "user": "B"},
		"openai": map[string]any{"key": "C"},
	}
	out, err := flatten("", m)
	if err != nil {
		t.Fatalf("unexpected error on distinct keys: %v", err)
	}
	if out["github.token"] != "A" || out["github.user"] != "B" || out["openai.key"] != "C" {
		t.Fatalf("distinct keys flattened wrong: %+v", out)
	}
}
