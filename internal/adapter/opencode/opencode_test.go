package opencode_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
)

func TestAdapter_Identity(t *testing.T) {
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	if a.Name() != "opencode" {
		t.Fatalf("Name = %q", a.Name())
	}
	if a.Capabilities() == 0 {
		t.Fatalf("expected non-zero capabilities")
	}
	// Must satisfy the interface.
	var _ adapter.Adapter = a
	// Compile-time pin: same drift-detection as the claude adapter — drops
	// of SetStderr fail the build before RouteTo would silently no-op us.
	var _ adapter.WarnSink = a
}

func TestAdapter_Capabilities_HasExpected(t *testing.T) {
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	caps := a.Capabilities()

	for _, want := range []adapter.Capability{
		adapter.CapMCP,
		adapter.CapMemory,
		adapter.CapSkill,
		adapter.CapSubagent,
		adapter.CapCommand,
	} {
		if caps&want == 0 {
			t.Fatalf("expected capability %v to be set, got %b", want, caps)
		}
	}
	// Hook and LSP are intentionally absent in v1.
	for _, absent := range []adapter.Capability{
		adapter.CapHook,
		adapter.CapLSP,
	} {
		if caps&absent != 0 {
			t.Fatalf("expected capability %v to be absent in v1, got %b", absent, caps)
		}
	}
}

func TestAdapter_DetectsConfigDir(t *testing.T) {
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, ".config", "opencode")
	_ = os.MkdirAll(cfgDir, 0o755)

	a := opencode.New(opencode.Options{TargetRoot: tmp})
	ok, err := a.Detect()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected Detect=true when ~/.config/opencode/ exists")
	}
}

func TestAdapter_DetectsViaLookPath(t *testing.T) {
	tmp := t.TempDir() // no config dir

	fakeLookPath := func(string) (string, error) { return "/usr/local/bin/opencode", nil }
	a := opencode.New(opencode.Options{TargetRoot: tmp, LookPath: fakeLookPath})
	ok, err := a.Detect()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected Detect=true when opencode is on PATH")
	}
}

func TestAdapter_DetectReturnsFalse_WhenAbsent(t *testing.T) {
	notFound := func(string) (string, error) { return "", os.ErrNotExist }
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir(), LookPath: notFound})
	ok, _ := a.Detect()
	if ok {
		t.Fatalf("expected Detect=false when config dir absent and not on PATH")
	}
}
