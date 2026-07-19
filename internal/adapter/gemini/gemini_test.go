package gemini_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestName(t *testing.T) {
	if got := gemini.New(gemini.Options{}).Name(); got != "gemini" {
		t.Fatalf("Name() = %q, want gemini", got)
	}
}

func TestKeyMergeStrategy(t *testing.T) {
	if got := gemini.New(gemini.Options{}).KeyMergeStrategy(); got != "merge-jsonc-keys" {
		t.Fatalf("KeyMergeStrategy() = %q, want merge-jsonc-keys", got)
	}
}

func TestDetect_ConfigDir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := gemini.New(gemini.Options{TargetRoot: tmp, LookPath: func(string) (string, error) { return "", errors.New("nope") }})
	if ok, err := a.Detect(); err != nil || !ok {
		t.Fatalf("Detect() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestDetect_Binary(t *testing.T) {
	a := gemini.New(gemini.Options{TargetRoot: t.TempDir(), LookPath: func(string) (string, error) { return "/usr/bin/gemini", nil }})
	if ok, err := a.Detect(); err != nil || !ok {
		t.Fatalf("Detect() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestDetect_NotInstalled(t *testing.T) {
	a := gemini.New(gemini.Options{TargetRoot: t.TempDir(), LookPath: func(string) (string, error) { return "", errors.New("nope") }})
	if ok, err := a.Detect(); err != nil || ok {
		t.Fatalf("Detect() = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestProjectScope_EmptyProjectErrors pins the adapter-boundary guard.
func TestProjectScope_EmptyProjectErrors(t *testing.T) {
	enabled := true
	c := source.Canonical{MCPServers: []source.MCPServer{{ID: "x", Server: source.MCPServerSpec{Command: "y", Enabled: &enabled}}}}
	a := gemini.New(gemini.Options{TargetRoot: t.TempDir()})
	if _, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Render: want ErrProjectRootRequired, got %v", err)
	}
	if _, err := a.Ingest(adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Ingest: want ErrProjectRootRequired, got %v", err)
	}
}
