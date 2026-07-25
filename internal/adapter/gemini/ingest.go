package gemini

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
	"github.com/spxrogers/agentsync/internal/source"
)

// walkCommandsDir is filepath.WalkDir behind a seam: the mid-walk error branch
// (an unreadable subdirectory reaching the callback's err argument) cannot be
// planted deterministically in the privileged test container, so the loud-
// return contract at the walkErr check is pinned by a test overriding this
// var to inject the error. Production never reassigns it.
var walkCommandsDir = filepath.WalkDir

// Ingest reads Gemini CLI's native config files and returns a partial
// source.Canonical. It is the inverse of Render (modulo the documented projected
// loss — subagent tools/color, command argument-hint/allowed-tools — which Render
// drops with a reported Skip).
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical
	warn := a.stderr()

	// MCP (mcpServers) and hooks (hooks) both live in settings.json — parse
	// once. Gemini CLI reads settings.json as JSONC (comments are a documented,
	// supported state of the file), so ingest must tolerate them too;
	// DecodeJSONC also preserves json.Number so large foreign integers survive
	// a later re-render unrounded.
	if data, present, err := adapter.ReadFileOptional(p.Settings); err != nil {
		return c, fmt.Errorf("read %s: %w", p.Settings, err)
	} else if present {
		top, err := jsonkeys.DecodeJSONC(data)
		if err != nil {
			return c, fmt.Errorf("parse %s: %w", p.Settings, err)
		}
		if servers, ok := top["mcpServers"].(map[string]any); ok {
			for id, raw := range servers {
				spec, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				c.MCPServers = append(c.MCPServers, source.MCPServer{ID: id, Server: IngestMCPSpec(spec)})
			}
		}
		// Refused events surface via RefusedHookEvents (adapter.HookIngestGuard):
		// import uses that to retire a stale canonical hooks/<event>.toml.
		hooks, _ := ingestHooks(top["hooks"], warn)
		c.Hooks = append(c.Hooks, hooks...)
	}

	// Commands from .gemini/commands/**/<name>.toml (TOML → description + body).
	// Gemini namespaces commands via subdirectories: commands/git/commit.toml is
	// the `/git:commit` command (the on-disk path separator becomes the ':'
	// invocation separator). Walk recursively and encode the subdir path into
	// Command.Name as a forward-slash relpath (e.g. "git/commit"); renderCommands
	// inverts it (filepath.FromSlash) back to the subdirectory layout so the
	// native→native round-trip is stable.
	//
	// Error discipline (#159, cf. adapter.ReadDirOptional's doc): only an ABSENT
	// commands dir (os.IsNotExist) is a silent skip. Every structural error —
	// an unstat-able root, a file sitting at the dir path, a mid-walk failure
	// such as an unreadable subdirectory — RETURNS loudly: a demoted warning
	// would hand back nil error with a silently short Commands slice, which
	// drift/capture could misread as "the user deleted those commands". Only
	// genuinely per-entry read/parse failures on an individual .toml file warn
	// and continue (one corrupt file must not hide every other command),
	// matching the sibling adapters' per-entry handling. The root is checked
	// with os.Stat rather than ReadDirOptional so the directory is listed once
	// (by WalkDir), not twice.
	switch fi, serr := os.Stat(p.CommandsDir); {
	case serr != nil && os.IsNotExist(serr):
		// Absent commands dir — a clean no-op.
	case serr != nil:
		return c, fmt.Errorf("read commands dir %s: %w", p.CommandsDir, serr)
	case !fi.IsDir():
		return c, fmt.Errorf("read commands dir %s: not a directory", p.CommandsDir)
	default:
		walkErr := walkCommandsDir(p.CommandsDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || filepath.Ext(d.Name()) != ".toml" {
				return nil
			}
			rel, err := filepath.Rel(p.CommandsDir, path)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(strings.TrimSuffix(rel, ".toml"))
			data, err := os.ReadFile(path)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping command %q: %v\n", name, err)
				return nil
			}
			var cf geminiCommandFile
			if err := toml.Unmarshal(data, &cf); err != nil {
				fmt.Fprintf(warn, "warning: skipping command %q: %v\n", name, err)
				return nil
			}
			fm := map[string]any{}
			if cf.Description != "" {
				fm["description"] = cf.Description
			}
			c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: fm, Body: cf.Prompt})
			return nil
		})
		if walkErr != nil {
			return c, fmt.Errorf("read commands dir %s: %w", p.CommandsDir, walkErr)
		}
	}

	// Subagents from .gemini/agents/<name>.md (markdown → frontmatter + body).
	agentEntries, present, err := adapter.ReadDirOptional(p.AgentsDir)
	if err != nil {
		return c, fmt.Errorf("read agents dir %s: %w", p.AgentsDir, err)
	}
	if present {
		for _, e := range agentEntries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			name := e.Name()[:len(e.Name())-len(".md")]
			data, err := os.ReadFile(filepath.Join(p.AgentsDir, e.Name()))
			if err != nil {
				if !os.IsNotExist(err) {
					fmt.Fprintf(warn, "warning: skipping subagent %q: read: %v\n", name, err)
				}
				continue
			}
			fm, body, lenient, err := claude.ParseFrontmatterWithReport(data)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping subagent %q: %v\n", name, err)
				continue
			}
			if lenient {
				fmt.Fprintf(warn, "warning: subagent %q frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')\n", name)
			}
			c.Subagents = append(c.Subagents, source.Subagent{Name: name, Frontmatter: fm, Body: body})
		}
	}

	// Memory from GEMINI.md (managed-file banner stripped — see claude/ingest.go).
	if data, present, err := adapter.ReadFileOptional(p.Memory); err != nil {
		return c, fmt.Errorf("read memory %s: %w", p.Memory, err)
	} else if present {
		c.Memory.Body = source.StripManagedBanner(string(data))
	}

	return c, nil
}

func asStr(v any) string { s, _ := v.(string); return s }

func asStrSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asStrMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}
