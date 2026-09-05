package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	_ "github.com/spxrogers/agentsync/internal/adapter/noop"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/source"
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
		Action:        adapter.ActionWrite,
		Path:          p,
		MergeStrategy: "merge-json-keys",
		Content:       []byte(`{"mcpServers":{"github":{"command":"npx"}}}`),
		SourceID:      "mcp/github.toml",
	}})
	if err != nil {
		t.Fatal(err)
	}

	// Expect a key entry for /mcpServers/github keyed by ${HOME}/.claude.json.
	wantKey := state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude.json", Pointer: "/mcpServers/github"}
	if _, found := s.Keys[wantKey]; !found {
		t.Fatalf("missing key %+v; have: %+v", wantKey, s.Keys)
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
		Action:   adapter.ActionWrite,
		Path:     p,
		Content:  content,
		Mode:     0o644,
		SourceID: "memory/global.md",
	}})
	if err != nil {
		t.Fatal(err)
	}

	key := state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/CLAUDE.md"}
	fe, ok := s.Files[key]
	if !ok {
		t.Fatalf("missing file entry %+v; have: %+v", key, s.Files)
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
	keep := state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude/agents/keep.md"}
	drop := state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude/agents/dropme.md"}
	otherAgent := state.Key{Agent: "opencode", Scope: "user", Path: "${HOME}/.config/opencode/agent/keep.md"}
	s.Files[keep] = state.FileEntry{SHA256: "a"}
	s.Files[drop] = state.FileEntry{SHA256: "b"}
	s.Files[otherAgent] = state.FileEntry{SHA256: "c"}

	render.PruneStaleState(s, home, "claude", adapter.ScopeUser, "", []adapter.FileOp{
		{Action: adapter.ActionWrite, Path: "/home/me/.claude/agents/keep.md"},
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
	keepKey := state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude.json", Pointer: "/mcpServers/keep"}
	dropKey := state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude.json", Pointer: "/mcpServers/dropme"}
	s.Keys[keepKey] = state.KeyEntry{SHA256: "a"}
	s.Keys[dropKey] = state.KeyEntry{SHA256: "b"}

	render.PruneStaleState(s, home, "claude", adapter.ScopeUser, "", []adapter.FileOp{{
		Action:        adapter.ActionWrite,
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
		{Action: adapter.ActionWrite, Path: macPath},
	}); err != nil {
		t.Fatal(err)
	}

	gotKey := state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude.json"}
	if _, ok := s.Files[gotKey]; !ok {
		t.Fatalf("RecordOpsState did not produce a portable key %+v; have %v", gotKey, s.Files)
	}
	// The machine-absolute path must NOT appear anywhere in the keys.
	for k := range s.Files {
		if strings.Contains(k.String(), macHome) {
			t.Fatalf("state key embeds machine-absolute path %q: %q", macHome, k.String())
		}
	}

	// Machine B reads the same state under a DIFFERENT $HOME. PruneStaleState
	// with the same logical op must recognise the entry as still-present.
	linuxHome := t.TempDir() // stand-in for /home/alice
	linuxPath := filepath.Join(linuxHome, ".claude.json")
	render.PruneStaleState(s, linuxHome, "claude", adapter.ScopeUser, "", []adapter.FileOp{
		{Action: adapter.ActionWrite, Path: linuxPath},
	})
	if _, ok := s.Files[gotKey]; !ok {
		t.Fatalf("portable key pruned on machine B; have %v", s.Files)
	}
}

// TestPruneStaleState_AmbiguousPathPrefixKeepsLiveKey pins that a dest path
// which is a colon-delimited string-prefix of another ("a" vs "a:b") never
// costs the longer path its ownership. Since the state key became typed
// (issue #227) this holds BY CONSTRUCTION — Path is its own field and is
// compared for equality, never as a string prefix — but the case stays as a
// regression against anyone reintroducing string matching.
func TestPruneStaleState_AmbiguousPathPrefixKeepsLiveKey(t *testing.T) {
	ops := []adapter.FileOp{
		{Action: adapter.ActionWrite, Path: "a", MergeStrategy: "merge-json-keys", Content: []byte(`{"x":1}`)},
		{Action: adapter.ActionWrite, Path: "a:b", MergeStrategy: "merge-json-keys", Content: []byte(`{"realptr":1}`)},
	}
	liveKey := state.Key{Agent: "claude", Scope: "user", Path: "a:b", Pointer: "/realptr"}
	for i := 0; i < 64; i++ {
		s := state.New()
		s.Keys[liveKey] = state.KeyEntry{SHA256: "deadbeef"}
		s.Keys[state.Key{Agent: "claude", Scope: "user", Path: "a", Pointer: "/x"}] = state.KeyEntry{SHA256: "feed"}
		// userHome "" so HomeRelative leaves the colon-bearing paths intact.
		render.PruneStaleState(s, "", "claude", adapter.ScopeUser, "", ops)
		if _, ok := s.Keys[liveKey]; !ok {
			t.Fatalf("iteration %d: live key %+v wrongly pruned (ambiguous path prefix)", i, liveKey)
		}
	}
}

func TestRecordState_SkipsDeleteOps(t *testing.T) {
	s := state.New()
	err := render.RecordOpsState(s, "/tmp", "claude", adapter.ScopeUser, "", []adapter.FileOp{{
		Action: adapter.ActionDelete,
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

	st := state.New()
	for path, srcID := range map[string]string{
		present:        "subagents/still-here.md",
		gone:           "subagents/reclaimed.md",
		notReclaimable: "memory/AGENTS.md",
	} {
		st.Files[state.NewFileKey(tmp, "claude", "user", "", path)] = state.FileEntry{SHA256: "old", SourceID: srcID}
	}

	// No ops at all: every entry is "no longer rendered".
	render.PruneStaleState(st, tmp, "claude", adapter.ScopeUser, "", nil)

	if _, ok := st.Files[state.NewFileKey(tmp, "claude", "user", "", present)]; !ok {
		t.Error("a reclaimable destination that is still on disk must keep its entry, " +
			"so a skipped delete is retried rather than forgotten")
	}
	if _, ok := st.Files[state.NewFileKey(tmp, "claude", "user", "", gone)]; ok {
		t.Error("a reclaimable destination that is gone must still be pruned")
	}
	if _, ok := st.Files[state.NewFileKey(tmp, "claude", "user", "", notReclaimable)]; ok {
		t.Error("the exemption must apply only to reclaimable SourceIDs; " +
			"a non-reclaimable entry must prune as before")
	}
}

// TestPruneStaleState_DanglingSymlinkIsKept is the discriminating case for the
// guard's PREDICATE, which the test above cannot reach: a present file and an
// absent one are kept/pruned identically under Stat and Lstat, so neither
// separates the correct check from the two wrong ones review found.
//
// A dangling symlink does. os.Stat follows it and reports ENOENT — so both
// `Stat() == nil` (the round-3 bug) and `!errors.Is(Stat_err, ErrNotExist)` (the
// round-4 near-miss) prune the entry and strand the link forever. os.Lstat sees
// the link itself, so the entry is kept, the delete is retried, and Delete's
// os.Remove clears it.
func TestPruneStaleState_DanglingSymlinkIsKept(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "dangling.md")
	if err := os.Symlink(filepath.Join(tmp, "nonexistent-target"), link); err != nil {
		t.Fatal(err)
	}

	st := state.New()
	key := state.NewFileKey(tmp, "claude", "user", "", link)
	st.Files[key] = state.FileEntry{SHA256: "old", SourceID: "subagents/dangling.md"}

	render.PruneStaleState(st, tmp, "claude", adapter.ScopeUser, "", nil)

	if _, ok := st.Files[key]; !ok {
		t.Error("a dangling symlink is still a destination agentsync owns and can remove; " +
			"pruning its entry strands it forever (the guard must Lstat, not Stat)")
	}
}

// TestPruneStaleState_RetiredSubagentSourceIDIsReclaimable pins the prefix that
// makes the #211 upgrade actually work for the users it targets.
//
// The canonical directory rename (agents/ → subagents/) ships in the same release
// as namespacing, so an upgrading user's state still holds "agents/<name>.md"
// SourceIDs — and the only rewriter runs from `migrate subagents`, which returns
// early when <home>/agents/ is empty. A user whose subagents come ONLY from
// plugins has no such directory, which is exactly the reported scenario. Without
// the retired prefix their pre-rename destination file is unreclaimable AND loses
// its state entry: left forever beside the namespaced ones, which is MORE
// duplicate agents than before and the opposite of what the upgrade notice says.
func TestPruneStaleState_RetiredSubagentSourceIDIsReclaimable(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "code-reviewer.md")
	if err := os.WriteFile(legacy, []byte("pre-rename"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := state.New()
	key := state.NewFileKey(tmp, "claude", "user", "", legacy)
	// The RETIRED spelling, as a pre-upgrade state file carries it.
	st.Files[key] = state.FileEntry{SHA256: "old", SourceID: "agents/code-reviewer.md"}

	render.PruneStaleState(st, tmp, "claude", adapter.ScopeUser, "", nil)
	if _, ok := st.Files[key]; !ok {
		t.Fatal("a pre-rename subagent entry must stay owned so its destination is reclaimed")
	}

	deletes := render.OrphanDeletes(st, tmp, "claude", adapter.ScopeUser, "", nil)
	var found bool
	for _, d := range deletes {
		if d.Path == legacy {
			found = true
		}
	}
	if !found {
		t.Errorf("apply must synthesize a delete for the pre-rename destination; got %v", deletes)
	}
}

// TestOrphanReclaimedPrefixes_RetiredAliasDoesNotOverMatch guards the one hazard
// of carrying a retired identifier in a live prefix list: if "agents/" ever
// becomes a LIVE SourceID prefix again, the alias silently starts matching it.
//
// It is asserted against the real producer — source.SubagentSourceID — rather
// than a literal, so it tracks the constant instead of a copy of it.
func TestOrphanReclaimedPrefixes_RetiredAliasDoesNotOverMatch(t *testing.T) {
	live := source.SubagentSourceID("reviewer")
	if strings.HasPrefix(filepath.ToSlash(live), source.LegacySubagentsDir+"/") {
		t.Fatalf("the live subagent SourceID (%q) now starts with the RETIRED prefix %q — "+
			"the alias in orphanReclaimedPrefixes would over-match every live subagent; "+
			"drop it or re-scope it", live, source.LegacySubagentsDir+"/")
	}
	// And the retired spelling is still recognised, which is the point of it.
	if !render.OrphanIsReclaimable(source.LegacySubagentsDir + "/reviewer.md") {
		t.Error("the retired agents/ spelling must stay reclaimable until it is out of circulation")
	}
	if !render.OrphanIsReclaimable(live) {
		t.Errorf("the live spelling %q must be reclaimable", live)
	}
	if render.OrphanIsReclaimable("memory/AGENTS.md") {
		t.Error("a non-component SourceID must not be reclaimable")
	}
}

// TestPruneStaleState_SiblingColonProjectRootSurvives is the direct regression
// for issue #227. Two project roots differ only by a ':'-suffixed variant:
//
//	${HOME}/work/app          and  ${HOME}/work/app:staging
//
// Under the v1 string key the second project's entry began with the first's
// prefix ("claude:project:${HOME}/work/app:"), so an apply of the SHORTER root
// pruned the LONGER root's ownership — and the next apply of that project
// classified its .mcp.json as a foreign collision, backing it up and rewriting
// it. The typed key compares Project as a field, so the sibling is untouched.
func TestPruneStaleState_SiblingColonProjectRootSurvives(t *testing.T) {
	const userHome = "/home/alice"
	shortRoot := filepath.Join(userHome, "work", "app")
	longRoot := filepath.Join(userHome, "work", "app:staging")

	s := state.New()
	shortKey := state.NewFileKey(userHome, "claude", "project", shortRoot, filepath.Join(shortRoot, ".mcp.json"))
	longKey := state.NewFileKey(userHome, "claude", "project", longRoot, filepath.Join(longRoot, ".mcp.json"))
	s.Files[shortKey] = state.FileEntry{SHA256: "a"}
	s.Files[longKey] = state.FileEntry{SHA256: "b"}

	// Apply the SHORT project with no ops at all: everything it owns is stale.
	render.PruneStaleState(s, userHome, "claude", adapter.ScopeProject, shortRoot, nil)

	if _, ok := s.Files[shortKey]; ok {
		t.Fatal("the short project's own stale entry should have been pruned")
	}
	if _, ok := s.Files[longKey]; !ok {
		t.Fatalf("a SIBLING project whose root merely shares a ':'-delimited prefix "+
			"must keep its ownership; have %+v", s.Files)
	}
}
