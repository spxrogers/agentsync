// Package opencode implements the OpenCode adapter for agentsync.
package opencode

import (
	"os"
	"os/exec"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// Options configure the adapter at construction.
type Options struct {
	TargetRoot string // honors AGENTSYNC_TARGET_ROOT (real "/Users/x" in production)
	// LookPath overrides exec.LookPath for testing. nil means use exec.LookPath.
	LookPath func(file string) (string, error)
}

// Adapter implements adapter.Adapter for OpenCode.
type Adapter struct{ opts Options }

// New constructs an OpenCode adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

func (a *Adapter) Name() string { return "opencode" }

func (a *Adapter) Capabilities() adapter.Capability {
	return adapter.CapMCP | adapter.CapMemory | adapter.CapSkill |
		adapter.CapSubagent | adapter.CapCommand
	// Hook + LSP capabilities omitted: shipped as ✗ skip in v1.
}

func (a *Adapter) Detect() (bool, error) {
	p := ResolvePaths(a.opts.TargetRoot, "", false)
	if _, err := os.Stat(p.ConfigDir); err == nil {
		return true, nil
	}
	lookPath := a.opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("opencode"); err == nil {
		return true, nil
	}
	return false, nil
}

// Render converts canonical source into FileOps for OpenCode. Stub: will be
// filled in Task 4–8.
func (a *Adapter) Render(_ source.Canonical, _ adapter.Scope, _ string) ([]adapter.FileOp, []adapter.Skip, error) {
	return nil, nil, nil
}

// Ingest reads OpenCode's native config files back into a source.Canonical. Stub:
// will be filled in Task 10.
func (a *Adapter) Ingest(_ adapter.Scope, _ string) (source.Canonical, error) {
	return source.Canonical{}, nil
}

// Apply writes FileOps to disk. Stub: will be filled in Task 9.
func (a *Adapter) Apply(_ []adapter.FileOp) error { return nil }
