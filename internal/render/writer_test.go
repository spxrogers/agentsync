package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// fakeJSONApply implements just enough of adapter.Adapter to test the
// Writer's collision behavior end-to-end. Apply does the same kind of
// merge the real claude adapter does (existing ∪ ours) so the writer
// sees both the pre-merge op.Content (for per-key conflict detection)
// and the post-merge final bytes (for the actual write).
type fakeJSONApply struct {
	name string
}

func (f *fakeJSONApply) Name() string                     { return f.name }
func (f *fakeJSONApply) Capabilities() adapter.Capability { return 0 }
func (f *fakeJSONApply) Detect() (bool, error)            { return true, nil }
func (f *fakeJSONApply) Render(_ secrets.Resolved, _ adapter.Scope, _ string) ([]adapter.FileOp, []adapter.Skip, error) {
	return nil, nil, nil
}

func (f *fakeJSONApply) Ingest(_ adapter.Scope, _ string) (source.Canonical, error) {
	return source.Canonical{}, nil
}

func (f *fakeJSONApply) KeyMergeStrategy() string { return "merge-json-keys" }

// Apply mirrors the production adapter pattern: for replace ops, hand
// op.Content to the writer; for merge ops, do the merge first and pass
// the result.
func (f *fakeJSONApply) Apply(ops []adapter.FileOp, w adapter.DestWriter) error {
	for _, op := range ops {
		switch op.Action {
		case "delete":
			if err := w.Delete(op); err != nil {
				return err
			}
		case "", "write":
			if op.MergeStrategy == "merge-json-keys" {
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
				if err := w.Write(op, append(out, '\n')); err != nil {
					return err
				}
			} else {
				if err := w.Write(op, op.Content); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// TestWriter_FileLevelBackup is the regression for the HIGH-severity
// finding: a pre-existing native file with no state entry is copied to
// <home>/.state/backups/<ts>/ before the writer's iox.AtomicWrite
// overwrites it.
func TestWriter_FileLevelBackup(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	_ = os.MkdirAll(home, 0o755)
	dest := filepath.Join(tmp, ".claude", "agents", "reviewer.md")
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	original := []byte("---\nname: reviewer\n---\nMy carefully hand-tuned reviewer prompt.\n")
	_ = os.WriteFile(dest, original, 0o644)

	st := state.New()
	w := render.NewWriter(st, home, tmp, adapter.ScopeUser, "", "claude")
	op := adapter.FileOp{
		Action:   "write",
		Path:     dest,
		Content:  []byte("---\nname: reviewer\n---\nNew shiny rendered prompt.\n"),
		Mode:     0o644,
		SourceID: "agents/reviewer.md",
	}
	if err := w.Write(op, op.Content); err != nil {
		t.Fatalf("Write: %v", err)
	}
	reports := w.Reports()
	if len(reports) != 1 {
		t.Fatalf("want 1 collision report; got %d (%v)", len(reports), reports)
	}
	if reports[0].Pointer != "" {
		t.Fatalf("expected file-level collision; got pointer %q", reports[0].Pointer)
	}
	got, err := os.ReadFile(reports[0].BackupTo)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("backup mismatch:\nwant: %s\ngot:  %s", original, got)
	}
	if !strings.HasPrefix(reports[0].BackupTo, filepath.Join(home, ".state", "backups")) {
		t.Fatalf("backup not under <home>/.state/backups: %s", reports[0].BackupTo)
	}
	// And the live file now has the new content.
	live, _ := os.ReadFile(dest)
	if !strings.Contains(string(live), "shiny rendered prompt") {
		t.Fatalf("live file missing new content: %s", live)
	}
}

// TestWriter_KeyLevelBackup asserts that for merge-json-keys ops, a per-
// pointer conflict in a shared JSON file (e.g. user's hand-edited
// ~/.claude.json with a custom mcpServers.github) backs up the whole
// file once before the merge writes.
func TestWriter_KeyLevelBackup(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	_ = os.MkdirAll(home, 0o755)
	dest := filepath.Join(tmp, ".claude.json")
	original := []byte(`{
  "mcpServers": {"github": {"command": "/usr/local/bin/my-fork", "args": ["--my-flag"]}},
  "preserveMe": "do not touch"
}`)
	_ = os.WriteFile(dest, original, 0o644)

	st := state.New()
	w := render.NewWriter(st, home, tmp, adapter.ScopeUser, "", "claude")
	ours := []byte(`{"mcpServers":{"github":{"command":"npx","args":["-y","@m/server-github"]}}}`)
	op := adapter.FileOp{
		Action:        "write",
		Path:          dest,
		Content:       ours,
		MergeStrategy: "merge-json-keys",
		Mode:          0o644,
		SourceID:      "mcp/github.toml",
	}

	// Compute final bytes the way the real adapter would.
	existing := map[string]any{}
	_ = json.Unmarshal(original, &existing)
	var oursMap map[string]any
	_ = json.Unmarshal(ours, &oursMap)
	for k, v := range oursMap {
		existing[k] = v
	}
	final, _ := json.MarshalIndent(existing, "", "  ")
	if err := w.Write(op, append(final, '\n')); err != nil {
		t.Fatalf("Write: %v", err)
	}

	reports := w.Reports()
	if len(reports) == 0 {
		t.Fatal("expected at least one key-level collision")
	}
	if reports[0].Pointer == "" {
		t.Fatalf("want per-pointer collision (Pointer set); got %+v", reports[0])
	}
	got, _ := os.ReadFile(reports[0].BackupTo)
	if !strings.Contains(string(got), "my-fork") {
		t.Fatalf("backup missing original command; got:\n%s", got)
	}
}

// TestWriter_KeyLevelBackup_ForeignNonObjectAtOwnedKey is the regression for
// silent data loss: when the existing dest holds a SCALAR or ARRAY at a
// top-level key agentsync owns as an object (e.g. mcpServers), overlayOwned
// replaces it wholesale, but maybeBackupKeyOp's per-child pointer loop never
// fired (getPointer can't descend a scalar/array), so the foreign value was
// overwritten with NO backup — violating the "back up foreign content before
// overwrite" invariant.
func TestWriter_KeyLevelBackup_ForeignNonObjectAtOwnedKey(t *testing.T) {
	cases := []struct {
		name   string
		dest   string
		marker string
	}{
		{"scalar", `{"mcpServers": "surprise-foreign-value", "preserveMe": "keep"}`, "surprise-foreign-value"},
		{"array", `{"mcpServers": ["foreign-array-entry"], "preserveMe": "keep"}`, "foreign-array-entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			home := filepath.Join(tmp, ".agentsync")
			_ = os.MkdirAll(home, 0o755)
			dest := filepath.Join(tmp, ".claude.json")
			_ = os.WriteFile(dest, []byte(tc.dest), 0o644)

			st := state.New()
			w := render.NewWriter(st, home, tmp, adapter.ScopeUser, "", "claude")
			ours := []byte(`{"mcpServers":{"github":{"command":"npx"}}}`)
			op := adapter.FileOp{
				Action:        "write",
				Path:          dest,
				Content:       ours,
				MergeStrategy: "merge-json-keys",
				Mode:          0o644,
				SourceID:      "mcp/github.toml",
			}
			final := []byte(`{"mcpServers":{"github":{"command":"npx"}},"preserveMe":"keep"}` + "\n")
			if err := w.Write(op, final); err != nil {
				t.Fatalf("Write: %v", err)
			}
			reports := w.Reports()
			if len(reports) == 0 {
				t.Fatal("expected a foreign-collision backup before wholesale-replacing the owned key, got none")
			}
			got, _ := os.ReadFile(reports[0].BackupTo)
			if !strings.Contains(string(got), tc.marker) {
				t.Fatalf("backup missing the foreign value %q; got:\n%s", tc.marker, got)
			}
		})
	}
}

// TestWriter_NoCollisionWhenAlreadyOwned asserts that when state already
// records the file (file-level FileEntry), no backup is written even if
// the bytes differ — that's just an in-place update by an owning agent.
func TestWriter_NoCollisionWhenAlreadyOwned(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	_ = os.MkdirAll(home, 0o755)
	dest := filepath.Join(tmp, "x.md")
	_ = os.WriteFile(dest, []byte("ours-v1"), 0o644)

	st := state.New()
	// State keys are HOME-relative against the user's $HOME (here tmp), so an
	// owned dest under tmp is recorded as "${HOME}/<rel>".
	rel, _ := filepath.Rel(tmp, dest)
	st.Files["claude:user::${HOME}/"+filepath.ToSlash(rel)] = state.FileEntry{SHA256: "anything"}

	w := render.NewWriter(st, home, tmp, adapter.ScopeUser, "", "claude")
	op := adapter.FileOp{Action: "write", Path: dest, Content: []byte("ours-v2"), Mode: 0o644}
	if err := w.Write(op, op.Content); err != nil {
		t.Fatal(err)
	}
	if len(w.Reports()) != 0 {
		t.Fatalf("expected no collisions when file is already owned; got %v", w.Reports())
	}
	if _, statErr := os.Stat(filepath.Join(home, ".state", "backups")); statErr == nil {
		t.Fatal("backups dir should not have been created")
	}
}

// TestWriter_NoCollisionWhenContentMatches asserts that a pre-existing
// file with the *same* content as what we'd write is not backed up —
// converged-on-arrival should be silent, not noisy.
func TestWriter_NoCollisionWhenContentMatches(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	_ = os.MkdirAll(home, 0o755)
	dest := filepath.Join(tmp, "match.md")
	content := []byte("identical")
	_ = os.WriteFile(dest, content, 0o644)

	st := state.New()
	w := render.NewWriter(st, home, tmp, adapter.ScopeUser, "", "claude")
	if err := w.Write(adapter.FileOp{Action: "write", Path: dest, Content: content}, content); err != nil {
		t.Fatal(err)
	}
	if len(w.Reports()) != 0 {
		t.Fatalf("expected no collision when content matches; got %v", w.Reports())
	}
}

// TestWriter_DeleteSkipsBackup asserts that delete ops do not produce
// backups — agentsync only deletes paths it already owns per state.
func TestWriter_DeleteSkipsBackup(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	_ = os.MkdirAll(home, 0o755)
	dest := filepath.Join(tmp, "to-delete.md")
	_ = os.WriteFile(dest, []byte("bye"), 0o644)

	st := state.New()
	w := render.NewWriter(st, home, tmp, adapter.ScopeUser, "", "claude")
	if err := w.Delete(adapter.FileOp{Action: "delete", Path: dest}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("dest should be gone after Delete")
	}
	if len(w.Reports()) != 0 {
		t.Fatalf("Delete must not produce collision reports; got %v", w.Reports())
	}
}

// TestRenderApply_MultipleMergeOpsSamePathAllApplied is the regression for
// the dedup bug: render.Apply skipped any second Action=="write" op for a
// path already seen, but that swept up merge-json-keys ops too. The claude
// adapter emits separate merge ops to settings.json for MCP, hooks, and LSP;
// deduping by path silently dropped hooks and LSP. Merge ops to the same
// path must ALL run (the adapter re-reads and merges each), so only
// whole-file replace writes may be deduped.
func TestRenderApply_MultipleMergeOpsSamePathAllApplied(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	_ = os.MkdirAll(home, 0o755)
	dest := filepath.Join(tmp, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)

	reg := adapter.NewRegistry()
	_ = reg.Register(&fakeJSONApply{name: "claude"})

	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {Ops: []adapter.FileOp{
				{Action: "write", Path: dest, Content: []byte(`{"mcpServers":{"a":1}}`), MergeStrategy: "merge-json-keys", Mode: 0o644},
				{Action: "write", Path: dest, Content: []byte(`{"hooks":{"PreToolUse":"echo"}}`), MergeStrategy: "merge-json-keys", Mode: 0o644},
				{Action: "write", Path: dest, Content: []byte(`{"lspServers":{"go":{"command":"gopls"}}}`), MergeStrategy: "merge-json-keys", Mode: 0o644},
			}},
		},
	}
	if _, _, err := render.Apply(plan, reg, state.New(), home, tmp, adapter.ScopeUser, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse settings.json: %v\n%s", err, data)
	}
	for _, key := range []string{"mcpServers", "hooks", "lspServers"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("merge op for %q was dropped; settings.json = %s", key, data)
		}
	}
}

// TestRenderApply_FullPathBacksUpAcrossAgents exercises the integrated
// path: render.Apply constructs one writer per agent, each agent's
// Apply routes through it, and the union of reports is returned.
func TestRenderApply_FullPathBacksUpAcrossAgents(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	_ = os.MkdirAll(home, 0o755)

	claudeDest := filepath.Join(tmp, ".claude", "agents", "x.md")
	_ = os.MkdirAll(filepath.Dir(claudeDest), 0o755)
	_ = os.WriteFile(claudeDest, []byte("original-claude"), 0o644)

	reg := adapter.NewRegistry()
	_ = reg.Register(&fakeJSONApply{name: "claude"})

	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {Ops: []adapter.FileOp{{
				Action:   "write",
				Path:     claudeDest,
				Content:  []byte("rendered-claude"),
				Mode:     0o644,
				SourceID: "agents/x.md",
			}}},
		},
	}
	st := state.New()
	reports, _, err := render.Apply(plan, reg, st, home, tmp, adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("want 1 backup; got %d", len(reports))
	}
	if !strings.HasPrefix(reports[0].BackupTo, filepath.Join(home, ".state", "backups")) {
		t.Fatalf("backup not under <home>/.state/backups: %s", reports[0].BackupTo)
	}
}

// TestPruneBackups_KeepsNewest verifies bounded backup retention: the most
// recent `keep` timestamp dirs survive, older ones are removed.
func TestPruneBackups_KeepsNewest(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".state", "backups")
	names := []string{
		"20260101T000001Z-000000001",
		"20260101T000002Z-000000001",
		"20260101T000003Z-000000001",
		"20260101T000004Z-000000001",
	}
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := render.PruneBackups(home, 2); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(root)
	if len(left) != 2 {
		t.Fatalf("want 2 backups kept, got %d", len(left))
	}
	for _, e := range left {
		if e.Name() < "20260101T000003Z" {
			t.Fatalf("pruned the wrong (newer) dir; kept %s", e.Name())
		}
	}
	// Missing backups root is a no-op, not an error.
	if err := render.PruneBackups(t.TempDir(), 2); err != nil {
		t.Fatalf("missing backups dir should be a no-op: %v", err)
	}
}

// TestWriter_JSONCFallbackBacksUpWholeFile covers maybeBackupFileOpForJSONCFallback
// (previously 0% covered): a merge op whose destination is JSONC (comments
// make strict json.Unmarshal fail) and which state does not own must be
// backed up whole-file before the merge writes.
func TestWriter_JSONCFallbackBacksUpWholeFile(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	_ = os.MkdirAll(home, 0o755)
	dest := filepath.Join(tmp, "opencode.json")
	original := []byte("// my hand-written config\n{\"mcp\":{\"github\":{\"command\":\"old\"}}}\n")
	_ = os.WriteFile(dest, original, 0o644)

	st := state.New() // nothing owned → must back up
	w := render.NewWriter(st, home, tmp, adapter.ScopeUser, "", "opencode")
	op := adapter.FileOp{
		Action:        "write",
		Path:          dest,
		Content:       []byte(`{"mcp":{"github":{"command":"new"}}}`),
		MergeStrategy: "merge-json-keys",
		Mode:          0o644,
	}
	if err := w.Write(op, []byte(`{"mcp":{"github":{"command":"new"}}}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	reports := w.Reports()
	if len(reports) != 1 || reports[0].Pointer != "" {
		t.Fatalf("expected one whole-file (no-pointer) backup; got %+v", reports)
	}
	got, _ := os.ReadFile(reports[0].BackupTo)
	if !strings.Contains(string(got), "old") {
		t.Fatalf("JSONC-fallback backup missing original content: %s", got)
	}
}
