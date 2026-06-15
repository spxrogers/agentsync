package cursor_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/cursor"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestName(t *testing.T) {
	a := cursor.New(cursor.Options{})
	if a.Name() != "cursor" {
		t.Fatalf("Name() = %q, want cursor", a.Name())
	}
}

func TestKeyMergeStrategy(t *testing.T) {
	a := cursor.New(cursor.Options{})
	if got := a.KeyMergeStrategy(); got != "merge-json-keys" {
		t.Fatalf("KeyMergeStrategy() = %q, want merge-json-keys", got)
	}
}

// TestCapabilities pins the component bitmask: MCP/Memory/Skill/Subagent/Command/
// Hook supported, LSP NOT (Cursor has no LSP config concept).
func TestCapabilities(t *testing.T) {
	a := cursor.New(cursor.Options{})
	caps := a.Capabilities()
	for _, c := range []struct {
		name string
		cap  adapter.Capability
		want bool
	}{
		{"MCP", adapter.CapMCP, true},
		{"Memory", adapter.CapMemory, true},
		{"Skill", adapter.CapSkill, true},
		{"Subagent", adapter.CapSubagent, true},
		{"Command", adapter.CapCommand, true},
		{"Hook", adapter.CapHook, true},
		{"LSP", adapter.CapLSP, false},
	} {
		if got := caps&c.cap != 0; got != c.want {
			t.Errorf("Capabilities() has %s = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestDetect_ConfigDir reports installed when ~/.cursor exists, regardless of a
// `cursor` binary on PATH (LookPath stubbed to always fail).
func TestDetect_ConfigDir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := cursor.New(cursor.Options{
		TargetRoot: tmp,
		LookPath:   func(string) (string, error) { return "", errors.New("not found") },
	})
	ok, err := a.Detect()
	if err != nil || !ok {
		t.Fatalf("Detect() = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestDetect_Binary reports installed when the cursor binary is on PATH even
// though ~/.cursor does not exist.
func TestDetect_Binary(t *testing.T) {
	a := cursor.New(cursor.Options{
		TargetRoot: t.TempDir(),
		LookPath:   func(string) (string, error) { return "/usr/local/bin/cursor", nil },
	})
	ok, err := a.Detect()
	if err != nil || !ok {
		t.Fatalf("Detect() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestDetect_NotInstalled(t *testing.T) {
	a := cursor.New(cursor.Options{
		TargetRoot: t.TempDir(),
		LookPath:   func(string) (string, error) { return "", errors.New("not found") },
	})
	ok, err := a.Detect()
	if err != nil || ok {
		t.Fatalf("Detect() = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestProjectScope_EmptyProjectErrors pins the adapter-boundary guard: a
// project-scope Render/Ingest with no project root must fail loudly
// (adapter.ErrProjectRootRequired) rather than silently fall through to the
// user-scope ~/.cursor/ paths.
func TestProjectScope_EmptyProjectErrors(t *testing.T) {
	enabled := true
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "x",
		Server: source.MCPServerSpec{Command: "y", Enabled: &enabled},
	}}}
	a := cursor.New(cursor.Options{TargetRoot: t.TempDir()})
	if _, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Render: want ErrProjectRootRequired, got %v", err)
	}
	if _, err := a.Ingest(adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Ingest: want ErrProjectRootRequired, got %v", err)
	}
}
