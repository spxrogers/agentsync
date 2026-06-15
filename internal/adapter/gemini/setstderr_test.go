package gemini_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/adaptertest"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
)

// TestSetStderr_NilResetsToDefault mirrors the claude/codex/cursor nil-reset
// test. After SetStderr(nil), warnings emitted during Ingest must (a) NOT reach
// the previously-set buffer and (b) reach os.Stderr instead. gemini emits the
// lenient-YAML warning while parsing subagent frontmatter (.gemini/agents/*.md).
func TestSetStderr_NilResetsToDefault(t *testing.T) {
	tmp := t.TempDir()
	agentsDir := filepath.Join(tmp, ".gemini", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "bad.md"),
		[]byte("---\nname: bad\ndescription: Triggers on: rebase\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	a := gemini.New(gemini.Options{TargetRoot: tmp, Stderr: &warn})
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

// TestSetStderr_SnapshotAtIngestEntry mirrors the claude/codex/cursor snapshot
// test: the warning sink is snapshotted at Ingest entry, so a mid-Ingest
// SetStderr does not redirect the remainder of that call.
func TestSetStderr_SnapshotAtIngestEntry(t *testing.T) {
	tmp := t.TempDir()
	agentsDir := filepath.Join(tmp, ".gemini", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		body := "---\nname: " + name + "\ndescription: Triggers on: rebase\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(agentsDir, name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var sibling bytes.Buffer
	primary := &adaptertest.SwapOnFirstWriteBuffer{}
	a := gemini.New(gemini.Options{TargetRoot: tmp, Stderr: primary})
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
