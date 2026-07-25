package gemini

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestIngest_MidWalkErrorFailsLoud pins the walkErr return contract: a mid-walk
// failure (an unreadable subdirectory reaching the WalkDir callback's err
// argument) must fail Ingest loudly — a demoted warning would hand back a
// silently short Commands slice that drift/capture misreads as deletions. The
// error is injected through the walkCommandsDir seam because an unreadable
// subdir cannot be planted deterministically in the privileged container
// (chmod 0o000 is bypassed by root). White-box: the seam is unexported.
func TestIngest_MidWalkErrorFailsLoud(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".gemini", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.toml"),
		[]byte("description = \"d\"\nprompt = \"p\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := walkCommandsDir
	defer func() { walkCommandsDir = orig }()
	walkCommandsDir = func(root string, fn fs.WalkDirFunc) error {
		// Deliver the real entries first, then the mid-walk error, exactly as
		// WalkDir does when it hits an unreadable subdirectory.
		if err := filepath.WalkDir(root, fn); err != nil {
			return err
		}
		return fn(filepath.Join(root, "broken"), nil, fmt.Errorf("permission denied (injected)"))
	}

	a := New(Options{TargetRoot: tmp})
	_, err := a.Ingest(adapter.ScopeUser, "")
	if err == nil {
		t.Fatal("a mid-walk error must fail Ingest loudly, not warn into a short Commands slice")
	}
	if !strings.Contains(err.Error(), "read commands dir") {
		t.Fatalf("error should carry the commands-dir context; got: %v", err)
	}
}
