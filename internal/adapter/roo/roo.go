// Package roo implements the Roo Code adapter for agentsync.
//
// Roo Code (a VS Code extension) keeps clean, filesystem-based config under a
// `.roo/` tree at both scopes (per docs.roocode.com): project-level at
// <repo>/.roo and global at ~/.roo. agentsync projects three components:
//
//   - MCP servers → .roo/mcp.json (a JSON `mcpServers` object; project-level).
//     Roo's GLOBAL MCP lives in VS Code's globalStorage (an OS- and editor-
//     variant-specific path no config-sync tool writes — rulesync, ruler, gaal,
//     ai-rulez, and agentsmesh all skip it), so agentsync targets the clean
//     project-level `.roo/mcp.json` and reports a skip for user-scope MCP. This
//     matches the dominant prior-art pattern (rulesync + ruler both do exactly
//     this).
//   - Memory  → .roo/rules/agentsync.md (a plain-markdown rule; Roo reads
//     .roo/rules/ recursively). Both scopes (~/.roo/rules and <repo>/.roo/rules).
//   - Commands → .roo/commands/<name>.md (markdown + YAML frontmatter:
//     description + argument-hint). Both scopes.
//
// Skills, subagents (Roo's "custom modes" are not a 1:1), hooks, and LSP have no
// faithful Roo target and are skipped. See docs/capability-matrix.md.
package roo

import (
	"io"
	"os"
	"os/exec"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// Options configure the adapter at construction.
type Options struct {
	TargetRoot string // honors AGENTSYNC_TARGET_ROOT
	LookPath   func(file string) (string, error)
	Stderr     io.Writer // Ingest warnings; nil means os.Stderr
}

// Adapter implements adapter.Adapter for Roo Code.
type Adapter struct{ opts Options }

// New constructs a Roo Code adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

func (a *Adapter) stderr() io.Writer {
	if a.opts.Stderr != nil {
		return a.opts.Stderr
	}
	return os.Stderr
}

// SetStderr replaces the warning sink. See claude.Adapter.SetStderr.
func (a *Adapter) SetStderr(w io.Writer) { a.opts.Stderr = w }

func (a *Adapter) Name() string { return "roo" }

// KeyMergeStrategy is roo's single key-merge strategy: JSON (.roo/mcp.json — the
// only file whose keys agentsync co-owns; rules/commands are whole-file writes).
func (a *Adapter) KeyMergeStrategy() string { return "merge-json-keys" }

func (a *Adapter) Capabilities() adapter.Capability {
	return adapter.CapMCP | adapter.CapMemory | adapter.CapCommand
	// Skill/Subagent/Hook/LSP omitted: no faithful Roo target (all ✗ skip).
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
	if _, err := lookPath("roo"); err == nil {
		return true, nil
	}
	return false, nil
}

// Render is implemented in render.go.
// Ingest is implemented in ingest.go.
// Apply is implemented in apply.go.
