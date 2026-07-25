package roo_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter/roo"
)

// TestIngest_PreservesLargeIntegerExtra is artifact-anchored on a real
// <project>/.roo/mcp.json carrying an unmodeled integer beyond float64's 2^53
// (2^53+1). The UseNumber decode (jsonkeys.DecodeObject) must keep the exact
// digits in the passthrough Extra map — a plain json.Unmarshal rounds it to
// 9007199254740992 — and the render-back must land the literal digits on disk.
func TestIngest_PreservesLargeIntegerExtra(t *testing.T) {
	proj := t.TempDir()
	mcpPath := filepath.Join(proj, ".roo", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath,
		[]byte(`{"mcpServers":{"srv":{"command":"node","sessionBudget":9007199254740993}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	a := roo.New(roo.Options{TargetRoot: t.TempDir()})
	spec := ingestOneRooServer(t, a, proj)
	if v := spec.Extra["sessionBudget"]; v != json.Number("9007199254740993") {
		t.Fatalf("large integer not preserved into Extra as json.Number: %#v", v)
	}

	data := renderApplyRooMCP(t, a, proj, spec)
	if !bytes.Contains(data, []byte("9007199254740993")) || bytes.Contains(data, []byte("9007199254740992")) {
		t.Fatalf("re-rendered mcp.json lost integer precision: %s", data)
	}
}
