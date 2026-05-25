package cli

import "testing"

// TestValidateMCPID_RejectsDegenerate mirrors source.ValidateComponentID: a
// lone "." or an all-whitespace id passes the separator/traversal check but
// would author mcp/..toml or "mcp/ .toml" — confusing artifacts no later
// command can address cleanly. The CLI gate should reject them early with a
// clear message rather than defer to the write boundary.
func TestValidateMCPID_RejectsDegenerate(t *testing.T) {
	bad := []string{"", ".", " ", "   ", "\t", "a/b", "a\\b", "a..b"}
	for _, s := range bad {
		if err := validateMCPID(s); err == nil {
			t.Errorf("validateMCPID(%q) = nil; want error", s)
		}
	}
	good := []string{"github", "my-server", "a.b.c", "srv1"}
	for _, s := range good {
		if err := validateMCPID(s); err != nil {
			t.Errorf("validateMCPID(%q) = %v; want nil", s, err)
		}
	}
}

// TestDeriveMarketplaceSlug_NeverEmpty proves the empty-fallback runs AFTER
// sanitisation: a punctuation-only source like "..." sanitises to "" and must
// still yield the usable "marketplace" slug, not "" (which would author
// marketplaces/.toml and a marketplaces/_ cache dir).
func TestDeriveMarketplaceSlug_NeverEmpty(t *testing.T) {
	for _, in := range []string{"...", ".", "//", "   ", "https://"} {
		if got := deriveMarketplaceSlug(in); got == "" {
			t.Errorf("deriveMarketplaceSlug(%q) = %q; want non-empty", in, got)
		}
	}
	// A normal source still derives a sensible, non-empty slug.
	if got := deriveMarketplaceSlug("https://github.com/obra/superpowers.git"); got == "" {
		t.Errorf("deriveMarketplaceSlug of a real URL returned empty")
	}
}
