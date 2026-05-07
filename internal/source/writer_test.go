package source_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestWriteMCP_RoundTrip(t *testing.T) {
	home := t.TempDir()

	m := source.MCPServer{
		ID: "github",
		Server: source.MCPServerSpec{
			Type:    "stdio",
			Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-github"},
		},
	}

	if err := source.WriteMCP(home, "github", m); err != nil {
		t.Fatalf("WriteMCP: %v", err)
	}

	// File must exist and be valid TOML that round-trips back.
	dest := filepath.Join(home, "mcp", "github.toml")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var got source.MCPServer
	if err := toml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Server.Command != "npx" {
		t.Errorf("command = %q, want npx", got.Server.Command)
	}
	if len(got.Server.Args) != 2 || got.Server.Args[0] != "-y" {
		t.Errorf("args = %v", got.Server.Args)
	}
}

func TestWriteMCP_Overwrites(t *testing.T) {
	home := t.TempDir()

	first := source.MCPServer{
		ID:     "s",
		Server: source.MCPServerSpec{Type: "stdio", Command: "old"},
	}
	if err := source.WriteMCP(home, "s", first); err != nil {
		t.Fatal(err)
	}

	second := source.MCPServer{
		ID:     "s",
		Server: source.MCPServerSpec{Type: "stdio", Command: "new"},
	}
	if err := source.WriteMCP(home, "s", second); err != nil {
		t.Fatal(err)
	}

	// Reload and verify it now matches second.
	fs := afero.NewOsFs()
	servers, err := source.Load(fs, home)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(servers.MCPServers) != 1 {
		t.Fatalf("servers = %d, want 1", len(servers.MCPServers))
	}
	if servers.MCPServers[0].Server.Command != "new" {
		t.Errorf("command = %q, want new", servers.MCPServers[0].Server.Command)
	}
}

func TestWritePlugin_RoundTrip(t *testing.T) {
	home := t.TempDir()

	p := source.Plugin{
		ID: "myplugin",
		Plugin: source.PluginSpec{
			ID:      "myplugin",
			Version: "1.0.0",
		},
	}
	if err := source.WritePlugin(home, "myplugin", p); err != nil {
		t.Fatalf("WritePlugin: %v", err)
	}

	dest := filepath.Join(home, "plugins", "myplugin.toml")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var got source.Plugin
	if err := toml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Plugin.Version != "1.0.0" {
		t.Errorf("version = %q, want 1.0.0", got.Plugin.Version)
	}
}

func TestWriteMarketplace_RoundTrip(t *testing.T) {
	home := t.TempDir()

	m := source.Marketplace{
		Name: "acme",
		Marketplace: source.MarketplaceSpec{
			URL: "https://example.com/registry",
			Ref: "main",
		},
	}
	if err := source.WriteMarketplace(home, "acme", m); err != nil {
		t.Fatalf("WriteMarketplace: %v", err)
	}

	dest := filepath.Join(home, "marketplaces", "acme.toml")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var got source.Marketplace
	if err := toml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Marketplace.URL != "https://example.com/registry" {
		t.Errorf("url = %q", got.Marketplace.URL)
	}
}
