package source_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestMarketplaceSpec_CanonicalRoundTripDropsCacheFields pins the DOCUMENTED
// behavior (issue #171): the canonical MarketplaceSpec deliberately does not model
// the CLI's head_sha / name fetch-cache keys, so a load->canonical-write round-trip
// through source drops them. The oracle is the on-disk file. (The CLI never loses
// them in practice — it reads/writes them via its own marketplaceTOMLSpec; this
// test documents the canonical model's intentional subset, per source.MarketplaceSpec.)
func TestMarketplaceSpec_CanonicalRoundTripDropsCacheFields(t *testing.T) {
	fs := afero.NewOsFs()
	home := t.TempDir()
	mpPath := filepath.Join(home, "marketplaces", "foo.toml")
	if err := os.MkdirAll(filepath.Dir(mpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Spec-complete on-disk fixture: canonical fields PLUS the CLI cache fields.
	fixture := "" +
		"[marketplace]\n" +
		"url = \"https://example.com/mp.git\"\n" +
		"ref = \"main\"\n" +
		"default_update_mode = \"pin\"\n" +
		"head_sha = \"deadbeefcafe\"\n" +
		"name = \"Foo Marketplace\"\n"
	if err := os.WriteFile(mpPath, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := source.Load(fs, home)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(c.Marketplaces) != 1 {
		t.Fatalf("want 1 marketplace, got %d", len(c.Marketplaces))
	}
	m := c.Marketplaces[0]
	// Canonical fields are modeled and preserved.
	if m.Marketplace.URL != "https://example.com/mp.git" || m.Marketplace.Ref != "main" || m.Marketplace.DefaultUpdateMode != "pin" {
		t.Fatalf("canonical fields not loaded: %+v", m.Marketplace)
	}
	// Name is re-derived from the FILENAME, not the persisted `name` key.
	if m.Name.Unverified() != "foo" {
		t.Fatalf("Name should be re-derived from the filename (foo), got %q", m.Name.Unverified())
	}

	// Canonical write-back drops the unmodeled cache fields — the documented subset.
	if err := source.WriteMarketplace(home, "foo", m); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(mpPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "head_sha") || strings.Contains(string(got), "name =") {
		t.Fatalf("canonical round-trip should DROP the head_sha/name cache fields (documented in source.MarketplaceSpec); on-disk file:\n%s", got)
	}
	// Canonical fields survive the round-trip.
	for _, want := range []string{"url =", "ref =", "default_update_mode ="} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("canonical round-trip dropped %q; on-disk file:\n%s", want, got)
		}
	}
}
