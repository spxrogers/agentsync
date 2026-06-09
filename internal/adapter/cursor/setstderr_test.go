package cursor_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/adaptertest"
	"github.com/spxrogers/agentsync/internal/adapter/cursor"
)

// TestSetStderr_NilResetsToDefault is the cursor adapter's mirror of the claude/
// codex nil-reset test. After SetStderr(nil), warnings emitted during Ingest must
// (a) NOT reach the previously-set buffer and (b) reach os.Stderr instead. cursor
// reads skills from .cursor/skills/.
func TestSetStderr_NilResetsToDefault(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, ".cursor", "skills", "bad-yaml")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: bad-yaml\ndescription: Triggers on: rebase\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	a := cursor.New(cursor.Options{TargetRoot: tmp, Stderr: &warn})
	a.SetStderr(nil)

	captured := adaptertest.CaptureOsStderr(t, func() {
		if _, err := a.Ingest(adapter.ScopeUser, ""); err != nil {
			t.Fatalf("Ingest after SetStderr(nil): %v", err)
		}
	})

	if warn.Len() > 0 {
		t.Fatalf("SetStderr(nil) did not detach the previously-set buffer; got:\n%s", warn.String())
	}
	if !strings.Contains(captured, "frontmatter is not strict YAML") {
		t.Fatalf("SetStderr(nil) must route to os.Stderr default; captured nothing matching the lenient-YAML notice:\n%s", captured)
	}
}

// TestSetStderr_SnapshotAtIngestEntry mirrors the claude/codex snapshot test:
// the warning sink is snapshotted at Ingest entry, so a mid-Ingest SetStderr does
// not redirect the remainder of that call.
func TestSetStderr_SnapshotAtIngestEntry(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"first", "second"} {
		dir := filepath.Join(tmp, ".cursor", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: Triggers on: rebase\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var sibling bytes.Buffer
	primary := &adaptertest.SwapOnFirstWriteBuffer{}
	a := cursor.New(cursor.Options{TargetRoot: tmp, Stderr: primary})
	primary.OnFirstWrite = func() { a.SetStderr(&sibling) }

	if _, err := a.Ingest(adapter.ScopeUser, ""); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if !primary.Fired() {
		t.Fatal("OnFirstWrite was never called — fixture didn't trigger any warning")
	}
	if sibling.Len() > 0 {
		t.Fatalf("snapshot contract violated: warnings after mid-Ingest SetStderr landed in the sibling buffer:\n%s", sibling.String())
	}
	if strings.Count(primary.String(), "frontmatter is not strict YAML") < 2 {
		t.Fatalf("expected BOTH lenient-YAML warnings in the original sink (snapshot); got:\n%s", primary.String())
	}
}
