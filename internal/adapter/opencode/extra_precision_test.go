package opencode_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestIngest_PreservesLargeIntegerExtra is artifact-anchored on a real
// ~/.config/opencode/opencode.json carrying an unmodeled integer beyond
// float64's 2^53 (2^53+1). The UseNumber decode (jsonkeys.DecodeObject over
// hujson's comment-stripped output) must keep the exact digits in the
// passthrough Extra map — a plain json.Unmarshal rounds it to 9007199254740992
// — and the render-back must land the literal digits on disk.
func TestIngest_PreservesLargeIntegerExtra(t *testing.T) {
	tmp := t.TempDir()
	settingsPath := filepath.Join(tmp, ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath,
		[]byte(`{"mcp":{"api":{"type":"local","command":["node","s.js"],"sessionBudget":9007199254740993}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	a := opencode.New(opencode.Options{TargetRoot: tmp})
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
	// Precondition (round-1 test-rigor finding): the fixture file already
	// contains the exact digits, so if render produced no op for it the byte
	// assertion below would pass vacuously against the never-rewritten fixture.
	found := false
	for _, op := range ops {
		if op.Path == settingsPath {
			found = true
		}
	}
	if !found {
		t.Fatalf("render produced no op for %s — on-disk assertion would be vacuous; ops=%+v", settingsPath, ops)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("9007199254740993")) || bytes.Contains(data, []byte("9007199254740992")) {
		t.Fatalf("re-rendered opencode.json lost integer precision: %s", data)
	}
}
