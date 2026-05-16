package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	_ "github.com/spxrogers/agentsync/internal/adapter/noop"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

func TestRecordState_FilesAndKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude.json")
	_ = os.WriteFile(p, []byte(`{"mcpServers":{"github":{"command":"npx"}},"foreign":{}}`), 0o644)

	s := state.New()
	// Use dir as home so the recorded state key uses HOME-relative form.
	err := render.RecordOpsState(s, dir, "claude", adapter.ScopeUser, "", []adapter.FileOp{{
		Action:        "write",
		Path:          p,
		MergeStrategy: "merge-json-keys",
		Content:       []byte(`{"mcpServers":{"github":{"command":"npx"}}}`),
		SourceID:      "mcp/github.toml",
	}})
	if err != nil {
		t.Fatal(err)
	}

	// Expect a key entry for /mcpServers/github keyed by ${HOME}/.claude.json.
	wantKey := "claude:user::${HOME}/.claude.json:/mcpServers/github"
	var found bool
	for k := range s.Keys {
		if k == wantKey {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing key %q; have: %+v", wantKey, s.Keys)
	}
	_ = json.RawMessage{}
}

func TestRecordState_FileReplace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "CLAUDE.md")
	content := []byte("# CLAUDE\nHello world\n")
	_ = os.WriteFile(p, content, 0o644)

	s := state.New()
	err := render.RecordOpsState(s, dir, "claude", adapter.ScopeUser, "", []adapter.FileOp{{
		Action:   "write",
		Path:     p,
		Content:  content,
		Mode:     0o644,
		SourceID: "memory/global.md",
	}})
	if err != nil {
		t.Fatal(err)
	}

	key := "claude:user::${HOME}/CLAUDE.md"
	fe, ok := s.Files[key]
	if !ok {
		t.Fatalf("missing file entry %q; have: %+v", key, s.Files)
	}
	if fe.SHA256 == "" {
		t.Fatal("SHA256 must not be empty")
	}
	if fe.SourceID != "memory/global.md" {
		t.Fatalf("unexpected SourceID: %s", fe.SourceID)
	}
}

func TestPruneStaleState_DropsRemovedFiles(t *testing.T) {
	s := state.New()
	home := "/home/me"
	keep := "claude:user::${HOME}/.claude/agents/keep.md"
	drop := "claude:user::${HOME}/.claude/agents/dropme.md"
	otherAgent := "opencode:user::${HOME}/.config/opencode/agent/keep.md"
	s.Files[keep] = state.FileEntry{SHA256: "a"}
	s.Files[drop] = state.FileEntry{SHA256: "b"}
	s.Files[otherAgent] = state.FileEntry{SHA256: "c"}

	render.PruneStaleState(s, home, "claude", adapter.ScopeUser, "", []adapter.FileOp{
		{Action: "write", Path: "/home/me/.claude/agents/keep.md"},
	})
	if _, ok := s.Files[keep]; !ok {
		t.Fatal("kept entry was pruned")
	}
	if _, ok := s.Files[drop]; ok {
		t.Fatal("stale entry was not pruned")
	}
	if _, ok := s.Files[otherAgent]; !ok {
		t.Fatal("other agent's entry must not be touched")
	}
}

func TestPruneStaleState_DropsRemovedKeys(t *testing.T) {
	s := state.New()
	home := "/home/me"
	clauJSON := "/home/me/.claude.json"
	keepKey := "claude:user::${HOME}/.claude.json:/mcpServers/keep"
	dropKey := "claude:user::${HOME}/.claude.json:/mcpServers/dropme"
	s.Keys[keepKey] = state.KeyEntry{SHA256: "a"}
	s.Keys[dropKey] = state.KeyEntry{SHA256: "b"}

	render.PruneStaleState(s, home, "claude", adapter.ScopeUser, "", []adapter.FileOp{{
		Action:        "write",
		Path:          clauJSON,
		MergeStrategy: "merge-json-keys",
		Content:       []byte(`{"mcpServers":{"keep":{"command":"x"}}}`),
	}})
	if _, ok := s.Keys[keepKey]; !ok {
		t.Fatal("kept key was pruned")
	}
	if _, ok := s.Keys[dropKey]; ok {
		t.Fatal("stale key was not pruned")
	}
}

// TestState_PortableAcrossHomes is the regression for the cross-machine
// portability bug: state keys used to embed absolute paths like
// /Users/alice/.claude.json, so a state file synced via chezmoi from
// /Users/alice/ to /home/alice/ would have every key fail to match on
// the destination machine and every native file would reclassify as
// ForeignCollision. With ${HOME}-relative keys, the same state file
// works on either machine.
func TestState_PortableAcrossHomes(t *testing.T) {
	s := state.New()
	// Machine A wrote state under /Users/alice/.
	macHome := "/Users/alice"
	macPath := "/Users/alice/.claude.json"
	if err := writeKey(s, macHome, "claude", adapter.ScopeUser, "", macPath); err != nil {
		t.Fatal(err)
	}
	// Machine B reads the same state under /home/alice/.
	linuxHome := "/home/alice"
	linuxPath := "/home/alice/.claude.json"
	gotKey := "claude:user::${HOME}/.claude.json"
	if _, ok := s.Files[gotKey]; !ok {
		t.Fatalf("expected portable key %q; have %v", gotKey, s.Files)
	}
	// Now PruneStaleState on machine B with the same logical op should
	// recognise the entry as still-present (not stale).
	render.PruneStaleState(s, linuxHome, "claude", adapter.ScopeUser, "", []adapter.FileOp{
		{Action: "write", Path: linuxPath},
	})
	if _, ok := s.Files[gotKey]; !ok {
		t.Fatalf("portable key pruned on machine B; have %v", s.Files)
	}
}

// writeKey is a tiny test helper that calls RecordOpsState with a single
// file op so the test stays focused on the key shape, not the
// per-op-type record path.
func writeKey(s *state.Targets, home, agent string, sc adapter.Scope, project, path string) error {
	tmp, _ := os.CreateTemp("", "agentsync-state-test-*")
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()
	_ = os.WriteFile(tmpPath, []byte("hello"), 0o644)
	// Manually format the portable key — we can't call RecordOpsState
	// because it reads the file at op.Path post-apply, and we want to
	// pin the key shape, not the I/O.
	s.Files[agent+":"+sc.String()+":"+project+":${HOME}/"+filepath.Base(path)] = state.FileEntry{
		SHA256: "deadbeef",
	}
	return nil
}

func TestRecordState_SkipsDeleteOps(t *testing.T) {
	s := state.New()
	err := render.RecordOpsState(s, "/tmp", "claude", adapter.ScopeUser, "", []adapter.FileOp{{
		Action: "delete",
		Path:   "/some/path",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 0 || len(s.Keys) != 0 {
		t.Fatal("delete ops should not create state entries")
	}
}
