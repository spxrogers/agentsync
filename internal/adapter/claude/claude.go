// Package claude implements the Claude Code adapter for agentsync.
package claude

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

// Adapter implements adapter.Adapter for Claude Code.
type Adapter struct {
	opts Options
}

// New constructs a Claude adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

func (a *Adapter) Name() string { return "claude" }

func (a *Adapter) Capabilities() adapter.Capability {
	return adapter.CapMCP | adapter.CapMemory | adapter.CapSkill |
		adapter.CapSubagent | adapter.CapCommand | adapter.CapHook | adapter.CapLSP
}

func (a *Adapter) Detect() (bool, error) {
	p := ResolvePaths(a.opts.TargetRoot, "", false)
	if _, err := os.Stat(p.Home); err == nil {
		return true, nil
	}
	lookPath := a.opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("claude"); err == nil {
		return true, nil
	}
	return false, nil
}

// Render is a stub — implemented in render.go (M1 Task 5+).
func (a *Adapter) Render(_ source.Canonical, _ adapter.Scope, _ string) ([]adapter.FileOp, []adapter.Skip, error) {
	return nil, nil, nil
}

// Ingest is a stub — implemented in ingest.go (M1 Task 9+).
func (a *Adapter) Ingest(_ adapter.Scope, _ string) (source.Canonical, error) {
	return source.Canonical{}, nil
}

// Apply is a stub — implemented in apply.go (M1 Task 10+).
func (a *Adapter) Apply(_ []adapter.FileOp) error {
	return nil
}
