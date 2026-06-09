// Package continuedev implements the Continue adapter for agentsync.
//
// (The package is named continuedev rather than `continue` because `continue` is
// a Go keyword; the agent's name — Adapter.Name() — is still "continue".)
//
// Continue stores configuration under ~/.continue (global) and <repo>/.continue
// (project), composed from "blocks" — individual files in per-kind subdirectories
// (per docs.continue.dev). agentsync projects to three of them:
//
//   - MCP servers → .continue/mcpServers/<id>.yaml  (one YAML block per server)
//   - Memory      → .continue/rules/agentsync.md    (a plain always-apply rule)
//   - Commands    → .continue/prompts/<name>.md      (one prompt block per command)
//
// Each is a whole-file (one-file-per-item) write, so the adapter has NO
// key-merge strategy. Continue has no Agent Skills, no per-file subagents (its
// "agents" are top-level assistants), no declarative hooks, and no LSP config, so
// those components are skipped with a report. See docs/capability-matrix.md for
// the per-component coverage and documented loss.
package continuedev

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
	// Stderr receives Ingest warnings. nil means os.Stderr.
	Stderr io.Writer
}

// Adapter implements adapter.Adapter for Continue.
type Adapter struct{ opts Options }

// New constructs a Continue adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

// stderr returns the configured warning sink, defaulting to os.Stderr.
func (a *Adapter) stderr() io.Writer {
	if a.opts.Stderr != nil {
		return a.opts.Stderr
	}
	return os.Stderr
}

// SetStderr replaces the warning sink the adapter writes Ingest warnings to.
// See claude.Adapter.SetStderr for the contract.
func (a *Adapter) SetStderr(w io.Writer) { a.opts.Stderr = w }

func (a *Adapter) Name() string { return "continue" }

// KeyMergeStrategy returns "" — Continue projects every component as a whole-file
// write (one YAML/markdown block per item), so the adapter owns no shared
// key-merge file.
func (a *Adapter) KeyMergeStrategy() string { return "" }

func (a *Adapter) Capabilities() adapter.Capability {
	return adapter.CapMCP | adapter.CapMemory | adapter.CapCommand
	// Skill/Subagent/Hook/LSP omitted: Continue has no faithful target for them
	// (it uses top-level assistants + rules, not per-file subagents or hooks).
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
	// Continue's CLI binary is `cn`.
	if _, err := lookPath("cn"); err == nil {
		return true, nil
	}
	return false, nil
}

// Render is implemented in render.go.
// Ingest is implemented in ingest.go.
// Apply is implemented in apply.go.
