package codex_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
)

// TestSetStderr_NilResetsToDefault is the codex adapter's mirror of the
// claude package's nil-reset test. Same contract, same shape: after
// SetStderr(nil), warnings emitted during Ingest must (a) NOT reach the
// previously-set buffer, and (b) reach os.Stderr instead. codex reads
// skills from the cross-agent ~/.agents/skills/ directory (see
// codex.ResolvePaths → SkillsDir).
func TestSetStderr_NilResetsToDefault(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, ".agents", "skills", "bad-yaml")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: bad-yaml\ndescription: Triggers on: rebase\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	a := codex.New(codex.Options{TargetRoot: tmp, Stderr: &warn})

	// Reset: a panic here is itself a contract failure.
	a.SetStderr(nil)

	captured := captureOsStderr(t, func() {
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

// captureOsStderr — see internal/adapter/claude/ingest_test.go for the
// shared shape. Per-package copy because `package foo_test` files can't
// share without introducing an extra helper package; ~15 duplicated
// lines is the lighter cost.
func captureOsStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}
