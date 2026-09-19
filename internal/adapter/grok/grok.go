// Package grok implements the Grok Build adapter using its native TOML MCP
// configuration and Markdown/JSON component files. See docs/grok.md for the
// verified upstream contracts and intentional import/projection limits.
package grok

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// Options configure filesystem roots and the warning sink.
type Options struct {
	TargetRoot string
	GrokHome   string // optional absolute GROK_HOME override; user scope only
	Stderr     io.Writer
}

// Adapter implements Grok Build's native render and capture boundary.
type Adapter struct{ opts Options }

func New(opts Options) *Adapter {
	if opts.GrokHome != "" {
		opts.GrokHome = filepath.Clean(opts.GrokHome)
	}
	return &Adapter{opts: opts}
}

func (a *Adapter) Name() string { return "grok" }

func (a *Adapter) SetStderr(w io.Writer) { a.opts.Stderr = w }

func (a *Adapter) stderr() io.Writer {
	if a.opts.Stderr != nil {
		return a.opts.Stderr
	}
	return os.Stderr
}

func (a *Adapter) Detect() (bool, error) {
	if err := a.validateHome(); err != nil {
		return false, err
	}
	if info, err := os.Stat(a.resolvePaths(adapter.ScopeUser, "").ConfigDir); err == nil && info.IsDir() {
		return true, nil
	}
	_, err := exec.LookPath("grok")
	return err == nil, nil
}

// KeyMergeStrategy is the default for config.toml. Hooks use JSON; callers
// resolving cleanup formats must use adapter.MergeStrategyForPath.
func (a *Adapter) KeyMergeStrategy() string { return "merge-toml-keys" }

func (a *Adapter) KeyMergeStrategyForPath(path string) string {
	if filepath.Base(path) == "agentsync.json" && filepath.Base(filepath.Dir(path)) == "hooks" {
		return "merge-json-keys"
	}
	return a.KeyMergeStrategy()
}

// VersionRoots declares the user-scope config dir for destination git backup. An
// absolute GROK_HOME may legitimately live outside $HOME (that is what upstream
// provides it for) and is versioned there like any other root. validateHome
// refuses `/` and $HOME outright (every path errors, not just this one); a
// GROK_HOME that is some other ancestor of $HOME (`/home`, `/Users`) is declared
// here like any root and dropped — with a warning — by the apply tail's central
// never-at-or-above-$HOME guard (internal/cli enabledVersionRoots), which owns
// that invariant for every adapter (issue #270).
func (a *Adapter) VersionRoots(scope adapter.Scope, project string) []string {
	if scope != adapter.ScopeUser || a.validateHome() != nil {
		return nil
	}
	return []string{filepath.Clean(a.resolvePaths(scope, project).ConfigDir)}
}

var (
	_ adapter.Adapter         = (*Adapter)(nil)
	_ adapter.PathKeyMerger   = (*Adapter)(nil)
	_ adapter.HookIngestGuard = (*Adapter)(nil)
	_ adapter.MCPSpecIngester = (*Adapter)(nil)
)
