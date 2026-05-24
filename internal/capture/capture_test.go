package capture_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/capture"
	"github.com/spxrogers/agentsync/internal/source"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCapture_ReReferencesAndPreserves drives Capture directly (no CLI) to prove
// the boundary both re-references a resolved secret AND preserves the
// source-only fields (agents + enabled) the rendered destination never carries.
func TestCapture_ReReferencesAndPreserves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOK", "live-secret-value")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	// Existing source: templated secret + source-only fields.
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"), ""+
		"[server]\n"+
		"type = \"stdio\"\n"+
		"command = \"npx\"\n"+
		"agents = [\"claude\"]\n"+
		"enabled = false\n"+
		"[server.env]\n"+
		"GH = \"${secret:TOK}\"\n")

	// Ingested-from-dest: secret resolved to cleartext, source-only fields absent.
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID: "srv",
		Server: source.MCPServerSpec{
			Type:    "stdio",
			Command: "npx",
			Env:     map[string]string{"GH": "live-secret-value"},
		},
	}}}

	res, err := capture.Capture(home, ingested, capture.Opts{})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(res.Written) != 1 || res.Written[0] != "mcp/srv.toml" {
		t.Fatalf("unexpected Result.Written: %v", res.Written)
	}

	got, ok, err := source.ReadMCP(home, "srv")
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	if got.Server.Env["GH"] != "${secret:TOK}" {
		t.Errorf("secret not re-referenced: GH=%q", got.Server.Env["GH"])
	}
	if len(got.Server.Agents) != 1 || got.Server.Agents[0] != "claude" {
		t.Errorf("source-only agents not preserved: %v", got.Server.Agents)
	}
	if got.Server.Enabled == nil || *got.Server.Enabled {
		t.Errorf("source-only enabled not preserved: %v", got.Server.Enabled)
	}
}

// TestCapture_FirstImportNoSource exercises the path where no canonical source
// exists yet (first import): there is nothing to re-reference against, so the
// ingested values are written verbatim and Capture must not error.
func TestCapture_FirstImportNoSource(t *testing.T) {
	home := t.TempDir()
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "srv",
		Server: source.MCPServerSpec{Type: "stdio", Command: "npx"},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
		t.Fatalf("capture (first import): %v", err)
	}
	got, ok, err := source.ReadMCP(home, "srv")
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	if got.Server.Command != "npx" {
		t.Errorf("first-import write-as-is failed: command=%q", got.Server.Command)
	}
}
