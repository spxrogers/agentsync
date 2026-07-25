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

	c := projOf(source.Canonical{MCPServers: []source.MCPServer{{ID: "srv", Server: spec}}})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	// Precondition (round-1 test-rigor finding): the fixture file already
	// contains the exact digits, so if render produced no op for it the byte
	// assertion below would pass vacuously against the never-rewritten fixture.
	found := false
	for _, op := range ops {
		if op.Path == mcpPath {
			found = true
		}
	}
	if !found {
		t.Fatalf("render produced no op for %s — on-disk assertion would be vacuous; ops=%+v", mcpPath, ops)
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
