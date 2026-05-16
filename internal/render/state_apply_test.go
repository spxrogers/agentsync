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
	err := render.RecordOpsState(s, "claude", adapter.ScopeUser, "", []adapter.FileOp{{
		Action:        "write",
		Path:          p,
		MergeStrategy: "merge-json-keys",
		Content:       []byte(`{"mcpServers":{"github":{"command":"npx"}}}`),
		SourceID:      "mcp/github.toml",
	}})
	if err != nil {
		t.Fatal(err)
	}

	// Expect a key entry for /mcpServers/github
	var found bool
	for k := range s.Keys {
		if k == "claude:user::"+p+":/mcpServers/github" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing key entry; have: %+v", s.Keys)
	}
	_ = json.RawMessage{}
}

func TestRecordState_FileReplace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "CLAUDE.md")
	content := []byte("# CLAUDE\nHello world\n")
	_ = os.WriteFile(p, content, 0o644)

	s := state.New()
	err := render.RecordOpsState(s, "claude", adapter.ScopeUser, "", []adapter.FileOp{{
		Action:   "write",
		Path:     p,
		Content:  content,
		Mode:     0o644,
		SourceID: "memory/global.md",
	}})
	if err != nil {
		t.Fatal(err)
	}

	key := "claude:user::" + p
	fe, ok := s.Files[key]
	if !ok {
		t.Fatalf("missing file entry; have: %+v", s.Files)
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
	keep := "claude:user::/home/me/.claude/agents/keep.md"
	drop := "claude:user::/home/me/.claude/agents/dropme.md"
	otherAgent := "opencode:user::/home/me/.config/opencode/agent/keep.md"
	s.Files[keep] = state.FileEntry{SHA256: "a"}
	s.Files[drop] = state.FileEntry{SHA256: "b"}
	s.Files[otherAgent] = state.FileEntry{SHA256: "c"}

	render.PruneStaleState(s, "claude", adapter.ScopeUser, "", []adapter.FileOp{
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
	clauJSON := "/home/me/.claude.json"
	keepKey := "claude:user::" + clauJSON + ":/mcpServers/keep"
	dropKey := "claude:user::" + clauJSON + ":/mcpServers/dropme"
	s.Keys[keepKey] = state.KeyEntry{SHA256: "a"}
	s.Keys[dropKey] = state.KeyEntry{SHA256: "b"}

	render.PruneStaleState(s, "claude", adapter.ScopeUser, "", []adapter.FileOp{{
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

func TestRecordState_SkipsDeleteOps(t *testing.T) {
	s := state.New()
	err := render.RecordOpsState(s, "claude", adapter.ScopeUser, "", []adapter.FileOp{{
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
