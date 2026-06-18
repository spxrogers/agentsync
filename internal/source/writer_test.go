package source_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestValidateComponentID_RejectsDegenerate guards the write boundary against
// ids that are syntactically free of separators/traversal yet still produce a
// nonsense file in the canonical source: a lone "." (→ "..toml") or an
// all-whitespace id (→ " .toml"). Every Write*/Read* routes through
// ValidateComponentID, so rejecting these here covers MCP/LSP/hook/plugin/
// skill/subagent/command in one place.
func TestValidateComponentID_RejectsDegenerate(t *testing.T) {
	for _, id := range []string{".", " ", "   ", "\t", "\n"} {
		if err := source.ValidateComponentID("mcp", id); err == nil {
			t.Errorf("ValidateComponentID(%q) = nil; want error", id)
		}
	}
	// Sanity: a normal id still passes.
	if err := source.ValidateComponentID("mcp", "github"); err != nil {
		t.Errorf("ValidateComponentID(%q) = %v; want nil", "github", err)
	}
}

// TestValidateComponentID_RejectsControlAndDeceptiveRunes guards the write
// boundary against an id that is separator-free and non-degenerate yet carries a
// terminal-control byte or a deceptive bidi/zero-width rune — a foreign id (from a
// native config on `import`/`reconcile`) that would make a pathological filename
// and, echoed back in a diagnostic, smuggle a terminal escape or spoof its own
// name. The check is tied to untrusted.Sanitize's rune set, so these must be
// rejected even though they pass the separator/traversal/degenerate gates.
func TestValidateComponentID_RejectsControlAndDeceptiveRunes(t *testing.T) {
	hostile := []string{
		"good\x1b[31m",  // ESC — terminal recolor
		"demo\r",        // CR — row spoof
		"demo\x07",      // BEL
		"name\u202egnp", // RLO (U+202E) — Trojan-Source bidi reorder
		"na\u200bme",    // ZWSP (U+200B) — invisible padding
		"x\u009bm",      // C1 CSI introducer (U+009B)
	}
	for _, id := range hostile {
		if err := source.ValidateComponentID("plugin", id); err == nil {
			t.Errorf("ValidateComponentID(%q) = nil; want rejection of control/deceptive rune", id)
		}
	}
	// A legitimate non-ASCII id (no explicit control/bidi/zero-width runes) still
	// passes — the gate strips only the spoofing set, never ordinary letters.
	for _, id := range []string{"github", "my-plugin", "naïve", "日本語"} {
		if err := source.ValidateComponentID("plugin", id); err != nil {
			t.Errorf("ValidateComponentID(%q) = %v; want nil (legitimate id)", id, err)
		}
	}
}

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
