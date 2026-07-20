package marketplace_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/marketplace"
)

// TestLoadProjected_TamperGuardAndBodiesUseSameFs is the regression for issue
// #162 item E: the tamper guard hashed the injected fs while projection read the
// real OS filesystem, so off the real fs the guard and the data it protected
// could read DIFFERENT trees. Here the entire plugin tree — including the skill
// body — lives ONLY on an in-memory fs. Before the fix, projection's os.ReadFile
// found nothing and projected zero skills; after it, the body is read through the
// SAME fs the guard hashed. Tampering that mem-fs body then flips the guard.
func TestLoadProjected_TamperGuardAndBodiesUseSameFs(t *testing.T) {
	fs := afero.NewMemMapFs()
	home := "/home/.agentsync"
	cache := filepath.Join(home, ".state", "cache", "plugins")
	pdir := filepath.Join(cache, "p")
	write := func(p, body string) {
		t.Helper()
		if err := afero.WriteFile(fs, p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(pdir, ".claude-plugin", "plugin.json"), `{"name":"p"}`)
	skill := filepath.Join(pdir, "skills", "s", "SKILL.md")
	write(skill, "---\nname: s\n---\nmemfs-only body\n")

	pin, err := marketplace.PluginTreeHash(fs, pdir)
	if err != nil {
		t.Fatalf("PluginTreeHash: %v", err)
	}
	write(filepath.Join(home, "plugins", "p.toml"),
		"[plugin]\nid = \"p@m\"\nversion = \"1\"\nmanifest_sha = \""+pin+"\"\n")

	c, err := marketplace.LoadProjected(fs, home, cache)
	if err != nil {
		t.Fatalf("LoadProjected: %v", err)
	}
	if len(c.Skills) != 1 || c.Skills[0].Name != "s" {
		t.Fatalf("skill not projected from the injected fs (bodies read via os.* instead?): %+v", c.Skills)
	}
	if !strings.Contains(c.Skills[0].Body, "memfs-only body") {
		t.Fatalf("skill body not read through the injected fs: %q", c.Skills[0].Body)
	}

	// Tamper the body on the SAME fs the guard hashes; the mismatch must surface.
	write(skill, "---\nname: s\n---\nMALICIOUS body\n")
	if _, err := marketplace.LoadProjected(fs, home, cache); err == nil ||
		!strings.Contains(err.Error(), "manifest SHA mismatch") {
		t.Fatalf("tampered body on the injected fs must be detected; got: %v", err)
	}
}
