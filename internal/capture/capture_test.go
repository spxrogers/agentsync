package capture_test

import (
	"os"
	"path/filepath"
	"strings"
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

// TestCapture_NoLeakOnStructuralEdit drives the full dest->source funnel for the
// exact scenario that leaked: a user adds a flag to an MCP server's args in the
// native UI (shifting the secret to a new index), then write-back is captured.
// The positional re-reference misses the shifted index; the value-based fallback
// must restore the ${secret:…} placeholder so the resolved credential is NEVER
// persisted into ~/.agentsync.
func TestCapture_NoLeakOnStructuralEdit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOK", "ghp_live_credential_value")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"), ""+
		"[server]\n"+
		"type = \"stdio\"\n"+
		"command = \"npx\"\n"+
		"args = [\"--token\", \"${secret:TOK}\"]\n")

	// Native edit prepended "--verbose"; the secret resolved to cleartext sits
	// at the new index 2.
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID: "srv",
		Server: source.MCPServerSpec{
			Type:    "stdio",
			Command: "npx",
			Args:    []string{"--verbose", "--token", "ghp_live_credential_value"},
		},
	}}}

	if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
		t.Fatalf("capture: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "mcp", "srv.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ghp_live_credential_value") {
		t.Fatalf("LEAK: resolved secret persisted into source:\n%s", raw)
	}
	got, _, _ := source.ReadMCP(home, "srv")
	if len(got.Server.Args) != 3 || got.Server.Args[2] != "${secret:TOK}" {
		t.Fatalf("shifted secret not re-referenced: %v", got.Server.Args)
	}
}

// TestCapture_RejectsTraversalID is the regression for an arbitrary-file-write
// primitive via import/capture: an ingested component id/event (taken from a
// foreign / synced / project-supplied native config) was joined straight into a
// source path with no validation, so `import claude:mcp:'../../../x'` wrote a
// .toml OUTSIDE ~/.agentsync. The source write boundary must reject traversal.
func TestCapture_RejectsTraversalID(t *testing.T) {
	home := t.TempDir()
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "../../../../escape",
		Server: source.MCPServerSpec{Type: "stdio", Command: "x"},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err == nil {
		t.Fatal("capture must reject a traversal component id, got nil error")
	}
}

// TestCapture_FailsClosedOnLoadError proves the secret boundary fails CLOSED
// when the current source cannot be loaded. A malformed file anywhere in the
// tree makes source.Load error; the previous code then skipped both
// re-referencing AND the warning and wrote the ingested values verbatim —
// silently persisting a resolved cleartext secret into ~/.agentsync. Capture
// must instead refuse to write and surface the load error.
func TestCapture_FailsClosedOnLoadError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOK", "live-secret-value")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	// Existing source: the server templated with the secret.
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"), ""+
		"[server]\n"+
		"type = \"stdio\"\n"+
		"command = \"npx\"\n"+
		"[server.env]\n"+
		"GH = \"${secret:TOK}\"\n")
	// An unrelated malformed file makes source.Load return an error.
	writeFile(t, filepath.Join(home, "mcp", "broken.toml"), "[server\ncommand = \"x\"\n")

	// Ingested-from-dest: apply resolved ${secret:TOK} to its cleartext.
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID: "srv",
		Server: source.MCPServerSpec{
			Type:    "stdio",
			Command: "npx",
			Env:     map[string]string{"GH": "live-secret-value"},
		},
	}}}

	if _, err := capture.Capture(home, ingested, capture.Opts{}); err == nil {
		t.Fatal("expected Capture to fail closed when source cannot be loaded, got nil error")
	}
	// The original templated source must be intact — no cleartext persisted.
	got, ok, rerr := source.ReadMCP(home, "srv")
	if rerr != nil {
		t.Fatalf("read back: %v", rerr)
	}
	if ok && got.Server.Env["GH"] == "live-secret-value" {
		t.Fatalf("LEAK: resolved cleartext secret persisted into source: GH=%q", got.Server.Env["GH"])
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
