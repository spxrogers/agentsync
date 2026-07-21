package roo_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/roo"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestName(t *testing.T) {
	if got := roo.New(roo.Options{}).Name(); got != "roo" {
		t.Fatalf("Name() = %q, want roo", got)
	}
}

func TestKeyMergeStrategy(t *testing.T) {
	if got := roo.New(roo.Options{}).KeyMergeStrategy(); got != "merge-json-keys" {
		t.Fatalf("KeyMergeStrategy() = %q, want merge-json-keys", got)
	}
}

func TestDetect_ConfigDir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".roo"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := roo.New(roo.Options{TargetRoot: tmp})
	if ok, err := a.Detect(); err != nil || !ok {
		t.Fatalf("Detect() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestDetect_NotInstalled(t *testing.T) {
	a := roo.New(roo.Options{TargetRoot: t.TempDir()})
	if ok, err := a.Detect(); err != nil || ok {
		t.Fatalf("Detect() = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestRoo_DetectNoBinaryProbe pins that Roo detection is the `.roo/` config-dir
// stat ONLY — there is no `roo` CLI to look up (Roo is a VS Code(-family)
// extension), so the former LookPath("roo") binary probe was removed. The
// LookPath option field is gone, so a "fake roo on PATH" can no longer make an
// uninstalled Roo look present: with no .roo/ dir, Detect is false regardless of
// what is on PATH; with a .roo/ dir, it is true.
func TestRoo_DetectNoBinaryProbe(t *testing.T) {
	t.Run("no .roo dir → false (PATH is never consulted)", func(t *testing.T) {
		a := roo.New(roo.Options{TargetRoot: t.TempDir()})
		if ok, err := a.Detect(); err != nil || ok {
			t.Fatalf("Detect() = (%v, %v), want (false, nil)", ok, err)
		}
	})
	t.Run(".roo dir → true", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, ".roo"), 0o755); err != nil {
			t.Fatal(err)
		}
		a := roo.New(roo.Options{TargetRoot: tmp})
		if ok, err := a.Detect(); err != nil || !ok {
			t.Fatalf("Detect() = (%v, %v), want (true, nil)", ok, err)
		}
	})
}

func TestProjectScope_EmptyProjectErrors(t *testing.T) {
	c := source.Canonical{Memory: source.Memory{Body: "x\n"}}
	a := roo.New(roo.Options{TargetRoot: t.TempDir()})
	if _, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Render: want ErrProjectRootRequired, got %v", err)
	}
	if _, err := a.Ingest(adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Ingest: want ErrProjectRootRequired, got %v", err)
	}
}
