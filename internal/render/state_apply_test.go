package render_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	_ "github.com/spxrogers/agentsync/internal/adapter/noop"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/testenv"
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
	// Machine A wrote state under its $HOME. We drive the REAL
	// RecordOpsState path (not a hand-faked key) so this test would have
	// caught the bug where the normalization base was the agentsync home
	// rather than the user's $HOME and the key stayed machine-absolute.
	macHome := t.TempDir() // stand-in for /Users/alice
	macPath := filepath.Join(macHome, ".claude.json")
	if err := os.WriteFile(macPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := render.RecordOpsState(s, macHome, "claude", adapter.ScopeUser, "", []adapter.FileOp{
		{Action: "write", Path: macPath},
	}); err != nil {
		t.Fatal(err)
	}

	gotKey := "claude:user::${HOME}/.claude.json"
	if _, ok := s.Files[gotKey]; !ok {
		t.Fatalf("RecordOpsState did not produce a portable key %q; have %v", gotKey, s.Files)
	}
	// The machine-absolute path must NOT appear anywhere in the keys.
	for k := range s.Files {
		if strings.Contains(k, macHome) {
			t.Fatalf("state key embeds machine-absolute path %q: %q", macHome, k)
		}
	}

	// Machine B reads the same state under a DIFFERENT $HOME. PruneStaleState
	// with the same logical op must recognise the entry as still-present.
	linuxHome := t.TempDir() // stand-in for /home/alice
	linuxPath := filepath.Join(linuxHome, ".claude.json")
	render.PruneStaleState(s, linuxHome, "claude", adapter.ScopeUser, "", []adapter.FileOp{
		{Action: "write", Path: linuxPath},
	})
	if _, ok := s.Files[gotKey]; !ok {
		t.Fatalf("portable key pruned on machine B; have %v", s.Files)
	}
}

// TestPruneStaleState_AmbiguousPathPrefixKeepsLiveKey is the regression for
// the prune bug where the Keys loop broke after the FIRST path whose
// "path:" prefixed the key, even when that path's pointer set didn't match.
// When one dest path is a colon-delimited string-prefix of another (e.g. a
// Windows "C:"-drive path, or any path containing ':'), the wrong candidate
// could be picked first (map order is random) and a live key pruned —
// dropping ownership and forcing a needless foreign-collision backup next
// apply. Looped to defeat map-iteration randomness: the old code failed
// ~half the time, the fix passes every time.
func TestPruneStaleState_AmbiguousPathPrefixKeepsLiveKey(t *testing.T) {
	ops := []adapter.FileOp{
		{Action: "write", Path: "a", MergeStrategy: "merge-json-keys", Content: []byte(`{"x":1}`)},
		{Action: "write", Path: "a:b", MergeStrategy: "merge-json-keys", Content: []byte(`{"realptr":1}`)},
	}
	const liveKey = "claude:user::a:b:/realptr"
	for i := 0; i < 64; i++ {
		s := state.New()
		s.Keys[liveKey] = state.KeyEntry{SHA256: "deadbeef"}
		s.Keys["claude:user::a:/x"] = state.KeyEntry{SHA256: "feed"}
		// userHome "" so HomeRelative leaves the colon-bearing paths intact.
		render.PruneStaleState(s, "", "claude", adapter.ScopeUser, "", ops)
		if _, ok := s.Keys[liveKey]; !ok {
			t.Fatalf("iteration %d: live key %q wrongly pruned (ambiguous path prefix)", i, liveKey)
		}
	}
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

// TestPruneStaleState_ReclaimableOrphanRetention pins both directions of the
// keep-guard added for the skipped-delete retry.
//
// KEEP when the destination survives: apply skips (rather than performs) an
// orphan delete it cannot read, and pruning the entry would make that skip
// permanent and silent — orphanDeletes could never synthesize the delete again
// and the warning would never repeat.
//
// PRUNE when it is provably gone: otherwise every successfully-reclaimed file
// would leak an entry forever and targets.json would grow without bound.
func TestPruneStaleState_ReclaimableOrphanRetention(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()

	present := filepath.Join(tmp, ".claude", "agents", "still-here.md")
	if err := os.MkdirAll(filepath.Dir(present), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(tmp, ".claude", "agents", "reclaimed.md")
	// A reclaimable SourceID whose file is gone, plus one that is NOT
	// reclaimable — the exemption must not widen to every component kind.
	notReclaimable := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.WriteFile(notReclaimable, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	prefix := fmt.Sprintf("claude:user:%s:", paths.HomeRelative(tmp, ""))
	st := state.New()
	for path, srcID := range map[string]string{
		present:        "subagents/still-here.md",
		gone:           "subagents/reclaimed.md",
		notReclaimable: "memory/AGENTS.md",
	} {
		st.Files[prefix+paths.HomeRelative(tmp, path)] = state.FileEntry{SHA256: "old", SourceID: srcID}
	}

	// No ops at all: every entry is "no longer rendered".
	render.PruneStaleState(st, tmp, "claude", adapter.ScopeUser, "", nil)

	if _, ok := st.Files[prefix+paths.HomeRelative(tmp, present)]; !ok {
		t.Error("a reclaimable destination that is still on disk must keep its entry, " +
			"so a skipped delete is retried rather than forgotten")
	}
	if _, ok := st.Files[prefix+paths.HomeRelative(tmp, gone)]; ok {
		t.Error("a reclaimable destination that is gone must still be pruned")
	}
	if _, ok := st.Files[prefix+paths.HomeRelative(tmp, notReclaimable)]; ok {
		t.Error("the exemption must apply only to reclaimable SourceIDs; " +
			"a non-reclaimable entry must prune as before")
	}
}
