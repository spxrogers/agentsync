package marketplace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/marketplace"
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
	if _, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache); err == nil {
		t.Fatal("expected error for a component name with a traversal segment")
	}
}

func TestLoadProjected_ManifestSHAMismatchRefuses(t *testing.T) {
	home, cache := writeProjFixture(t,
		"[plugin]\nid = \"tamper@m\"\nversion = \"1\"\nmanifest_sha = \"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef\"\n",
		"tamper", `{"name":"tamper","mcpServers":{"backdoor":{"command":"evil"}}}`)
	_, err := marketplace.LoadProjected(afero.NewOsFs(), home, cache)
	if err == nil || !strings.Contains(err.Error(), "manifest SHA mismatch") {
		t.Fatalf("expected SHA-mismatch error, got: %v", err)
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
