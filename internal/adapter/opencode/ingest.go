package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/tailscale/hujson"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// Ingest reads OpenCode's native config files and returns a partial
// source.Canonical. It is the inverse of Render.
//
// Round-trip notes:
//   - Subagent `mode` (primary/all/subagent) is PRESERVED into the canonical
//     frontmatter. The canonical `source.Subagent.Frontmatter` is a free-form
//     map, so an OpenCode-specific key round-trips without a schema change; the
//     render side re-emits an ingested `mode` verbatim, so a native primary/all
//     agent is no longer demoted to subagent on the next apply.
//   - Subagents still lose the Claude-side `tools`/`color` fields (dropped on
//     render because OpenCode has no equivalent). Ingest reconstructs only what
//     is present on disk.
//   - Only agentsync-OWNED agents/commands are captured (see destOwned): the
//     agents/ and commands/ directories are shared with the user's own
//     hand-authored files, which are left untouched and never pulled into the
//     canonical source.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP from opencode.json /mcp/<id>
	if data, err := os.ReadFile(p.Settings); err == nil {
		parsed, parseErr := hujson.Parse(data)
		if parseErr == nil {
			parsed.Standardize()
			var top map[string]any
			if json.Unmarshal(parsed.Pack(), &top) == nil {
				if servers, ok := top["mcp"].(map[string]any); ok {
					for id, raw := range servers {
						spec, ok := raw.(map[string]any)
						if !ok {
							continue
						}
						m := source.MCPServer{ID: id, Server: IngestMCPSpec(spec)}
						c.MCPServers = append(c.MCPServers, m)
					}
				}
			}
		}
	}

	warn := a.stderr()

	// Skills from ~/.claude/skills/<name>/ (SKILL.md + bundled files; shared with Claude)
	if entries, err := os.ReadDir(p.ClaudeSkillsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillDir := filepath.Join(p.ClaudeSkillsDir, e.Name())
			data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
			if err != nil {
				continue
			}
			fm, body, lenient, err := claude.ParseFrontmatterWithReport(data)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping skill %q: %v\n", e.Name(), err)
				continue
			}
			if lenient {
				fmt.Fprintf(warn, "warning: skill %q frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')\n", e.Name())
			}
			files, err := source.ReadSkillFiles(afero.NewOsFs(), skillDir)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping skill %q: read bundled files: %v\n", e.Name(), err)
				continue
			}
			c.Skills = append(c.Skills, source.Skill{Name: e.Name(), Frontmatter: fm, Body: body, Files: files})
		}
	}

	// Ownership: the agents/ and commands/ dirs are shared with the user's own
	// hand-authored files (and agents/ with OpenCode's primary-agent workflow),
	// so ingest must capture ONLY the files agentsync actually wrote — never pull
	// unmanaged user config into the canonical source. Ownership comes from the
	// apply state (see destOwned); a missing state owns nothing, and a corrupt one
	// is reported and treated as owning nothing rather than risking over-capture.
	statePath := filepath.Join(a.opts.TargetRoot, ".agentsync", ".state", "targets.json")
	ownState, stErr := state.Load(statePath)
	if stErr != nil {
		fmt.Fprintf(warn, "warning: opencode ingest could not read apply state (%v); skipping agent/command capture\n", stErr)
		ownState = state.New()
	}

	// Subagents from <AgentsDir>/<name>.md — frontmatter munged back to canonical:
	// preserve `mode` (primary/all/subagent), retain `description` + `model`.
	if entries, err := os.ReadDir(p.AgentsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			destPath := filepath.Join(p.AgentsDir, e.Name())
			if !a.destOwned(ownState, scope, project, destPath) {
				continue // hand-authored / agentsync-unmanaged file — leave it alone
			}
			data, err := os.ReadFile(destPath)
			if err != nil {
				continue
			}
			name := e.Name()[:len(e.Name())-len(".md")]
			fm, body, lenient, err := claude.ParseFrontmatterWithReport(data)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping subagent %q: %v\n", name, err)
				continue
			}
			if lenient {
				fmt.Fprintf(warn, "warning: subagent %q frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')\n", name)
			}
			// `mode` is retained: it is a legal OpenCode agent key and the
			// canonical frontmatter is free-form, so it round-trips through render.
			c.Subagents = append(c.Subagents, source.Subagent{Name: name, Frontmatter: fm, Body: body})
		}
	}

	// Commands from <CommandsDir>/<name>.md — owned files only.
	if entries, err := os.ReadDir(p.CommandsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			destPath := filepath.Join(p.CommandsDir, e.Name())
			if !a.destOwned(ownState, scope, project, destPath) {
				continue // hand-authored / agentsync-unmanaged file — leave it alone
			}
			data, err := os.ReadFile(destPath)
			if err != nil {
				continue
			}
			name := e.Name()[:len(e.Name())-len(".md")]
			fm, body, lenient, err := claude.ParseFrontmatterWithReport(data)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping command %q: %v\n", name, err)
				continue
			}
			if lenient {
				fmt.Fprintf(warn, "warning: command %q frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')\n", name)
			}
			c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: fm, Body: body})
		}
	}

	// Memory from AGENTS.md (managed-file banner stripped — see claude/ingest.go)
	if data, err := os.ReadFile(p.Memory); err == nil {
		c.Memory.Body = source.StripManagedBanner(string(data))
	}

	return c, nil
}

// destOwned reports whether the apply state records destPath as an
// agentsync-owned file for this adapter at (scope, project). Ingest consults it
// so it captures only agentsync-managed agents/commands from the shared
// ~/.config/opencode/{agents,commands}/ directories, never a user's
// hand-authored files sitting alongside them.
//
// The state key mirrors render.RecordOpsState exactly —
// "<agent>:<scope>:<home-rel-project>:<home-rel-path>" — so a file apply wrote
// is recognized here. Home-relativization uses the adapter's configured
// TargetRoot, the SAME base ResolvePaths uses for every other path, so the read
// side agrees with the write side in production (the CLI registry constructs
// every adapter with TargetRoot = paths.HomeDir, the same value apply feeds
// RecordOpsState).
//
// Scope note: filtering ingest by ownership is currently OpenCode-only — no
// other deep adapter filters its Ingest yet (a known class-wide gap tracked for
// follow-up; reconcile is unaffected because it classifies drift via the render
// plan + state and never calls Ingest). The state path is derived from
// TargetRoot and assumes the default AGENTSYNC_HOME layout
// (<target-root>/.agentsync); an AGENTSYNC_HOME relocated outside TargetRoot is
// not honored here.
func (a *Adapter) destOwned(st *state.Targets, scope adapter.Scope, project, destPath string) bool {
	home := a.opts.TargetRoot
	key := fmt.Sprintf("opencode:%s:%s:%s", scope.String(),
		paths.HomeRelative(home, project), paths.HomeRelative(home, destPath))
	_, ok := st.Files[key]
	return ok
}

func asStr(v any) string { s, _ := v.(string); return s }

// IngestMCPSpec translates one OpenCode-native MCP server spec — the value
// under opencode.json `/mcp/<id>` — into the canonical MCPServerSpec. It is the
// exact inverse of opencodeMCPSpec (Render). Exported so reconcile's key-level
// write-back reconstructs an opencode MCP server through the SAME translation
// the adapter uses, rather than assuming the dest shape matches the canonical
// model (it doesn't: command is an array, env is `environment`, type is
// local/remote).
func IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	command, args := opencodeIngestCommand(raw["command"])
	return source.MCPServerSpec{
		Type:    canonicalMCPType(asStr(raw["type"])),
		Command: command,
		Args:    args,
		Env:     asStrMap(raw["environment"]),
		URL:     asStr(raw["url"]),
		Headers: asStrMap(raw["headers"]),
		Extra:   claude.ExtraNativeKeys(raw, "type", "command", "environment", "url", "headers", "enabled"),
	}
}

// canonicalMCPType maps OpenCode's transport ("local"/"remote") back to the
// canonical model's stdio/http. OpenCode has no separate sse transport, so a
// remote server normalises to "http" on write-back (an apply-only flow is
// unaffected — render maps both http and sse to "remote"). An unrecognised
// value is preserved verbatim.
func canonicalMCPType(opencodeType string) string {
	switch opencodeType {
	case "local":
		return "stdio"
	case "remote":
		return "http"
	default:
		return opencodeType
	}
}

// opencodeIngestCommand splits OpenCode's command — a string array
// [command, ...args] — into the canonical command + args. A bare string is
// tolerated for hand-edited configs. A single-element array yields no args so
// the value round-trips cleanly (render flattens command+nil-args back to it).
func opencodeIngestCommand(v any) (command string, args []string) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	arr := asStrSlice(v)
	switch len(arr) {
	case 0:
		return "", nil
	case 1:
		return arr[0], nil
	default:
		return arr[0], arr[1:]
	}
}

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
