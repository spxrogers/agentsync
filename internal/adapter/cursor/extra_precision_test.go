package cursor_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/cursor"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestIngest_PreservesLargeIntegerExtra is artifact-anchored on a real
// ~/.cursor/mcp.json carrying an unmodeled integer beyond float64's 2^53
// (2^53+1). The UseNumber decode (jsonkeys.DecodeObject) must keep the exact
// digits in the passthrough Extra map — a plain json.Unmarshal rounds it to
// 9007199254740992 — and the render-back must land the literal digits on disk.
func TestIngest_PreservesLargeIntegerExtra(t *testing.T) {
	tmp := t.TempDir()
	mcpPath := filepath.Join(tmp, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath,
		[]byte(`{"mcpServers":{"api":{"command":"node","sessionBudget":9007199254740993}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	a := cursor.New(cursor.Options{TargetRoot: tmp})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(got.MCPServers) != 1 {
		t.Fatalf("mcp = %d", len(got.MCPServers))
	}
	if v := got.MCPServers[0].Server.Extra["sessionBudget"]; v != json.Number("9007199254740993") {
		t.Fatalf("large integer not preserved into Extra as json.Number: %#v", v)
	}

	ops, _, err := a.Render(secrets.ForRender(source.Canonical{MCPServers: got.MCPServers}), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("9007199254740993")) || bytes.Contains(data, []byte("9007199254740992")) {
		t.Fatalf("re-rendered mcp.json lost integer precision: %s", data)
	}
}
