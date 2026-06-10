// Package windsurf implements the Windsurf (Cascade) adapter for agentsync.
//
// Windsurf's config model is asymmetric across scopes (per docs.windsurf.com,
// now docs.devin.ai):
//
//   - MCP servers live ONLY in the global ~/.codeium/windsurf/mcp_config.json
//     (a JSON `mcpServers` object). There is no project-level MCP file, so MCP is
//     rendered at user scope and skipped (with a report) at project scope.
//   - Rules (memory): project scope renders a workspace rule at
//     .windsurf/rules/agentsync.md carrying the documented `trigger: always_on`
//     activation frontmatter; user scope renders the single global rules file
//     ~/.codeium/windsurf/memories/global_rules.md (always-on, frontmatter-less).
//   - Workflows (slash commands) are plain markdown at BOTH scopes:
//     .windsurf/workflows/ (project) and ~/.codeium/windsurf/global_workflows/
//     (user).
//
// (Upstream now documents `.devin/rules|workflows/` as the preferred workspace
// layout with `.windsurf/` as the supported fallback; agentsync targets
// `.windsurf/`, which every released version reads.)
//
// Skills, subagents, hooks, and LSP have no Windsurf concept and are skipped.
// See docs/capability-matrix.md for the per-component coverage and documented loss.
package windsurf

import (
	"io"
	"os"
	"os/exec"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// Options configure the adapter at construction.
type Options struct {
	TargetRoot string // honors AGENTSYNC_TARGET_ROOT (real "/Users/x" in production)
	// Stderr is the warning sink for Ingest; nil means os.Stderr.
	Stderr io.Writer
	// LookPath overrides exec.LookPath for testing. nil means use exec.LookPath.
	LookPath func(file string) (string, error)
}

// Adapter implements adapter.Adapter for Windsurf.
type Adapter struct{ opts Options }

// New constructs a Windsurf adapter.
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
