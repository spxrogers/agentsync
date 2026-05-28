// Package opencode implements the OpenCode adapter for agentsync.
package opencode

import (
	"io"
	"os"
	"os/exec"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// Options configure the adapter at construction.
type Options struct {
	TargetRoot string // honors AGENTSYNC_TARGET_ROOT (real "/Users/x" in production)
	// LookPath overrides exec.LookPath for testing. nil means use exec.LookPath.
	LookPath func(file string) (string, error)
	// Stderr receives Ingest warnings (lenient-YAML notices, dropped components).
	// nil means os.Stderr.
	Stderr io.Writer
}

// Adapter implements adapter.Adapter for OpenCode.
type Adapter struct{ opts Options }

// New constructs an OpenCode adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

// stderr returns the configured warning sink, defaulting to os.Stderr.
func (a *Adapter) stderr() io.Writer {
	if a.opts.Stderr != nil {
		return a.opts.Stderr
	}
	return os.Stderr
}

func (a *Adapter) Name() string { return "opencode" }

// KeyMergeStrategy is opencode's single key-merge strategy: JSONC
// (opencode.json), which MUST be merged via hujson, not strict JSON.
func (a *Adapter) KeyMergeStrategy() string { return "merge-jsonc-keys" }

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

// Render is implemented in render.go.
// Ingest is implemented in ingest.go.
// Apply is implemented in apply.go.
