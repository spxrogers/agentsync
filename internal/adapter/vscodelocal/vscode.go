// Package vscodelocal renders profile-scoped VS Code Local harness instructions.
// It does not configure Copilot Agent Host, inline completions, models or BYOK.
package vscodelocal

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

const (
	FileName    = "agentsync.instructions.md"
	Frontmatter = "---\napplyTo: \"**\"\n---\n\n"
)

type (
	Options struct{ RedirectRoot string }
	Adapter struct{ opts Options }
)

func New(opts Options) *Adapter             { return &Adapter{opts: opts} }
func (a *Adapter) Name() string             { return "vscode-local" }
func (a *Adapter) Detect() (bool, error)    { _, err := exec.LookPath("code"); return err == nil, nil }
func (a *Adapter) KeyMergeStrategy() string { return "" }

// ResolveTarget uses the explicitly selected profile promptsHome. Never guess
// from a product name, enumerate other profiles, or share Copilot's home paths.
// A test redirect always wins over a real configured profile path.
func ResolveTarget(promptsHome, redirect string) (string, error) {
	if promptsHome == "" {
		return "", fmt.Errorf("vscode-local requires [vscode_local].prompts_home selected from the active VS Code profile")
	}
	if !filepath.IsAbs(promptsHome) {
		return "", fmt.Errorf("vscode_local.prompts_home must be absolute")
	}
	if redirect != "" {
		return filepath.Join(redirect, ".vscode-local", "prompts", FileName), nil
	}
	return filepath.Join(filepath.Clean(promptsHome), FileName), nil
}

func (a *Adapter) Render(r secrets.Resolved, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, nil, err
	}
	c := r.Canonical() //nolint:forbidigo // sanctioned adapter render egress
	if scope != adapter.ScopeUser {
		return nil, []adapter.Skip{{Component: "memory", Name: a.Name(), Reason: "vscode-local supports selected user profile only", Kind: adapter.SkipDropped}}, nil
	}
	var skips []adapter.Skip
	unsupported := func(kind, name string) {
		skips = append(skips, adapter.Skip{Component: kind, Name: name, Reason: "vscode-local manages instructions only; use the existing agent target for this component", Kind: adapter.SkipDropped})
	}
	for _, s := range c.Skills {
		unsupported("skill", s.Name)
	}
	for _, s := range c.MCPServers {
		unsupported("mcp", s.ID)
	}
	for _, s := range c.Subagents {
		unsupported("subagent", s.Name)
	}
	for _, s := range c.Commands {
		unsupported("command", s.Name)
	}
	for _, s := range c.Hooks {
		unsupported("hook", s.Event.String())
	}
	for _, s := range c.LSPServers {
		unsupported("lsp", s.ID)
	}
	if c.Memory.Body == "" {
		return nil, skips, nil
	}
	if c.Config.VSCodeLocal.PromptsHome == "" && !c.Config.Agents[a.Name()].Enabled {
		unsupported("memory", a.Name())
		return nil, skips, nil
	}
	target, err := ResolveTarget(c.Config.VSCodeLocal.PromptsHome, a.opts.RedirectRoot)
	if err != nil {
		return nil, nil, err
	}
	body := source.RenderManagedMemory(c.Memory.Body, c.Memory.Fragments, FileName, true)
	return []adapter.FileOp{{Action: adapter.ActionWrite, Path: target, Content: []byte(Frontmatter + body), Mode: 0o644, SourceID: "memory/AGENTS.md", MergeStrategy: "replace"}}, skips, nil
}

// Ingest fails explicitly: the profile binding is canonical configuration and
// cannot be inferred from this interface. Never capture unrelated profile files.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	return source.Canonical{}, fmt.Errorf("vscode-local import/write-back is unsupported; edit canonical memory and apply")
}

func (a *Adapter) Apply(ops []adapter.FileOp, w adapter.DestWriter) error {
	return adapter.DispatchOps(ops, w, func(op adapter.FileOp) error {
		// Refuse a same-named user file rather than claiming it by filename alone.
		data, present, err := adapter.ReadFileOptional(op.Path)
		if err != nil {
			return err
		}
		if present && (!strings.HasPrefix(string(data), Frontmatter) || !strings.Contains(string(data), "<!-- agentsync:managed memory-banner -->")) {
			return fmt.Errorf("refusing to overwrite unmanaged instructions %s; choose another profile or back up and resolve the filename collision", op.Path)
		}
		return w.Write(op, op.Content)
	})
}

// No VersionRoots: never initialize Git amid a user's VS Code profile. The
// ordinary writer backs up changed managed content; deployment also snapshots it.
var _ adapter.Adapter = (*Adapter)(nil)
