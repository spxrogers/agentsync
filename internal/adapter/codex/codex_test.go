package codex_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
)

func TestAdapter_Identity(t *testing.T) {
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	if a.Name() != "codex" {
		t.Fatalf("Name = %q", a.Name())
	}
	if a.Capabilities() == 0 {
		t.Fatalf("expected non-zero capabilities")
	}
	if a.KeyMergeStrategy() != "merge-toml-keys" {
		t.Fatalf("KeyMergeStrategy = %q, want merge-toml-keys", a.KeyMergeStrategy())
	}
	// Must satisfy both interfaces.
	var _ adapter.Adapter = a
	var _ adapter.PluginIngester = a
	// Compile-time pin: drift on SetStderr fails the build before RouteTo
	// would silently no-op us at runtime.
	var _ adapter.WarnEmitter = a
}

func TestAdapter_Capabilities_HasExpected(t *testing.T) {
	a := codex.New(codex.Options{TargetRoot: t.TempDir()})
	caps := a.Capabilities()

	for _, want := range []adapter.Capability{
		adapter.CapMCP,
		adapter.CapMemory,
		adapter.CapSkill,
		adapter.CapSubagent,
		adapter.CapCommand,
		adapter.CapHook,
	} {
		if caps&want == 0 {
			t.Fatalf("expected capability %v to be set, got %b", want, caps)
		}
	}
	// Codex has no LSP concept.
	if caps&adapter.CapLSP != 0 {
		t.Fatalf("expected CapLSP to be absent, got %b", caps)
	}
}

func TestAdapter_DetectsConfigDir(t *testing.T) {
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, ".codex")
	_ = os.MkdirAll(cfgDir, 0o755)

	a := codex.New(codex.Options{TargetRoot: tmp})
	ok, err := a.Detect()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected Detect=true when ~/.codex/ exists")
	}
}

func TestAdapter_DetectsViaLookPath(t *testing.T) {
	tmp := t.TempDir() // no config dir
	fakeLookPath := func(string) (string, error) { return "/usr/local/bin/codex", nil }
	a := codex.New(codex.Options{TargetRoot: tmp, LookPath: fakeLookPath})
	ok, err := a.Detect()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected Detect=true when codex is on PATH")
	}
}

func TestAdapter_DetectReturnsFalse_WhenAbsent(t *testing.T) {
	notFound := func(string) (string, error) { return "", os.ErrNotExist }
	a := codex.New(codex.Options{TargetRoot: t.TempDir(), LookPath: notFound})
	ok, _ := a.Detect()
	if ok {
		t.Fatalf("expected Detect=false when config dir absent and not on PATH")
	}
}
