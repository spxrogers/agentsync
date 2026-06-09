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

func TestCapabilities(t *testing.T) {
	caps := roo.New(roo.Options{}).Capabilities()
	for _, c := range []struct {
		name string
		cap  adapter.Capability
		want bool
	}{
		{"MCP", adapter.CapMCP, true},
		{"Memory", adapter.CapMemory, true},
		{"Command", adapter.CapCommand, true},
		{"Skill", adapter.CapSkill, false},
		{"Subagent", adapter.CapSubagent, false},
		{"Hook", adapter.CapHook, false},
		{"LSP", adapter.CapLSP, false},
	} {
		if got := caps&c.cap != 0; got != c.want {
			t.Errorf("Capabilities() has %s = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDetect_ConfigDir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".roo"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := roo.New(roo.Options{TargetRoot: tmp, LookPath: func(string) (string, error) { return "", errors.New("nope") }})
	if ok, err := a.Detect(); err != nil || !ok {
		t.Fatalf("Detect() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestDetect_NotInstalled(t *testing.T) {
	a := roo.New(roo.Options{TargetRoot: t.TempDir(), LookPath: func(string) (string, error) { return "", errors.New("nope") }})
	if ok, err := a.Detect(); err != nil || ok {
		t.Fatalf("Detect() = (%v, %v), want (false, nil)", ok, err)
	}
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
