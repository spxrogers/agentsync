package marketplace_test

import (
	"encoding/json"
	"testing"

	"github.com/spxrogers/agentsync/internal/marketplace"
)

func TestParseMarketplace_StringSource(t *testing.T) {
	raw := []byte(`{
		"name": "x",
		"owner": {"name": "y"},
		"plugins": [{"name": "p", "source": "./plugins/p"}]
	}`)
	var m marketplace.Marketplace
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Plugins[0].Source.Relative != "./plugins/p" {
		t.Fatalf("source = %+v", m.Plugins[0].Source)
	}
}

func TestParseMarketplace_ObjectSource(t *testing.T) {
	raw := []byte(`{
		"name": "x", "owner": {"name": "y"},
		"plugins": [{"name": "p", "source": {"source":"github","repo":"o/r","ref":"v1"}}]
	}`)
	var m marketplace.Marketplace
	_ = json.Unmarshal(raw, &m)
	if m.Plugins[0].Source.Kind != "github" || m.Plugins[0].Source.Repo != "o/r" {
		t.Fatalf("source = %+v", m.Plugins[0].Source)
	}
}

func TestParseMarketplace_FullFields(t *testing.T) {
	raw := []byte(`{
		"$schema": "http://example.com/schema.json",
		"name": "my-marketplace",
		"owner": {"name": "Alice", "email": "alice@example.com"},
		"description": "A test marketplace",
		"version": "1.0.0",
		"metadata": {"pluginRoot": "plugins/"},
		"plugins": [
			{
				"name": "plugin-a",
				"source": "./plugins/a",
				"description": "Plugin A",
				"strict": false,
				"mcpServers": {"srv": {"command": "run"}}
			},
			{
				"name": "plugin-b",
				"source": {"source": "npm", "package": "@scope/pkg", "version": "2.0.0"}
			}
		],
		"allowCrossMarketplaceDependenciesOn": ["other-mp"]
	}`)

	var m marketplace.Marketplace
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if m.Schema != "http://example.com/schema.json" {
		t.Errorf("schema = %q", m.Schema)
	}
	if m.Owner.Email != "alice@example.com" {
		t.Errorf("owner.email = %q", m.Owner.Email)
	}
	if m.Metadata == nil || m.Metadata.PluginRoot != "plugins/" {
		t.Errorf("metadata = %v", m.Metadata)
	}
	if len(m.AllowCrossMarketplaceDependenciesOn) != 1 {
		t.Errorf("cross deps = %v", m.AllowCrossMarketplaceDependenciesOn)
	}

	a := m.Plugins[0]
	if a.Source.Relative != "./plugins/a" {
		t.Errorf("plugin-a source = %+v", a.Source)
	}
	if a.Strict == nil || *a.Strict {
		t.Errorf("plugin-a strict should be false")
	}
	if a.MCPServers["srv"] == nil {
		t.Errorf("plugin-a mcpServers missing srv")
	}

	b := m.Plugins[1]
	if b.Source.Kind != "npm" || b.Source.Package != "@scope/pkg" || b.Source.Version != "2.0.0" {
		t.Errorf("plugin-b source = %+v", b.Source)
	}
}

func TestSource_RoundTrip(t *testing.T) {
	// Relative source: marshal → JSON string
	s := marketplace.Source{Relative: "./plugins/foo"}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `"./plugins/foo"` {
		t.Fatalf("marshal relative = %s", data)
	}

	var back marketplace.Source
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Relative != "./plugins/foo" {
		t.Fatalf("unmarshal relative = %+v", back)
	}
}

func TestSource_GitSubdir(t *testing.T) {
	raw := `{"source":"git-subdir","repo":"https://github.com/o/r","path":"tools/my-plugin","ref":"main"}`
	var s marketplace.Source
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if s.Kind != "git-subdir" {
		t.Errorf("kind = %q", s.Kind)
	}
	if s.Path != "tools/my-plugin" {
		t.Errorf("path = %q", s.Path)
	}
}

func TestPluginManifest_Parse(t *testing.T) {
	raw := `{
		"name": "my-plugin",
		"version": "1.2.3",
		"mcpServers": {
			"server-a": {"type": "stdio", "command": "run-a"},
			"server-b": {"type": "http", "url": "http://localhost:8080"}
		},
		"skills": ["skill-one", "skill-two"],
		"lspServers": {
			"lsp-a": {"command": "lang-server"}
		}
	}`
	var pm marketplace.PluginManifest
	if err := json.Unmarshal([]byte(raw), &pm); err != nil {
		t.Fatal(err)
	}
	if pm.Name != "my-plugin" {
		t.Errorf("name = %q", pm.Name)
	}
	if pm.Version != "1.2.3" {
		t.Errorf("version = %q", pm.Version)
	}
	if len(pm.MCPServers) != 2 {
		t.Errorf("mcpServers count = %d", len(pm.MCPServers))
	}
	if len(pm.LSPServers) != 1 {
		t.Errorf("lspServers count = %d", len(pm.LSPServers))
	}
}
