// Package cline implements the Cline adapter for agentsync.
//
// Cline's config is scope-asymmetric (verified against docs.cline.bot and the
// prior art of rulesync/ruler/agentsmesh):
//
//   - MCP servers: Cline has NO project-level MCP file, and its VS Code-extension
//     MCP lives in OS/editor-specific globalStorage that no config-sync tool
//     writes. Cline's CLI, however, reads a clean `~/.cline/mcp.json`, so
//     agentsync targets THAT at user scope and skips (reports) project-scope MCP.
//   - Rules (memory): `.clinerules/` directory of plain markdown at the repo root
//     (Cline concatenates them). Cline's global rules live in `~/Documents/Cline/`
//     (a non-XDG app path agentsync does not target), so memory renders at project
//     scope only.
//   - Workflows (slash commands): `.clinerules/workflows/<name>.md`, plain
//     markdown invoked as `/<name>.md`. Project scope only (Cline's global
//     rules/workflows live under the non-XDG ~/Documents/Cline/ path agentsync
//     deliberately does not target).
//
// Skills, subagents, hooks, and LSP have no Cline concept and are skipped. Rules/
// workflows are plain markdown (no frontmatter parsing), so the adapter emits no
// Ingest warnings and does not implement WarnEmitter. See docs/capability-matrix.md.
package cline

import (
	"os"
	"os/exec"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// Options configure the adapter at construction.
type Options struct {
	TargetRoot string // honors AGENTSYNC_TARGET_ROOT
	LookPath   func(file string) (string, error)
}

// Adapter implements adapter.Adapter for Cline.
type Adapter struct{ opts Options }

// New constructs a Cline adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

func (a *Adapter) Name() string { return "cline" }

// KeyMergeStrategy is cline's single key-merge strategy: JSON (~/.cline/mcp.json).
// Rules/workflows are whole-file markdown writes.
func (a *Adapter) KeyMergeStrategy() string { return "merge-json-keys" }

func (a *Adapter) Capabilities() adapter.Capability {
	return adapter.CapMCP | adapter.CapMemory | adapter.CapCommand
	// Skill/Subagent/Hook/LSP omitted: no Cline concept (all ✗ skip).
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
	if _, err := lookPath("cline"); err == nil {
		return true, nil
	}
	return false, nil
}

// Render is implemented in render.go.
// Ingest is implemented in ingest.go.
// Apply is implemented in apply.go.
