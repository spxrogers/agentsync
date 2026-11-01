// Package vscodelocal renders VS Code Local profile and project resources.
// It does not configure Copilot Agent Host, inline completions, models or BYOK.
package vscodelocal

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/generic"
	"github.com/spxrogers/agentsync/internal/paths"
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
func (a *Adapter) KeyMergeStrategy() string { return "merge-jsonc-keys" }

// VersionRoots implements adapter.VersionedDirs, always declaring NO roots.
// The selected VS Code profile is application-owned space; backing it up is the
// destination-directory Git backup's job, never a repo initialized by the
// apply tail inside the editor profile tree (CLAUDE.md invariant 1: the
// versioned-home sweep must never sweep toward $HOME or app homes).
func (a *Adapter) VersionRoots(scope adapter.Scope, project string) []string {
	return nil
}

// ResolveTarget uses the explicitly selected profile promptsHome. Never guess
// from a product name, enumerate other profiles, or share Copilot's home paths.
// A test redirect always wins over a real configured profile path.
func ResolveTarget(promptsHome, redirect string) (string, error) {
	if promptsHome == "" {
		return "", fmt.Errorf("vscode-local requires [vscode_local].prompts_home selected from the active VS Code profile")
	}
	resolved, err := (source.VSCodeLocalConfig{PromptsHome: promptsHome}).ResolvePromptsHome()
	if err != nil {
		return "", err
	}
	if redirect != "" {
		return filepath.Join(redirect, ".vscode-local", "prompts", FileName), nil
	}
	return filepath.Join(resolved, FileName), nil
}

func (a *Adapter) Render(r secrets.Resolved, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, nil, err
	}
	c := r.Canonical() //nolint:forbidigo // sanctioned adapter render egress
	if scope == adapter.ScopeProject && c.Project != nil {
		c = *c.Project
	}
	var skips []adapter.Skip
	unsupported := func(kind, name string) {
		skips = append(skips, adapter.Skip{Component: kind, Name: name, Reason: "vscode-local does not translate hooks or editor LSP configuration", Kind: adapter.SkipDropped})
	}
	for _, s := range c.Hooks {
		unsupported("hook", s.Event.String())
	}
	for _, s := range c.LSPServers {
		unsupported("lsp", s.ID)
	}
	if scope == adapter.ScopeUser && c.Config.VSCodeLocal.PromptsHome == "" && !c.Config.Agents[a.Name()].Enabled {
		unsupported("memory", a.Name())
		return nil, skips, nil
	}
	var profile string
	if scope == adapter.ScopeUser {
		target, err := ResolveTarget(c.Config.VSCodeLocal.PromptsHome, a.opts.RedirectRoot)
		if err != nil {
			return nil, nil, err
		}
		profile = filepath.Dir(target)
	}
	home := paths.HomeDir(paths.OSEnv{})
	if a.opts.RedirectRoot != "" {
		home = a.opts.RedirectRoot
	}
	ops, resourceSkips, err := generic.RenderVSCodeResources(c, a.Name(), home, profile, scope, project)
	if err != nil {
		return nil, nil, err
	}
	skips = append(skips, resourceSkips...)
	skillOps, skillSkips, err := generic.RenderVSCodeSkills(c, home, scope, project)
	if err != nil {
		return nil, nil, err
	}
	ops = append(ops, skillOps...)
	skips = append(skips, skillSkips...)
	mcpPath := filepath.Join(filepath.Dir(profile), "mcp.json")
	if scope == adapter.ScopeProject {
		mcpPath = filepath.Join(project, ".vscode", "mcp.json")
	} else if a.opts.RedirectRoot == "" {
		mcpPath, err = c.Config.VSCodeLocal.ResolveMCPFile()
		if err != nil {
			return nil, nil, err
		}
	}
	mcpOps, mcpSkips, err := generic.RenderVSCodeMCP(c, a.Name(), mcpPath)
	if err != nil {
		return nil, nil, err
	}
	ops = append(ops, mcpOps...)
	skips = append(skips, mcpSkips...)
	instructionOps, err := generic.RenderVSCodeInstructions(c.Memory.Fragments, a.Name(), home, profile, scope, project)
	if err != nil {
		return nil, nil, err
	}
	ops = append(ops, instructionOps...)
	if c.Memory.Body != "" {
		target := filepath.Join(profile, FileName)
		if scope == adapter.ScopeProject {
			target = filepath.Join(project, ".github", "instructions", FileName)
		}
		body := source.RenderManagedMemory(c.Memory.Body, c.Memory.Fragments, FileName, true)
		ops = append(ops, adapter.FileOp{Path: target, Content: []byte(Frontmatter + body), Mode: 0o644, SourceID: "memory/AGENTS.md", MergeStrategy: "replace"})
	}
	return ops, skips, nil
}

// Ingest fails explicitly: the profile binding is canonical configuration and
// cannot be inferred from this interface. Never capture unrelated profile files.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	return source.Canonical{}, fmt.Errorf("vscode-local import/write-back is unsupported; edit canonical resources and apply")
}

func (a *Adapter) IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	return generic.New(generic.Spec{MCP: generic.MCPTarget{RootKey: "servers", TransportKey: "type"}}, generic.Options{}).IngestMCPSpec(raw)
}

func (a *Adapter) Apply(ops []adapter.FileOp, w adapter.DestWriter) error {
	return adapter.DispatchOps(ops, w, func(op adapter.FileOp) error {
		if op.MergeStrategy == "merge-jsonc-keys" || strings.HasPrefix(filepath.ToSlash(op.SourceID), "skills/") {
			return generic.New(generic.Spec{}, generic.Options{}).Apply([]adapter.FileOp{op}, w)
		}
		// Refuse a same-named user file rather than claiming it by filename alone.
		data, present, err := adapter.ReadFileOptional(op.Path)
		if err != nil {
			return err
		}
		if present && !strings.Contains(string(data), "<!-- agentsync:managed memory-banner -->") {
			return fmt.Errorf("refusing to overwrite unmanaged instructions %s; choose another profile or back up and resolve the filename collision", op.Path)
		}
		return w.Write(op, op.Content)
	})
}

// No VersionRoots: never initialize Git amid a user's VS Code profile. The
// ordinary writer backs up changed managed content; deployment also snapshots it.
var _ adapter.Adapter = (*Adapter)(nil)
