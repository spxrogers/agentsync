package claude_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
)

func TestAdapter_Identity(t *testing.T) {
	a := claude.New(claude.Options{TargetRoot: t.TempDir()})
	if a.Name() != "claude" {
		t.Fatalf("Name = %q", a.Name())
	}
	if a.Capabilities() == 0 {
		t.Fatalf("expected non-zero capabilities")
	}
	var _ adapter.Adapter = a
	// Compile-time pin: a future refactor that drops SetStderr (or changes
	// its signature) fails to build here, surfacing the drift before
	// ui.WarnWriter.RouteTo would silently no-op the adapter at runtime.
	var _ adapter.WarnEmitter = a
}

func TestAdapter_DetectsHomeDir(t *testing.T) {
	tmp := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmp, ".claude"), 0o755)

	a := claude.New(claude.Options{TargetRoot: tmp})
	ok, err := a.Detect()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected Detect=true when ~/.claude/ exists")
	}
}

func TestAdapter_DetectsAbsentReturnsFalse(t *testing.T) {
	noPath := func(string) (string, error) { return "", os.ErrNotExist }
	a := claude.New(claude.Options{TargetRoot: t.TempDir(), LookPath: noPath})
	ok, _ := a.Detect()
	if ok {
		t.Fatalf("expected Detect=false on empty home")
	}
}
