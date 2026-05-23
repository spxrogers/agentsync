package render

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestBackupPathFor_NeverEscapesRoot asserts that no src — including
// adversarial inputs containing ".." or absolute paths — can place the
// backup destination outside backupRoot.
func TestBackupPathFor_NeverEscapesRoot(t *testing.T) {
	root := filepath.Join("/tmp", "agentsync-backup-root")
	cases := []string{
		"/home/user/.claude/settings.json",
		"/home/user/../../../etc/passwd",
		"../../etc/passwd",
		"/./././tmp/foo",
		"",
		".",
		"..",
	}
	for _, src := range cases {
		dest := backupPathFor(src, root)
		rel, err := filepath.Rel(root, dest)
		if err != nil {
			t.Errorf("backupPathFor(%q): Rel failed: %v", src, err)
			continue
		}
		if strings.HasPrefix(rel, "..") {
			t.Errorf("backupPathFor(%q) = %q escapes root %q (rel=%q)", src, dest, root, rel)
		}
	}
}
