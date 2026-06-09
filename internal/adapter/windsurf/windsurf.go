// Package windsurf implements the Windsurf (Cascade) adapter for agentsync.
//
// Windsurf's config model is asymmetric across scopes (per docs.windsurf.com,
// now docs.devin.ai):
//
//   - MCP servers live ONLY in the global ~/.codeium/windsurf/mcp_config.json
//     (a JSON `mcpServers` object). There is no project-level MCP file, so MCP is
//     rendered at user scope and skipped (with a report) at project scope.
//   - Rules (memory) and workflows (slash commands) live in the project tree
//     under .windsurf/rules/ and .windsurf/workflows/ as PLAIN markdown (no
//     frontmatter). Windsurf's global rules are app-managed, so memory/commands
//     are rendered at project scope and skipped (with a report) at user scope.
//
// Skills, subagents, hooks, and LSP have no Windsurf concept and are skipped.
// Because rules/workflows are plain markdown (no frontmatter parsing), the
// adapter emits no Ingest warnings and therefore does not implement WarnEmitter.
// See docs/capability-matrix.md for the per-component coverage and documented loss.
package windsurf

import (
	"os"
	"os/exec"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// Options configure the adapter at construction.
type Options struct {
	TargetRoot string // honors AGENTSYNC_TARGET_ROOT (real "/Users/x" in production)
	// LookPath overrides exec.LookPath for testing. nil means use exec.LookPath.
	LookPath func(file string) (string, error)
}

// Adapter implements adapter.Adapter for Windsurf.
type Adapter struct{ opts Options }

// New constructs a Windsurf adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

func (a *Adapter) Name() string { return "windsurf" }

// KeyMergeStrategy is windsurf's single key-merge strategy: JSON
// (mcp_config.json). It is the only file whose keys agentsync co-owns; rules and
// workflows are whole-file markdown writes.
func (a *Adapter) KeyMergeStrategy() string { return "merge-json-keys" }

func (a *Adapter) Capabilities() adapter.Capability {
	return adapter.CapMCP | adapter.CapMemory | adapter.CapCommand
	// Skill/Subagent/Hook/LSP omitted: no Windsurf concept (all ✗ skip).
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
	if _, err := lookPath("windsurf"); err == nil {
		return true, nil
	}
	return false, nil
}

// Render is implemented in render.go.
// Ingest is implemented in ingest.go.
// Apply is implemented in apply.go.
