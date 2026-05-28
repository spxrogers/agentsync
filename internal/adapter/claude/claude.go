// Package claude implements the Claude Code adapter for agentsync.
package claude

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
	// nil means os.Stderr. Tests inject a bytes.Buffer to assert on warnings.
	Stderr io.Writer
}

// stderr returns the configured warning sink, defaulting to os.Stderr.
func (a *Adapter) stderr() io.Writer {
	if a.opts.Stderr != nil {
		return a.opts.Stderr
	}
	return os.Stderr
}

// Adapter implements adapter.Adapter for Claude Code.
type Adapter struct {
	opts Options
}

// New constructs a Claude adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

func (a *Adapter) Name() string { return "claude" }

// KeyMergeStrategy is claude's single key-merge strategy: strict JSON
// (.claude.json, settings.json).
func (a *Adapter) KeyMergeStrategy() string { return "merge-json-keys" }

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
