package noop_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/noop"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestNoop_Implements(t *testing.T) {
	var _ adapter.Adapter = noop.New("test")
}

func TestNoop_Render(t *testing.T) {
	a := noop.New("test")
	ops, skips, err := a.Render(secrets.ForRender(source.Canonical{}), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 || len(skips) != 0 {
		t.Fatalf("noop render returned %d ops, %d skips", len(ops), len(skips))
	}
}

func TestNoop_Name(t *testing.T) {
	if got := noop.New("codex").Name(); got != "codex" {
		t.Fatalf("Name() = %q, want codex", got)
	}
	// New clamps an empty name to a usable default (the registry keys by name).
	if got := noop.New("").Name(); got != "noop" {
		t.Fatalf("New(\"\").Name() = %q, want noop", got)
	}
}

func TestNoop_Ingest(t *testing.T) {
	got, err := noop.New("test").Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(got.MCPServers) != 0 || len(got.Skills) != 0 || len(got.Commands) != 0 ||
		len(got.Subagents) != 0 || len(got.Hooks) != 0 || len(got.LSPServers) != 0 ||
		got.Memory.Body != "" {
		t.Fatalf("noop Ingest must return an empty canonical, got %+v", got)
	}
}

func TestNoop_KeyMergeStrategy(t *testing.T) {
	if got := noop.New("test").KeyMergeStrategy(); got != "" {
		t.Fatalf("KeyMergeStrategy() = %q, want \"\" (noop merges no keys)", got)
	}
}

// TestNoop_Capabilities pins noop's zero capability surface. There is no
// Capabilities() bitmask in the adapter interface, so the observable equivalent is
// asserted: noop advertises no key-merge and, handed a canonical with EVERY
// component, projects nothing (no ops) and reports nothing (no skips).
func TestNoop_Capabilities(t *testing.T) {
	a := noop.New("test")
	if a.KeyMergeStrategy() != "" {
		t.Fatalf("noop must advertise no key-merge, got %q", a.KeyMergeStrategy())
	}
	full := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "s", Server: source.MCPServerSpec{Command: "x"}}},
		Skills:     []source.Skill{{Name: "sk", Body: "x\n"}},
		Commands:   []source.Command{{Name: "c", Body: "y\n"}},
		Subagents:  []source.Subagent{{Name: "a", Body: "z\n"}},
		Hooks:      []source.Hook{{Event: "PreToolUse", Type: "command", Command: "echo"}},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
		Memory:     source.Memory{Body: "rules\n"},
	}
	ops, skips, err := a.Render(secrets.ForRender(full), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 || len(skips) != 0 {
		t.Fatalf("noop has no capability surface: got %d ops, %d skips", len(ops), len(skips))
	}
}

// TestNoop_Detect pins the documented test-only sentinel behavior (always present).
func TestNoop_Detect(t *testing.T) {
	ok, err := noop.New("test").Detect()
	if err != nil || !ok {
		t.Fatalf("Detect() = (%v, %v), want (true, nil)", ok, err)
	}
}

// spyWriter records DestWriter calls so a test can assert none happened.
type spyWriter struct {
	writes, deletes int
}

func (s *spyWriter) Write(adapter.FileOp, []byte) error { s.writes++; return nil }
func (s *spyWriter) Delete(adapter.FileOp) error        { s.deletes++; return nil }

// TestNoop_ApplyIgnoresOps is the load-bearing assertion: Apply is a true no-op —
// handed a non-empty []FileOp (a write AND a delete), it must NOT touch the
// DestWriter at all.
func TestNoop_ApplyIgnoresOps(t *testing.T) {
	spy := &spyWriter{}
	ops := []adapter.FileOp{
		{Action: "write", Path: "/tmp/should-not-write", Content: []byte("nope"), Mode: 0o644},
		{Action: "delete", Path: "/tmp/should-not-delete"},
	}
	if err := noop.New("test").Apply(ops, spy); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if spy.writes != 0 || spy.deletes != 0 {
		t.Fatalf("noop Apply must ignore its ops; got %d writes, %d deletes", spy.writes, spy.deletes)
	}
}
