package continuedev_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/continuedev"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestName(t *testing.T) {
	if got := continuedev.New(continuedev.Options{}).Name(); got != "continue" {
		t.Fatalf("Name() = %q, want continue", got)
	}
}

// TestKeyMergeStrategy: Continue projects whole-file blocks, so no key-merge.
func TestKeyMergeStrategy(t *testing.T) {
	if got := continuedev.New(continuedev.Options{}).KeyMergeStrategy(); got != "" {
		t.Fatalf("KeyMergeStrategy() = %q, want \"\"", got)
	}
}

func TestDetect_ConfigDir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".continue"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := continuedev.New(continuedev.Options{TargetRoot: tmp, LookPath: func(string) (string, error) { return "", errors.New("nope") }})
	if ok, err := a.Detect(); err != nil || !ok {
		t.Fatalf("Detect() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestDetect_Binary(t *testing.T) {
	a := continuedev.New(continuedev.Options{TargetRoot: t.TempDir(), LookPath: func(f string) (string, error) {
		if f == "cn" {
			return "/usr/bin/cn", nil
		}
		return "", errors.New("nope")
	}})
	if ok, err := a.Detect(); err != nil || !ok {
		t.Fatalf("Detect() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestDetect_NotInstalled(t *testing.T) {
	a := continuedev.New(continuedev.Options{TargetRoot: t.TempDir(), LookPath: func(string) (string, error) { return "", errors.New("nope") }})
	if ok, err := a.Detect(); err != nil || ok {
		t.Fatalf("Detect() = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestProjectScope_EmptyProjectErrors pins the adapter-boundary guard.
func TestProjectScope_EmptyProjectErrors(t *testing.T) {
	enabled := true
	c := source.Canonical{MCPServers: []source.MCPServer{{ID: "x", Server: source.MCPServerSpec{Command: "y", Enabled: &enabled}}}}
	a := continuedev.New(continuedev.Options{TargetRoot: t.TempDir()})
	if _, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Render: want ErrProjectRootRequired, got %v", err)
	}
	if _, err := a.Ingest(adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Ingest: want ErrProjectRootRequired, got %v", err)
	}
}
