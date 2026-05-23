package project_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestMerge_MemoryImport_RejectsSymlinkEscape is the regression for a
// containment hole in project memory imports. importWithinRoot was purely
// lexical, so a committed symlink under the project root (link -> /etc/passwd)
// passed the check and os.ReadFile followed it off-root, splicing arbitrary
// host-file content into the rendered memory. Project markers come from cloned
// repos, so this is attacker-influenced.
func TestMerge_MemoryImport_RejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// Secret file OUTSIDE the project root.
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-HOST-DATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink INSIDE the root pointing at the outside secret.
	link := filepath.Join(root, "leak.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	m := &project.Marker{
		Root:   root,
		Memory: project.ProjectMemorySection{Import: []string{"leak.md"}},
	}
	out := project.Merge(source.Canonical{}, m)
	if strings.Contains(out.Memory.Body, "TOP-SECRET-HOST-DATA") {
		t.Fatalf("symlinked import escaped the project root and leaked host data: %q", out.Memory.Body)
	}
}

// TestMerge_MemoryImport_AllowsRegularFile confirms the guard still imports a
// legitimate in-root file.
func TestMerge_MemoryImport_AllowsRegularFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "extra.md"), []byte("project memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &project.Marker{
		Root:   root,
		Memory: project.ProjectMemorySection{Import: []string{"extra.md"}},
	}
	out := project.Merge(source.Canonical{}, m)
	if !strings.Contains(out.Memory.Body, "project memory") {
		t.Fatalf("legitimate in-root import was dropped: %q", out.Memory.Body)
	}
}
