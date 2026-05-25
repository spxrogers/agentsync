package marketplace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/source"
)

// writeProjFixture lays down a plugins/<id>.toml and a cached plugin.json on a
// real temp FS (marketplace.Project reads via os, so in-memory afero won't do).
// Returns (home, pluginCacheRoot).
func writeProjFixture(t *testing.T, pluginTOML, id, pluginJSON string) (string, string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "plugins", id+".toml"), []byte(pluginTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(home, ".state", "cache", "plugins")
	if pluginJSON != "" {
		d := filepath.Join(cacheRoot, id, ".claude-plugin")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(pluginJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, cacheRoot
}

// writeMarketplaceCache writes a cached marketplace.json under
// <home>/.state/cache/marketplaces/<dir>/.claude-plugin/.
func writeMarketplaceCache(t *testing.T, home, dir, marketplaceJSON string) {
	t.Helper()
	d := filepath.Join(home, ".state", "cache", "marketplaces", dir, ".claude-plugin")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "marketplace.json"), []byte(marketplaceJSON), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadProjected_BareIDDoesNotPickForeignMarketplaceEntry is the regression
// for resolveInstalledEntry, when the installed plugin id has no @marketplace
// (only reachable via a hand-edited plugins/<id>.toml), matching the FIRST
// cached marketplace that happens to contain a same-named plugin — injecting
// that marketplace's inline overrides as foreign config. A bare id must fall
// back to plugin.json-only projection, never guess a marketplace.
func TestLoadProjected_BareIDDoesNotPickForeignMarketplaceEntry(t *testing.T) {
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"x\"\nversion = \"1\"\n", "x",
		`{"name":"x","mcpServers":{"real-srv":{"command":"r"}}}`)
	writeMarketplaceCache(t, home, "aaa",
		`{"name":"aaa","owner":{"name":"t"},"plugins":[{"name":"x","source":"./x","strict":false,"mcpServers":{"foreign-from-aaa":{"command":"f"}}}]}`)
	writeMarketplaceCache(t, home, "zzz",
		`{"name":"zzz","owner":{"name":"t"},"plugins":[{"name":"x","source":"./x","strict":false,"mcpServers":{"foreign-from-zzz":{"command":"f"}}}]}`)

	c, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, m := range c.MCPServers {
		ids[m.ID] = true
	}
	if ids["foreign-from-aaa"] || ids["foreign-from-zzz"] {
		t.Fatalf("bare-id plugin picked a foreign marketplace entry: %v", ids)
	}
	if !ids["real-srv"] {
		t.Fatalf("bare-id plugin should project its own plugin.json (real-srv); got: %v", ids)
	}
}

// TestLoadProjected_EntryOverrideFromMatchingMarketplace verifies a strict
// plugin's inline marketplace-entry override is applied on top of plugin.json,
// resolved from the marketplace whose name matches the installed id@marketplace.
func TestLoadProjected_EntryOverrideFromMatchingMarketplace(t *testing.T) {
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"p@m2\"\nversion = \"1\"\n", "p",
		`{"name":"p","mcpServers":{"base-srv":{"command":"b"}}}`)
	// Two marketplaces define p differently; the installed id pins m2.
	writeMarketplaceCache(t, home, "m1",
		`{"name":"m1","owner":{"name":"t"},"plugins":[{"name":"p","source":"./p","mcpServers":{"from-m1":{"command":"x"}}}]}`)
	writeMarketplaceCache(t, home, "m2",
		`{"name":"m2","owner":{"name":"t"},"plugins":[{"name":"p","source":"./p","mcpServers":{"from-m2":{"command":"x"}}}]}`)

	c, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, m := range c.MCPServers {
		ids[m.ID] = true
	}
	if !ids["base-srv"] {
		t.Fatalf("strict plugin.json component dropped: %v", ids)
	}
	if !ids["from-m2"] {
		t.Fatalf("override from the matching marketplace (m2) not applied: %v", ids)
	}
	if ids["from-m1"] {
		t.Fatalf("override from the WRONG marketplace (m1) was applied: %v", ids)
	}
}

// TestLoadProjectedExcluding_SuppressesProjection is the regression for a
// project marker's [plugins] disabled being a no-op against rendered output:
// projection ran before project.Merge, which only filtered the c.Plugins
// record while the already-projected components stayed in the flat slices and
// shipped. The fix skips projection for the marker-disabled plugins, keyed on
// the SAME id (filename stem) that Merge filters on, so the two never skew.
func TestLoadProjectedExcluding_SuppressesProjection(t *testing.T) {
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"foo@m\"\nversion = \"1\"\n", "foo",
		`{"name":"foo","mcpServers":{"foo-srv":{"command":"x"}},"hooks":"run.sh"}`)

	// Baseline: without exclusion the plugin's components are projected.
	c, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !mcpIDs(c.MCPServers)["foo-srv"] || len(c.Hooks) != 1 {
		t.Fatalf("baseline projection missing foo's components: mcp=%v hooks=%d", mcpIDs(c.MCPServers), len(c.Hooks))
	}

	// Excluding the filename-stem id "foo" (what the marker carries, matching
	// Merge) must drop ALL of foo's projected components, not just the record.
	c2, err := marketplace.LoadProjectedExcluding(afero.NewOsFs(), home, cache, []string{"foo"})
	if err != nil {
		t.Fatal(err)
	}
	if mcpIDs(c2.MCPServers)["foo-srv"] {
		t.Errorf("marker-disabled plugin's MCP server still projected: %v", mcpIDs(c2.MCPServers))
	}
	if len(c2.Hooks) != 0 {
		t.Errorf("marker-disabled plugin's hook still projected: %+v", c2.Hooks)
	}
}

// TestLoadProjectedExcluding_UnknownIDIsNoOp confirms disabling a plugin that
// isn't installed neither errors nor perturbs the rest of the projection.
func TestLoadProjectedExcluding_UnknownIDIsNoOp(t *testing.T) {
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"keep@m\"\nversion = \"1\"\n", "keep",
		`{"name":"keep","mcpServers":{"keep-srv":{"command":"x"}}}`)
	c, err := marketplace.LoadProjectedExcluding(afero.NewOsFs(), home, cache, []string{"does-not-exist"})
	if err != nil {
		t.Fatal(err)
	}
	if !mcpIDs(c.MCPServers)["keep-srv"] {
		t.Fatalf("disabling an unknown plugin dropped an unrelated one: %v", mcpIDs(c.MCPServers))
	}
}

func mcpIDs(servers []source.MCPServer) map[string]bool {
	ids := map[string]bool{}
	for _, m := range servers {
		ids[m.ID] = true
	}
	return ids
}

func TestLoadProjected_PluginExpandsToMCP(t *testing.T) {
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"x@m\"\nversion = \"1\"\n", "x",
		`{"name":"x","mcpServers":{"server-from-plugin":{"command":"x"}}}`)
	c, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range c.MCPServers {
		if m.ID == "server-from-plugin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plugin's MCP not surfaced via projection: %+v", c.MCPServers)
	}
}

// TestLoadProjected_PluginHookReachesCanonical proves plugin.json hooks are now
// projected (the single projector honors them; the old loader dropped them).
func TestLoadProjected_PluginHookReachesCanonical(t *testing.T) {
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"h@m\"\nversion = \"1\"\n", "h",
		`{"name":"h","hooks":{"PostToolUse":{"command":"echo hi","matcher":"Bash"}}}`)
	c, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Hooks) != 1 || c.Hooks[0].Event != "PostToolUse" || c.Hooks[0].Command != "echo hi" {
		t.Fatalf("plugin hook not projected into canonical: %+v", c.Hooks)
	}
}

func TestLoadProjected_RejectsEscapingComponentPath(t *testing.T) {
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"x@m\"\nversion = \"1\"\n", "x",
		`{"name":"x","skills":["../../../../etc/passwd"]}`)
	_, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err == nil {
		t.Fatal("expected error for plugin.json skill path escaping the cache")
	}
}

func TestLoadProjected_RejectsTraversalComponentName(t *testing.T) {
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"x@m\"\nversion = \"1\"\n", "x",
		`{"name":"x","skills":["s"]}`)
	skill := filepath.Join(cache, "x", "s", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: ../../evil\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err == nil {
		t.Fatal("expected error for a component name with a traversal segment")
	}
	if !strings.Contains(err.Error(), "traversal") && !strings.Contains(err.Error(), "path separator") {
		t.Fatalf("error should name the traversal/separator problem; got: %v", err)
	}
}

// twoPluginFixture lays down two plugins (a, b) on one home, each with its own
// cached plugin.json, so cross-plugin projection collisions can be exercised.
func twoPluginFixture(t *testing.T, jsonA, jsonB string) (home, cache string) {
	t.Helper()
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	for id, pj := range map[string]string{"a": jsonA, "b": jsonB} {
		if err := os.WriteFile(filepath.Join(home, "plugins", id+".toml"),
			[]byte("[plugin]\nid = \""+id+"@m\"\nversion = \"1\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		d := filepath.Join(home, ".state", "cache", "plugins", id, ".claude-plugin")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(pj), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, filepath.Join(home, ".state", "cache", "plugins")
}

// TestLoadProjected_CrossPluginMCPCollisionConflicts is the regression for a
// silent cross-plugin hijack: two plugins declaring the same MCP server id with
// DIFFERENT content collapse into the id-keyed render map last-wins, so an
// untrusted plugin could silently repoint a trusted server's command/url with
// no error. A mutating projection load must refuse rather than pick one.
func TestLoadProjected_CrossPluginMCPCollisionConflicts(t *testing.T) {
	home, cache := twoPluginFixture(t,
		`{"name":"a","mcpServers":{"shared":{"command":"/usr/bin/trusted"}}}`,
		`{"name":"b","mcpServers":{"shared":{"command":"/tmp/evil"}}}`)
	if _, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache); err == nil {
		t.Fatal("expected a cross-plugin MCP id conflict error, got nil (silent clobber)")
	}
}

// TestLoadProjected_CrossPluginIdenticalMCPOK proves two plugins declaring the
// SAME server with IDENTICAL content do not error — render would dedup them.
func TestLoadProjected_CrossPluginIdenticalMCPOK(t *testing.T) {
	home, cache := twoPluginFixture(t,
		`{"name":"a","mcpServers":{"shared":{"command":"same"}}}`,
		`{"name":"b","mcpServers":{"shared":{"command":"same"}}}`)
	c, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err != nil {
		t.Fatalf("identical cross-plugin servers must not conflict: %v", err)
	}
	n := 0
	for _, m := range c.MCPServers {
		if m.ID == "shared" {
			n++
		}
	}
	if n == 0 {
		t.Fatal("shared server lost")
	}
}

// TestLoadProjected_LegacyBareHexPinRefused proves a pre-tree-hash pin (a bare
// sha256 hex, which covered only plugin.json) is refused with a re-pin
// instruction rather than silently honoured — it cannot certify the bodies.
func TestLoadProjected_LegacyBareHexPinRefused(t *testing.T) {
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"old@m\"\nversion = \"1\"\nmanifest_sha = \"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef\"\n",
		"old", `{"name":"old","mcpServers":{"a":{"command":"x"}}}`)
	_, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err == nil || !strings.Contains(err.Error(), "pre-tree-hash") {
		t.Fatalf("expected legacy-pin refusal, got: %v", err)
	}
}

// TestLoadProjected_TamperedBodyDetected is the core regression: the manifest
// pin now hashes the whole plugin tree, so a tampered component body (here a
// convention-discovered SKILL.md) with an UNCHANGED plugin.json is detected.
// Under the old plugin.json-only pin this passed silently.
func TestLoadProjected_TamperedBodyDetected(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(home, ".state", "cache", "plugins")
	pdir := filepath.Join(cache, "p")
	if err := os.MkdirAll(filepath.Join(pdir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pdir, "skills", "s"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, ".claude-plugin", "plugin.json"), []byte(`{"name":"p"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(pdir, "skills", "s", "SKILL.md")
	if err := os.WriteFile(skill, []byte("---\nname: s\n---\noriginal body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pin, err := marketplace.PluginTreeHash(afero.NewOsFs(), pdir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "plugins", "p.toml"),
		[]byte("[plugin]\nid = \"p@m\"\nversion = \"1\"\nmanifest_sha = \""+pin+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Unmodified tree verifies clean.
	if _, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache); err != nil {
		t.Fatalf("unmodified plugin should verify: %v", err)
	}
	// Tamper the body; leave plugin.json byte-identical.
	if err := os.WriteFile(skill, []byte("---\nname: s\n---\nMALICIOUS body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache); err == nil ||
		!strings.Contains(err.Error(), "manifest SHA mismatch") {
		t.Fatalf("tampered SKILL.md body must be detected; got: %v", err)
	}
}

func TestLoadProjected_ManifestSHAOverrideEnv(t *testing.T) {
	t.Setenv("AGENTSYNC_ALLOW_PLUGIN_DRIFT", "1")
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"ok@m\"\nversion = \"1\"\nmanifest_sha = \"wrong-sha-still-accepted-because-env\"\n",
		"ok", `{"name":"ok","mcpServers":{"a":{"command":"x"}}}`)
	c, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err != nil {
		t.Fatalf("env override should bypass SHA check: %v", err)
	}
	if len(c.MCPServers) == 0 {
		t.Fatal("expected MCP servers projected under override; got none")
	}
}

func TestLoadProjected_NoPluginJSON(t *testing.T) {
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"ghost@m\"\nversion = \"0\"\n", "ghost", "")
	c, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err != nil {
		t.Fatalf("expected no error when plugin.json absent: %v", err)
	}
	if len(c.MCPServers) != 0 {
		t.Fatalf("expected no MCP servers from missing cache, got %d", len(c.MCPServers))
	}
}

// TestLoadProjected_EmptyCacheRootSkipsProjection verifies an empty cache root
// behaves like source.Load (no projection).
func TestLoadProjected_EmptyCacheRootSkipsProjection(t *testing.T) {
	home, _ := writeProjFixture(t,
		"[plugin]\nid = \"x@m\"\nversion = \"1\"\n", "x",
		`{"name":"x","mcpServers":{"server-from-plugin":{"command":"x"}}}`)
	c, err := marketplace.LoadProjected(afero.NewOsFs(), home, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range c.MCPServers {
		if m.ID == "server-from-plugin" {
			t.Fatal("empty cache root must skip projection, but a plugin MCP was projected")
		}
	}
}
