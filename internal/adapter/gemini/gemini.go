// Package gemini implements the Gemini CLI adapter for agentsync.
//
// Gemini CLI stores user-level configuration under ~/.gemini and project-level
// under <repo>/.gemini (per geminicli.com/docs). Its settings file is JSON
// (settings.json) and holds BOTH MCP servers (the `mcpServers` object — the same
// idea as Claude) and lifecycle hooks (the `hooks` object — the same nested
// matcher-group/hooks-array shape Claude uses), so the adapter has a single
// key-merge strategy (merge-json-keys) and co-owns two sections of one file,
// exactly as Claude co-owns `hooks`/`lspServers` inside its settings.json.
//
// The remaining components are files: memory → GEMINI.md (the hierarchical
// context file: `~/.gemini/GEMINI.md` at user scope, repo-root `GEMINI.md` at
// project scope), slash commands → `.gemini/commands/<name>.toml` (TOML with
// `description` + `prompt`), and subagents → `.gemini/agents/<name>.md` (markdown
// with YAML frontmatter). Gemini CLI has no Agent Skills concept (it uses
// extensions) and no LSP config, so both are skipped. See
// docs/capability-matrix.md for the per-component coverage and documented loss.
package gemini

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

// Adapter implements adapter.Adapter for Gemini CLI.
type Adapter struct{ opts Options }

// New constructs a Gemini CLI adapter.
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

func (a *Adapter) Name() string { return "gemini" }

// KeyMergeStrategy is gemini's single key-merge strategy: JSON (settings.json).
// settings.json is gemini's ONLY key-merge destination — both `mcpServers` and
// `hooks` live there — so the single strategy the render pipeline uses for
// orphan-cleanup synthesis is correct for every key-merge path this adapter owns.
func (a *Adapter) KeyMergeStrategy() string { return "merge-jsonc-keys" }

func (a *Adapter) Capabilities() adapter.Capability {
	return adapter.CapMCP | adapter.CapMemory | adapter.CapSubagent |
		adapter.CapCommand | adapter.CapHook
	// Skill omitted: Gemini CLI has no Agent Skills concept (uses extensions).
	// LSP omitted: Gemini CLI has no LSP configuration concept. Both ✗ skip.
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
	if _, err := lookPath("gemini"); err == nil {
		return true, nil
	}
	return false, nil
}

// Render is implemented in render.go.
// Ingest is implemented in ingest.go.
// Apply is implemented in apply.go.
