package cline_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/cline"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestName(t *testing.T) {
	if got := cline.New(cline.Options{}).Name(); got != "cline" {
		t.Fatalf("Name() = %q, want cline", got)
	}
}

func TestKeyMergeStrategy(t *testing.T) {
	if got := cline.New(cline.Options{}).KeyMergeStrategy(); got != "merge-json-keys" {
		t.Fatalf("KeyMergeStrategy() = %q, want merge-json-keys", got)
	}
}

func TestCapabilities(t *testing.T) {
	caps := cline.New(cline.Options{}).Capabilities()
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
	if err := os.MkdirAll(filepath.Join(tmp, ".cline"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := cline.New(cline.Options{TargetRoot: tmp, LookPath: func(string) (string, error) { return "", errors.New("nope") }})
	if ok, err := a.Detect(); err != nil || !ok {
		t.Fatalf("Detect() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestDetect_NotInstalled(t *testing.T) {
	a := cline.New(cline.Options{TargetRoot: t.TempDir(), LookPath: func(string) (string, error) { return "", errors.New("nope") }})
	if ok, err := a.Detect(); err != nil || ok {
		t.Fatalf("Detect() = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestProjectScope_EmptyProjectErrors(t *testing.T) {
	c := source.Canonical{Memory: source.Memory{Body: "x\n"}}
	a := cline.New(cline.Options{TargetRoot: t.TempDir()})
	if _, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Render: want ErrProjectRootRequired, got %v", err)
	}
	if _, err := a.Ingest(adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Ingest: want ErrProjectRootRequired, got %v", err)
	}
}
