package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// fakeAdapter implements adapter.Adapter just enough for the guard's tests.
// It applies replace and merge-json-keys ops via os.WriteFile so we can
// observe what landed on disk.
type fakeAdapter struct {
	name string
}

func (f *fakeAdapter) Name() string                  { return f.name }
func (f *fakeAdapter) Capabilities() adapter.Capability { return 0 }
func (f *fakeAdapter) Detect() (bool, error)         { return true, nil }

func (f *fakeAdapter) Render(_ source.Canonical, _ adapter.Scope, _ string) ([]adapter.FileOp, []adapter.Skip, error) {
	return nil, nil, nil
}

func (f *fakeAdapter) Ingest(_ adapter.Scope, _ string) (source.Canonical, error) {
	return source.Canonical{}, nil
}

func (f *fakeAdapter) Apply(ops []adapter.FileOp) error {
	for _, op := range ops {
		if op.Action != "" && op.Action != "write" {
			continue
		}
		if op.MergeStrategy == "merge-json-keys" {
			// Simple union: existing wins on disjoint keys, ours wins on
			// conflicts (the production claude/opencode adapters use the
			// jsonkeys.MergeKeys helper; the test uses a simpler model).
			existing := map[string]any{}
			if data, err := os.ReadFile(op.Path); err == nil {
				_ = json.Unmarshal(data, &existing)
			}
			var ours map[string]any
			_ = json.Unmarshal(op.Content, &ours)
			for k, v := range ours {
				existing[k] = v
			}
			out, _ := json.MarshalIndent(existing, "", "  ")
			return os.WriteFile(op.Path, out, 0o644)
		}
		if err := os.WriteFile(op.Path, op.Content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// TestApplyWithCollisionGuard_FileLevel asserts that a pre-existing
// destination file with no state entry is backed up before being
// overwritten — the README's promise the original code never delivered.
func TestApplyWithCollisionGuard_FileLevel(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(tmp, ".claude", "agents", "reviewer.md")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("---\nname: reviewer\n---\nMy carefully hand-tuned reviewer prompt.\n")
	if err := os.WriteFile(dest, original, 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newAdapterRegistry(&fakeAdapter{name: "claude"})
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {Ops: []adapter.FileOp{{
				Action:   "write",
				Path:     dest,
				Content:  []byte("---\nname: reviewer\n---\nNew shiny rendered prompt.\n"),
				SourceID: "agents/reviewer.md",
			}}},
		},
	}
	st := state.New()

	reports, err := render.ApplyWithCollisionGuard(plan, reg, st, home, adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("want 1 collision; got %d (%v)", len(reports), reports)
	}
	r := reports[0]
	if r.Pointer != "" {
		t.Fatalf("expected file-level collision; got pointer %q", r.Pointer)
	}
	if r.BackupTo == "" {
		t.Fatal("BackupTo must be set")
	}
	// Original content must be at the backup path.
	got, err := os.ReadFile(r.BackupTo)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("backup mismatch:\nwant: %s\ngot:  %s", original, got)
	}
	// Backup must be under <home>/.state/backups/.
	if !strings.HasPrefix(r.BackupTo, filepath.Join(home, ".state", "backups")) {
		t.Fatalf("backup not under <home>/.state/backups: %s", r.BackupTo)
	}
}

// TestApplyWithCollisionGuard_KeyLevel asserts that a per-key conflict in
// a shared JSON file (e.g. the user's hand-edited ~/.claude.json with a
// custom mcpServers.github) triggers a backup of the whole file before
// the merge-json-keys op silently overwrites the conflicting key.
func TestApplyWithCollisionGuard_KeyLevel(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(tmp, ".claude.json")
	original := []byte(`{
  "mcpServers": {
    "github": {"command": "/usr/local/bin/my-fork", "args": ["--my-flag"]}
  },
  "preserveMe": "do not touch"
}`)
	if err := os.WriteFile(dest, original, 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newAdapterRegistry(&fakeAdapter{name: "claude"})
	ours := []byte(`{"mcpServers":{"github":{"command":"npx","args":["-y","@m/server-github"]}}}`)
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {Ops: []adapter.FileOp{{
				Action:        "write",
				Path:          dest,
				Content:       ours,
				MergeStrategy: "merge-json-keys",
				SourceID:      "mcp/github.toml",
			}}},
		},
	}
	st := state.New()

	reports, err := render.ApplyWithCollisionGuard(plan, reg, st, home, adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(reports) == 0 {
		t.Fatal("expected at least one key-level collision")
	}
	if reports[0].Pointer == "" {
		t.Fatalf("want key-level collision (Pointer set); got %+v", reports[0])
	}
	got, err := os.ReadFile(reports[0].BackupTo)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(string(got), "my-fork") {
		t.Fatalf("backup missing original command; got:\n%s", got)
	}
}

// TestApplyWithCollisionGuard_NoCollision_NoBackup asserts that when state
// already records the file (i.e. agentsync owns it), no backup is written.
func TestApplyWithCollisionGuard_NoCollision_NoBackup(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	_ = os.MkdirAll(home, 0o755)
	dest := filepath.Join(tmp, "x.md")
	_ = os.WriteFile(dest, []byte("ours"), 0o644)
	st := state.New()
	st.Files["claude:user::"+dest] = state.FileEntry{SHA256: "anything"}

	reg := newAdapterRegistry(&fakeAdapter{name: "claude"})
	plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{
		"claude": {Ops: []adapter.FileOp{{Action: "write", Path: dest, Content: []byte("new")}}},
	}}
	reports, err := render.ApplyWithCollisionGuard(plan, reg, st, home, adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 0 {
		t.Fatalf("expected no collisions when file is already owned; got %v", reports)
	}
	// And the backup tree should not exist.
	if _, statErr := os.Stat(filepath.Join(home, ".state", "backups")); statErr == nil {
		t.Fatal("backups dir should not have been created")
	}
}

// newAdapterRegistry builds an adapter.Registry with the supplied adapters.
// The Registry constructor is in the adapter package; we rebuild it here to
// avoid a hard test dependency on its public API.
func newAdapterRegistry(as ...adapter.Adapter) *adapter.Registry {
	r := adapter.NewRegistry()
	for _, a := range as {
		_ = r.Register(a)
	}
	return r
}
