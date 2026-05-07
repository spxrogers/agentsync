package opencode_test

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter/opencode"
)

func TestMergeJSONC_PreservesForeignAndAddsOurs(t *testing.T) {
	existing := []byte(`{
  // top comment
  "foreign": 1,
  "mcp": {
    "stale": {} // soon-to-be-removed
  }
}`)
	ours := map[string]any{
		"mcp": map[string]any{
			"github": map[string]any{"command": "npx"},
		},
	}
	owned := []string{"/mcp/stale", "/mcp/github"}

	out, err := opencode.MergeJSONC(existing, ours, owned)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	// foreign key must survive
	if !strings.Contains(s, `"foreign"`) {
		t.Fatalf("foreign key lost:\n%s", s)
	}
	// stale owned key must be removed
	if strings.Contains(s, "stale") {
		t.Fatalf("stale should be removed:\n%s", s)
	}
	// our new key must appear
	if !strings.Contains(s, `"github"`) {
		t.Fatalf("github not added:\n%s", s)
	}
	// command value must be correct
	if !strings.Contains(s, `"npx"`) {
		t.Fatalf("command value missing:\n%s", s)
	}
}

func TestMergeJSONC_EmptyExistingStartsClean(t *testing.T) {
	ours := map[string]any{
		"mcp": map[string]any{
			"github": map[string]any{"command": "npx"},
		},
	}
	out, err := opencode.MergeJSONC(nil, ours, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"github"`) {
		t.Fatalf("github not added:\n%s", s)
	}
}

func TestMergeJSONC_InvalidJSONCReturnsError(t *testing.T) {
	existing := []byte(`{ not valid json at all !!!`)
	_, err := opencode.MergeJSONC(existing, map[string]any{}, nil)
	if err == nil {
		t.Fatal("expected error for invalid JSONC")
	}
}
