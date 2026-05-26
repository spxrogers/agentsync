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

// Adapter implements adapter.Adapter (and adapter.PluginIngester) for Codex CLI.
type Adapter struct{ opts Options }

// New constructs a Codex adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

func (a *Adapter) Name() string { return "codex" }

// KeyMergeStrategy is codex's single key-merge strategy: TOML (config.toml),
// which MUST be merged via go-toml, not strict JSON. Hooks land in a separate
// hooks.json merged with the JSON strategy at apply time, but config.toml is
// the file the render pipeline synthesizes orphan-cleanup ops for, so the
// adapter's single strategy is the TOML one.
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
