package generic_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/generic"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestGeneric_TransportKeylessSSEFlipDocumented is artifact-anchored and backs the
// capability-matrix breadth-tier transport note: for a TRANSPORT-KEYLESS dialect
// (no `type`/`transport` field — here an antigravity-shaped spec with a single
// `serverUrl` remote key), a canonical `sse` server has nowhere to record its
// transport, so it is written with just a url and canonicalizes back as `http` on
// capture. Render → apply (writes the mcp file) → ingest asserts the documented
// sse→http flip actually happens.
func TestGeneric_TransportKeylessSSEFlipDocumented(t *testing.T) {
	tmp := t.TempDir()
	// antigravity's dialect: no TransportKey, remote url under `serverUrl`.
	spec := generic.Spec{Name: "antigravity", MCP: generic.MCPTarget{User: ".gemini/config/mcp_config.json", RemoteURLKey: "serverUrl"}}
	a := generic.New(spec, generic.Options{TargetRoot: tmp})

	in := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "sse-srv", Server: source.MCPServerSpec{Type: "sse", URL: "https://x/sse", Headers: map[string]string{"A": "b"}}},
	}}
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}

	// On disk: a serverUrl, and NO transport field to record `sse`.
	data, err := os.ReadFile(filepath.Join(tmp, ".gemini", "config", "mcp_config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	srv := top["mcpServers"].(map[string]any)["sse-srv"].(map[string]any)
	if srv["serverUrl"] != "https://x/sse" {
		t.Fatalf("remote url must be under serverUrl: %v", srv)
	}
	for _, k := range []string{"type", "transport"} {
		if _, has := srv[k]; has {
			t.Fatalf("keyless dialect must not write a transport field %q: %v", k, srv)
		}
	}

	// Capture: the sse transport is lost — it comes back as http (the documented flip).
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServers) != 1 {
		t.Fatalf("want 1 ingested server, got %d", len(got.MCPServers))
	}
	if s := got.MCPServers[0].Server; s.Type != "http" || s.URL != "https://x/sse" {
		t.Fatalf("sse must collapse to http on capture: got %+v", s)
	}
}
