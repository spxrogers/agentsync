// Package cursor implements the Cursor adapter for agentsync.
//
// Cursor stores its configuration under a `.cursor/` directory — user-level at
// ~/.cursor and project-level at <repo>/.cursor (per cursor.com/docs). Unlike
// Claude (which mixes many concerns into settings.json) Cursor uses dedicated
// JSON files per concern: MCP servers live in `.cursor/mcp.json` (the same
// `mcpServers` object shape Claude uses) and hooks in `.cursor/hooks.json`
// (a `{ "version": 1, "hooks": { … } }` document). Both are JSON, so the
// adapter has a single key-merge strategy (merge-json-keys), exactly as Claude
// owns keys in two JSON files (.claude.json + settings.json).
//
// The remaining components are files: memory → AGENTS.md (project scope only —
// Cursor keeps user-level rules in app-local storage), skills → the shared
// `.cursor/skills/<name>/` directory, subagents → `.cursor/agents/<name>.md`,
// and slash commands → `.cursor/commands/<name>.md`. See
// docs/capability-matrix.md for the per-component coverage and documented loss.
package cursor

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

// Adapter implements adapter.Adapter for Cursor.
type Adapter struct{ opts Options }

// New constructs a Cursor adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

// stderr returns the configured warning sink, defaulting to os.Stderr.
func (a *Adapter) stderr() io.Writer {
	if a.opts.Stderr != nil {
		return a.opts.Stderr
	}
	return os.Stderr
}

// SetStderr replaces the warning sink the adapter writes Ingest warnings to,
// so a CLI command can route adapter warnings through the same styled writer
// it uses for its own output. See claude.Adapter.SetStderr for the contract.
func (a *Adapter) SetStderr(w io.Writer) { a.opts.Stderr = w }

func (a *Adapter) Name() string { return "cursor" }

// KeyMergeStrategy is cursor's single key-merge strategy: JSON. Both
// `.cursor/mcp.json` and `.cursor/hooks.json` are JSON key-merge destinations
// (the same strategy Claude uses for .claude.json + settings.json), so the
// single strategy the render pipeline uses for orphan-cleanup synthesis is
// correct for every key-merge path this adapter owns.
func (a *Adapter) KeyMergeStrategy() string { return "merge-json-keys" }

func (a *Adapter) Capabilities() adapter.Capability {
	return adapter.CapMCP | adapter.CapMemory | adapter.CapSkill |
		adapter.CapSubagent | adapter.CapCommand | adapter.CapHook
	// LSP omitted: Cursor has no LSP configuration concept (shipped as ✗ skip).
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
	if _, err := lookPath("cursor"); err == nil {
		return true, nil
	}
	return false, nil
}

// Render is implemented in render.go.
// Ingest is implemented in ingest.go.
// Apply is implemented in apply.go.
