// Package codex implements the Codex CLI adapter for agentsync.
//
// Codex stores user-level configuration under $CODEX_HOME (defaulting to
// ~/.codex). Unlike Claude (JSON) and OpenCode (JSONC), Codex's primary config
// file is TOML (config.toml), so MCP servers are projected as TOML tables and
// merged via the merge-toml-keys strategy (settings.go). Memory, skills,
// subagents, commands, and hooks land in their own files. See
// docs/capability-matrix.md for the per-component coverage and documented loss.
package codex

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

// Adapter implements adapter.Adapter (and adapter.PluginIngester) for Codex CLI.
type Adapter struct{ opts Options }

// New constructs a Codex adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

// stderr returns the configured warning sink, defaulting to os.Stderr.
func (a *Adapter) stderr() io.Writer {
	if a.opts.Stderr != nil {
		return a.opts.Stderr
	}
	return os.Stderr
}

func (a *Adapter) Name() string { return "codex" }

// KeyMergeStrategy is codex's single key-merge strategy: TOML (config.toml),
// merged via go-toml (not strict JSON). config.toml is codex's ONLY key-merge
// destination — both [mcp_servers.*] and [hooks.*] live there — so the single
// strategy the render pipeline uses for orphan-cleanup synthesis is correct for
// every key-merge path this adapter owns. (Hooks are written as inline
// config.toml tables rather than a separate hooks.json precisely so there is one
// strategy; see renderHooks.)
func (a *Adapter) KeyMergeStrategy() string { return "merge-toml-keys" }

func (a *Adapter) Capabilities() adapter.Capability {
	return adapter.CapMCP | adapter.CapMemory | adapter.CapSkill |
		adapter.CapSubagent | adapter.CapCommand | adapter.CapHook
	// LSP omitted: Codex has no LSP concept (shipped as ✗ skip).
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
	if _, err := lookPath("codex"); err == nil {
		return true, nil
	}
	return false, nil
}

// Render is implemented in render.go.
// Ingest is implemented in ingest.go.
// Apply is implemented in apply.go.
// IngestPlugins is implemented in ingest_plugins.go.
