package opencode_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
)

// TestSetStderr_NilResetsToDefault is the opencode adapter's mirror of
// the claude package's nil-reset test. Same contract, same shape: after
// SetStderr(nil), warnings emitted during Ingest must (a) NOT reach the
// previously-set buffer, and (b) reach os.Stderr instead. opencode reads
// skills from the shared ~/.claude/skills/ directory (see
// opencode.ResolvePaths → ClaudeSkillsDir), so the fixture lives there.
//
// The contract (adapter.WarnEmitter) is interface-level; one test per
// implementing adapter is the agreed-upon coverage. See
// internal/adapter/claude/ingest_test.go for the lead test.
func TestSetStderr_NilResetsToDefault(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, ".claude", "skills", "bad-yaml")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: bad-yaml\ndescription: Triggers on: rebase\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	a := opencode.New(opencode.Options{TargetRoot: tmp, Stderr: &warn})

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

// captureOsStderr swaps os.Stderr for a pipe, runs fn, then restores and
// drains the pipe. Per-package copy of the helper in
// internal/adapter/claude/ingest_test.go — sharing it across `package
// foo_test` files would require a new exported helper package; ~15
// lines duplicated keeps the test contract local to each adapter.
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
