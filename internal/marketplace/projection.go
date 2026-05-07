package marketplace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// ProjectionResult collects the canonical model entries derived from a plugin's
// components.
type ProjectionResult struct {
	MCPServers []source.MCPServer
	Skills     []source.Skill
	Subagents  []source.Subagent
	Commands   []source.Command
	Hooks      []source.Hook
	LSPServers []source.LSPServer
}

// Project loads the plugin's components and returns them as canonical entries.
//
// Strict mode (entry.Strict == nil || *entry.Strict):
//   - Reads cacheDir/.claude-plugin/plugin.json for the primary component list.
//   - Then merges in any component fields on the PluginEntry itself (overrides).
//
// Non-strict mode:
//   - Uses only the PluginEntry's inlined component fields.
//
// In both cases, ${CLAUDE_PLUGIN_ROOT} in command/url strings is replaced with
// cacheDir so non-Claude adapters can resolve binary paths.
func Project(entry PluginEntry, cacheDir string) (ProjectionResult, error) {
	return ProjectWithReader(entry, cacheDir, os.ReadFile)
}

// ProjectWithReader is like Project but uses a caller-supplied readFile function
// for loading plugin.json. This enables in-memory filesystem use in tests.
func ProjectWithReader(entry PluginEntry, cacheDir string, readFile func(string) ([]byte, error)) (ProjectionResult, error) {
	var pr ProjectionResult
	strict := entry.Strict == nil || *entry.Strict

	if strict {
		manifestPath := filepath.Join(cacheDir, ".claude-plugin", "plugin.json")
		data, err := readFile(manifestPath)
		if err != nil && !os.IsNotExist(err) {
			return pr, fmt.Errorf("read plugin.json: %w", err)
		}
		if err == nil {
			var manifest PluginManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				return pr, fmt.Errorf("parse plugin.json: %w", err)
			}
			applyManifest(manifest, &pr, cacheDir)
		}
		// Merge entry-level overrides on top of manifest components.
		applyEntryOverrides(entry, &pr, cacheDir)
	} else {
		applyEntryFull(entry, &pr, cacheDir)
	}
	return pr, nil
}

// resolvePluginRoot replaces the ${CLAUDE_PLUGIN_ROOT} placeholder with cacheDir.
func resolvePluginRoot(s, cacheDir string) string {
	return strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", cacheDir)
}

// resolvePluginRootInArgs applies resolvePluginRoot to each element of args.
func resolvePluginRootInArgs(args []string, cacheDir string) []string {
	if args == nil {
		return nil
	}
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = resolvePluginRoot(a, cacheDir)
	}
	return out
}

// applyManifest converts a PluginManifest into ProjectionResult entries.
func applyManifest(manifest PluginManifest, pr *ProjectionResult, cacheDir string) {
	for name, raw := range manifest.MCPServers {
		spec := parseMCPSpec(raw, cacheDir)
		pr.MCPServers = append(pr.MCPServers, source.MCPServer{ID: name, Server: spec})
	}
	for name, raw := range manifest.LSPServers {
		spec := parseLSPSpec(raw, cacheDir)
		pr.LSPServers = append(pr.LSPServers, source.LSPServer{ID: name, Spec: spec})
	}
	// Skills and commands from manifest are markdown paths; for now we record
	// them as stub entries so adapters can at least enumerate them.
	for _, sk := range toStringSlice(manifest.Skills) {
		pr.Skills = append(pr.Skills, source.Skill{Name: resolvePluginRoot(sk, cacheDir)})
	}
	for _, cmd := range toStringSlice(manifest.Commands) {
		pr.Commands = append(pr.Commands, source.Command{Name: resolvePluginRoot(cmd, cacheDir)})
	}
	for _, ag := range toStringSlice(manifest.Agents) {
		pr.Subagents = append(pr.Subagents, source.Subagent{Name: resolvePluginRoot(ag, cacheDir)})
	}
	// Hooks: may be a string command or an object with event+command shape.
	applyHooks(manifest.Hooks, pr, cacheDir)
}

// applyEntryOverrides merges component fields from a PluginEntry into an
// already-seeded ProjectionResult (strict mode overlay).
func applyEntryOverrides(entry PluginEntry, pr *ProjectionResult, cacheDir string) {
	for name, raw := range entry.MCPServers {
		spec := parseMCPSpec(raw, cacheDir)
		pr.MCPServers = append(pr.MCPServers, source.MCPServer{ID: name, Server: spec})
	}
	for name, raw := range entry.LSPServers {
		spec := parseLSPSpec(raw, cacheDir)
		pr.LSPServers = append(pr.LSPServers, source.LSPServer{ID: name, Spec: spec})
	}
	for _, sk := range toStringSlice(entry.Skills) {
		pr.Skills = append(pr.Skills, source.Skill{Name: resolvePluginRoot(sk, cacheDir)})
	}
	for _, cmd := range toStringSlice(entry.Commands) {
		pr.Commands = append(pr.Commands, source.Command{Name: resolvePluginRoot(cmd, cacheDir)})
	}
	for _, ag := range toStringSlice(entry.Agents) {
		pr.Subagents = append(pr.Subagents, source.Subagent{Name: resolvePluginRoot(ag, cacheDir)})
	}
	applyHooks(entry.Hooks, pr, cacheDir)
}

// applyEntryFull applies all component fields from a non-strict PluginEntry.
func applyEntryFull(entry PluginEntry, pr *ProjectionResult, cacheDir string) {
	applyEntryOverrides(entry, pr, cacheDir)
}

// applyHooks interprets the hooks field (string | []string | map) and appends
// Hook entries. Hook format from plugin.json can be:
//   - a plain command string → PreToolUse catch-all
//   - []string → each a PreToolUse catch-all
//   - map[event]command or map[event][]hookObj
func applyHooks(hooks any, pr *ProjectionResult, cacheDir string) {
	if hooks == nil {
		return
	}
	switch v := hooks.(type) {
	case string:
		pr.Hooks = append(pr.Hooks, source.Hook{
			Event:   "PreToolUse",
			Matcher: "*",
			Type:    "command",
			Command: resolvePluginRoot(v, cacheDir),
		})
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				pr.Hooks = append(pr.Hooks, source.Hook{
					Event:   "PreToolUse",
					Matcher: "*",
					Type:    "command",
					Command: resolvePluginRoot(s, cacheDir),
				})
			}
		}
	case map[string]any:
		for event, val := range v {
			switch ev := val.(type) {
			case string:
				pr.Hooks = append(pr.Hooks, source.Hook{
					Event:   event,
					Matcher: "*",
					Type:    "command",
					Command: resolvePluginRoot(ev, cacheDir),
				})
			case map[string]any:
				cmd, _ := ev["command"].(string)
				matcher, _ := ev["matcher"].(string)
				if matcher == "" {
					matcher = "*"
				}
				pr.Hooks = append(pr.Hooks, source.Hook{
					Event:   event,
					Matcher: matcher,
					Type:    "command",
					Command: resolvePluginRoot(cmd, cacheDir),
				})
			case []any:
				for _, item := range ev {
					if m, ok := item.(map[string]any); ok {
						cmd, _ := m["command"].(string)
						matcher, _ := m["matcher"].(string)
						if matcher == "" {
							matcher = "*"
						}
						pr.Hooks = append(pr.Hooks, source.Hook{
							Event:   event,
							Matcher: matcher,
							Type:    "command",
							Command: resolvePluginRoot(cmd, cacheDir),
						})
					}
				}
			}
		}
	}
}

// parseMCPSpec converts a raw map (from JSON) into a source.MCPServerSpec,
// resolving ${CLAUDE_PLUGIN_ROOT} in command and url fields.
func parseMCPSpec(raw any, cacheDir string) source.MCPServerSpec {
	m, ok := raw.(map[string]any)
	if !ok {
		return source.MCPServerSpec{}
	}
	spec := source.MCPServerSpec{}
	if t, ok := m["type"].(string); ok {
		spec.Type = t
	}
	if cmd, ok := m["command"].(string); ok {
		spec.Command = resolvePluginRoot(cmd, cacheDir)
	}
	if u, ok := m["url"].(string); ok {
		spec.URL = resolvePluginRoot(u, cacheDir)
	}
	if args, ok := m["args"].([]any); ok {
		for _, a := range args {
			if s, ok := a.(string); ok {
				spec.Args = append(spec.Args, resolvePluginRoot(s, cacheDir))
			}
		}
	}
	if env, ok := m["env"].(map[string]any); ok {
		spec.Env = make(map[string]string, len(env))
		for k, v := range env {
			if s, ok := v.(string); ok {
				spec.Env[k] = resolvePluginRoot(s, cacheDir)
			}
		}
	}
	if headers, ok := m["headers"].(map[string]any); ok {
		spec.Headers = make(map[string]string, len(headers))
		for k, v := range headers {
			if s, ok := v.(string); ok {
				spec.Headers[k] = resolvePluginRoot(s, cacheDir)
			}
		}
	}
	if agents, ok := m["agents"].([]any); ok {
		for _, a := range agents {
			if s, ok := a.(string); ok {
				spec.Agents = append(spec.Agents, s)
			}
		}
	}
	return spec
}

// parseLSPSpec converts a raw map into a source.LSPServerSpec.
func parseLSPSpec(raw any, cacheDir string) source.LSPServerSpec {
	m, ok := raw.(map[string]any)
	if !ok {
		return source.LSPServerSpec{}
	}
	spec := source.LSPServerSpec{}
	if cmd, ok := m["command"].(string); ok {
		spec.Command = resolvePluginRoot(cmd, cacheDir)
	}
	if u, ok := m["url"].(string); ok {
		spec.URL = resolvePluginRoot(u, cacheDir)
	}
	if args, ok := m["args"].([]any); ok {
		for _, a := range args {
			if s, ok := a.(string); ok {
				spec.Args = append(spec.Args, resolvePluginRoot(s, cacheDir))
			}
		}
	}
	if env, ok := m["env"].(map[string]any); ok {
		spec.Env = make(map[string]string, len(env))
		for k, v := range env {
			if s, ok := v.(string); ok {
				spec.Env[k] = resolvePluginRoot(s, cacheDir)
			}
		}
	}
	if headers, ok := m["headers"].(map[string]any); ok {
		spec.Headers = make(map[string]string, len(headers))
		for k, v := range headers {
			if s, ok := v.(string); ok {
				spec.Headers[k] = resolvePluginRoot(s, cacheDir)
			}
		}
	}
	return spec
}

// toStringSlice normalises the Skills/Commands/Agents fields (string | []string | []any).
func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case string:
		return []string{t}
	case []string:
		return t
	case []any:
		var out []string
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
