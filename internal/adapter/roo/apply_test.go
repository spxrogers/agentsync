package roo_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/roo"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// The roo package's main_test.go supplies the container gate (TestMain →
// testenv.MustRunInContainer()); these FS-touching tests inherit it. Do NOT add
// a second TestMain here.
//
// Roo's MCP is PROJECT-SCOPE ONLY: at user scope Paths.MCP == "" and renderMCP
// emits no write op (only a skip). Every test below therefore renders at
// adapter.ScopeProject with a non-empty project root and places the MCP servers
// on the Project overlay (c.Project = &projC), which is what render.go projects
// at project scope. All three anchor to a spec-complete ON-DISK .roo/mcp.json
// fixture per the CLAUDE.md "fidelity tests anchor to the artifact" rule.

// writeMCPFixture pre-authors an on-disk .roo/mcp.json under proj and returns its
// path. This is the artifact under test — assertions re-read from here.
func writeMCPFixture(t *testing.T, proj, content string) string {
	t.Helper()
	mcpPath := filepath.Join(proj, ".roo", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return mcpPath
}

// renderApply drives the roo adapter at project scope over the given canonical,
// letting the caller mutate the rendered ops (e.g. to inject OwnedKeys as the
// render pipeline does in production) before Apply writes them to disk.
func renderApply(t *testing.T, c source.Canonical, proj string, mutate func([]adapter.FileOp)) {
	t.Helper()
	a := roo.New(roo.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if mutate != nil {
		mutate(ops)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

// readServers re-reads the merged .roo/mcp.json and returns its mcpServers object
// decoded with plain json.Unmarshal (safe when no value exceeds 2^53).
func readServers(t *testing.T, mcpPath string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", mcpPath, err)
	}
	servers, ok := got["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing or not an object in %s: %v", mcpPath, got)
	}
	return servers
}

// projectMCP builds a canonical whose MCP servers live on the Project overlay,
// which is what roo's Render projects at project scope.
func projectMCP(servers ...source.MCPServer) source.Canonical {
	projC := source.Canonical{MCPServers: servers}
	c := projC
	c.Project = &projC
	return c
}

// TestApply_MCP_PreservesForeignServers is the roo mirror of the windsurf apply
// test, adapted for project scope: a hand-authored foreign server in
// .roo/mcp.json survives the merge alongside the agentsync-added server.
func TestApply_MCP_PreservesForeignServers(t *testing.T) {
	proj := t.TempDir()
	mcpPath := writeMCPFixture(t, proj, `{ "mcpServers": { "user-server": { "command": "mine" } } }`)

	c := projectMCP(source.MCPServer{ID: "ours", Server: source.MCPServerSpec{Command: "npx"}})
	renderApply(t, c, proj, nil)

	servers := readServers(t, mcpPath)
	if _, ok := servers["user-server"]; !ok {
		t.Fatalf("foreign server clobbered: %v", servers)
	}
	if _, ok := servers["ours"]; !ok {
		t.Fatalf("our server not added: %v", servers)
	}
}

// TestApply_MCP_OrphanRemoval proves an agentsync-owned server dropped from the
// canonical model is removed from .roo/mcp.json via the op's OwnedKeys, while a
// foreign server and the currently-rendered server both survive. The render
// pipeline injects OwnedKeys from state in production
// (internal/render/pipeline.go); at the adapter level the test sets them on the
// rendered FileOp directly, exactly as the orphan-removal path consumes them.
func TestApply_MCP_OrphanRemoval(t *testing.T) {
	proj := t.TempDir()
	mcpPath := writeMCPFixture(t, proj, `{
  "mcpServers": {
    "user-server": { "command": "mine" },
    "old-ours": { "command": "stale" }
  }
}`)

	// The Project overlay renders only "ours" (not "old-ours"), so the op's
	// Content has mcpServers.ours but not old-ours.
	c := projectMCP(source.MCPServer{ID: "ours", Server: source.MCPServerSpec{Command: "npx"}})
	renderApply(t, c, proj, func(ops []adapter.FileOp) {
		found := false
		for i := range ops {
			if ops[i].MergeStrategy == "merge-json-keys" {
				// old-ours was owned by a prior run but is absent from this
				// render, so it must be removed; ours is still owned.
				ops[i].OwnedKeys = []string{"/mcpServers/ours", "/mcpServers/old-ours"}
				found = true
			}
		}
		if !found {
			t.Fatal("no merge-json-keys op rendered for MCP")
		}
	})

	servers := readServers(t, mcpPath)
	if _, ok := servers["user-server"]; !ok {
		t.Fatalf("foreign server clobbered: %v", servers)
	}
	if _, ok := servers["ours"]; !ok {
		t.Fatalf("our server not present: %v", servers)
	}
	if _, ok := servers["old-ours"]; ok {
		t.Fatalf("orphaned owned server not removed: %v", servers)
	}
}

// TestApply_MCP_PreservesLargeForeignInt proves the json.Number claim in
// apply.go's readJSONFile: a foreign integer greater than 2^53 survives the
// read→merge→re-marshal round-trip EXACTLY, not rounded to a float64. The
// assertion decodes with json.Decoder + UseNumber() because a plain
// json.Unmarshal would itself round the value and mask the very bug guarded.
func TestApply_MCP_PreservesLargeForeignInt(t *testing.T) {
	const bigInt = "9007199254740993" // 2^53 + 1: unrepresentable exactly as float64
	proj := t.TempDir()
	mcpPath := writeMCPFixture(t, proj, `{ "mcpServers": { "user-server": { "command": "mine", "timeout": `+bigInt+` } } }`)

	c := projectMCP(source.MCPServer{ID: "ours", Server: source.MCPServerSpec{Command: "npx"}})
	renderApply(t, c, proj, nil)

	// Decode the re-read file with UseNumber so the foreign integer stays a
	// json.Number (its literal digits), never a rounded float64.
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var got map[string]any
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode %s: %v", mcpPath, err)
	}

	servers, ok := got["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing or not an object: %v", got)
	}
	// Foreign server preserved alongside ours.
	if _, ok := servers["ours"]; !ok {
		t.Fatalf("our server not added: %v", servers)
	}
	user, ok := servers["user-server"].(map[string]any)
	if !ok {
		t.Fatalf("foreign server clobbered: %v", servers)
	}
	timeout, ok := user["timeout"].(json.Number)
	if !ok {
		t.Fatalf("timeout not decoded as json.Number (plain-unmarshal rounding?): %T %v", user["timeout"], user["timeout"])
	}
	if timeout.String() != bigInt {
		t.Fatalf("large foreign int rounded: got %s, want %s", timeout.String(), bigInt)
	}
}
