// Package marketplace models the Claude marketplace plugin format and provides
// the projection layer that decomposes plugin manifests into canonical source
// model entries. Skills, commands, and subagents are fully loaded from their
// on-disk markdown files (frontmatter + body + clean Name) via the injected
// readFile function so that adapters downstream receive complete, render-ready
// entries.
package marketplace

import (
	"encoding/json"
	"fmt"
	"log/slog"
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

// Project loads the plugin's components and returns them as canonical entries:
//   - Reads cacheDir/.claude-plugin/plugin.json (when present) for the primary
//     component list; a missing plugin.json is fine (a curator-defined plugin
//     may declare everything in the entry).
//   - If the manifest lists no skills, falls back to convention-based discovery:
//     scans cacheDir/skills/*/ for SKILL.md files.
//   - Then overlays any component fields on the PluginEntry itself; an inline
//     override of a same-named component wins (last-write at render).
//
// This is a UNION: plugin.json PLUS entry additions/overrides. The entry.Strict
// flag no longer changes projection — it used to make "non-strict" IGNORE
// plugin.json entirely, which silently dropped a plugin's own components
// whenever the marketplace entry was non-strict (and after an upstream
// strict-flip). Union semantics never drop the plugin's declared components.
//
// ${CLAUDE_PLUGIN_ROOT} in command/url strings is replaced with cacheDir so
// non-Claude adapters can resolve binary paths.
func Project(entry PluginEntry, cacheDir string) (ProjectionResult, error) {
	return projectWithFuncs(entry, cacheDir, os.ReadFile, os.ReadDir)
}

// ProjectWithReader is like Project but uses a caller-supplied readFile function
// for loading plugin.json and component markdown files. This enables in-memory
// filesystem use in tests. Convention-based discovery (skills/ directory scan)
// is disabled when using ProjectWithReader; use Project for full behavior.
func ProjectWithReader(entry PluginEntry, cacheDir string, readFile func(string) ([]byte, error)) (ProjectionResult, error) {
	return projectWithFuncs(entry, cacheDir, readFile, nil)
}

// projectWithFuncs is the internal implementation shared by Project and ProjectWithReader.
// listDir may be nil to disable convention-based skills discovery.
func projectWithFuncs(entry PluginEntry, cacheDir string, readFile func(string) ([]byte, error), listDir func(string) ([]os.DirEntry, error)) (ProjectionResult, error) {
	var pr ProjectionResult

	// Always honour plugin.json when present (a missing one is fine for a
	// curator-defined plugin), then overlay the entry's component config. Union
	// semantics — plugin.json PLUS entry additions/overrides — regardless of the
	// Strict flag, so a non-strict entry never drops the plugin's own components.
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
		if err := applyManifest(manifest, &pr, cacheDir, readFile, listDir); err != nil {
			return pr, err
		}
	}
	if err := applyEntryOverrides(entry, &pr, cacheDir, readFile); err != nil {
		return pr, err
	}
	dedupProjection(&pr)
	return pr, nil
}

// dedupProjection collapses same-identity components that the union (plugin.json
// PLUS entry overrides) can produce. Without it, an entry that overrides a
// same-named plugin.json skill/subagent/command yields TWO canonical entries
// that render to the SAME dest path — and apply's cross-agent divergence guard
// then aborts the whole run rather than letting the override win. MCP/LSP dups
// only ever last-win at the render map, but deduping here keeps the component
// count honest too.
//
// Precedence matches the documented "inline override wins" rule: name/id-keyed
// components keep the LAST occurrence (the entry is applied after plugin.json),
// while hooks — which have no override key — are deduped only on EXACT content,
// so two genuinely-distinct hooks for the same event both survive.
func dedupProjection(pr *ProjectionResult) {
	pr.MCPServers = dedupLastWins(pr.MCPServers, func(s source.MCPServer) string { return s.ID })
	pr.LSPServers = dedupLastWins(pr.LSPServers, func(s source.LSPServer) string { return s.ID })
	pr.Skills = dedupLastWins(pr.Skills, func(s source.Skill) string { return s.Name })
	pr.Subagents = dedupLastWins(pr.Subagents, func(s source.Subagent) string { return s.Name })
	pr.Commands = dedupLastWins(pr.Commands, func(c source.Command) string { return c.Name })
	pr.Hooks = dedupHooks(pr.Hooks)
}

// dedupLastWins returns items with same-key duplicates removed, keeping the last
// occurrence of each key and preserving the order in which those last
// occurrences appear.
func dedupLastWins[T any](items []T, key func(T) string) []T {
	if len(items) < 2 {
		return items
	}
	lastIdx := make(map[string]int, len(items))
	for i, it := range items {
		lastIdx[key(it)] = i
	}
	out := make([]T, 0, len(items))
	for i, it := range items {
		if lastIdx[key(it)] == i {
			out = append(out, it)
		}
	}
	return out
}

// dedupHooks removes exact-duplicate hooks, preserving order. Hooks have no
// override key, so only byte-identical entries are collapsed.
func dedupHooks(hooks []source.Hook) []source.Hook {
	if len(hooks) < 2 {
		return hooks
	}
	seen := make(map[source.Hook]bool, len(hooks))
	out := make([]source.Hook, 0, len(hooks))
	for _, h := range hooks {
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// resolvePluginRoot replaces the ${CLAUDE_PLUGIN_ROOT} placeholder with cacheDir.
// Used for command/arg/url substitution where we want only env-style expansion,
// not filesystem path joining.
func resolvePluginRoot(s, cacheDir string) string {
	return strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", cacheDir)
}

// resolveComponentPath resolves a manifest-listed component path (skill,
// subagent, command) against cacheDir. Resolution order:
//  1. ${CLAUDE_PLUGIN_ROOT} substitution if present (the literal placeholder
//     wins; result is treated as already-rooted).
//  2. Absolute paths returned as-is.
//  3. Relative paths joined to cacheDir.
//
// This separates "make a command portable" semantics (resolvePluginRoot) from
// "find a file inside the plugin cache" semantics (this function), which the
// Claude marketplace plugin convention conflates: manifest entries like
// "./skills/foo" are relative to the plugin root, not the process cwd.
func resolveComponentPath(s, cacheDir string) (string, error) {
	var resolved string
	switch {
	case strings.Contains(s, "${CLAUDE_PLUGIN_ROOT}"):
		resolved = strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", cacheDir)
	case filepath.IsAbs(s):
		resolved = s
	default:
		resolved = filepath.Join(cacheDir, s)
	}
	if err := assertWithinCache(cacheDir, resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// assertWithinCache rejects a resolved component path that escapes cacheDir.
// The manifest *contents* are untrusted: a hostile plugin.json listing
// "skills":["/etc/passwd"] or "commands":["../../../../secret"] would
// otherwise be read and projected into the user's agent config. The
// fetchers are hardened, but manifest-listed paths were not.
func assertWithinCache(cacheDir, resolved string) error {
	absCache, err := filepath.Abs(cacheDir)
	if err != nil {
		return err
	}
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return err
	}
	if !pathContains(filepath.Clean(absCache), filepath.Clean(absResolved)) {
		return fmt.Errorf("plugin component path %q escapes plugin cache %q", resolved, cacheDir)
	}
	return nil
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
// listDir may be nil; when non-nil and the manifest lists no skills, skills
// are discovered by convention from cacheDir/skills/*/SKILL.md.
func applyManifest(manifest PluginManifest, pr *ProjectionResult, cacheDir string, readFile func(string) ([]byte, error), listDir func(string) ([]os.DirEntry, error)) error {
	for name, raw := range manifest.MCPServers {
		spec := parseMCPSpec(raw, cacheDir)
		pr.MCPServers = append(pr.MCPServers, source.MCPServer{ID: name, Server: spec})
	}
	for name, raw := range manifest.LSPServers {
		spec := parseLSPSpec(raw, cacheDir)
		pr.LSPServers = append(pr.LSPServers, source.LSPServer{ID: name, Spec: spec})
	}

	skillPaths := toStringSlice(manifest.Skills)
	if len(skillPaths) == 0 && listDir != nil {
		// Convention-based discovery: scan cacheDir/skills/*/SKILL.md
		discovered, err := discoverSkillDirs(filepath.Join(cacheDir, "skills"), listDir)
		if err != nil {
			slog.Warn("plugin skills convention-discovery failed", "cacheDir", cacheDir, "error", err)
		} else {
			skillPaths = discovered
		}
	}
	for _, sk := range skillPaths {
		p, err := resolveComponentPath(sk, cacheDir)
		if err != nil {
			return err
		}
		skill, err := loadSkillEntry(p, readFile)
		if err != nil {
			return fmt.Errorf("load skill %q: %w", sk, err)
		}
		if skill != nil {
			pr.Skills = append(pr.Skills, *skill)
		}
	}

	for _, cmd := range toStringSlice(manifest.Commands) {
		p, err := resolveComponentPath(cmd, cacheDir)
		if err != nil {
			return err
		}
		command, err := loadMarkdownEntry(p, readFile)
		if err != nil {
			return fmt.Errorf("load command %q: %w", cmd, err)
		}
		if command != nil {
			pr.Commands = append(pr.Commands, source.Command{Name: command.name, Frontmatter: command.fm, Body: command.body})
		}
	}
	for _, ag := range toStringSlice(manifest.Agents) {
		p, err := resolveComponentPath(ag, cacheDir)
		if err != nil {
			return err
		}
		agent, err := loadMarkdownEntry(p, readFile)
		if err != nil {
			return fmt.Errorf("load agent %q: %w", ag, err)
		}
		if agent != nil {
			pr.Subagents = append(pr.Subagents, source.Subagent{Name: agent.name, Frontmatter: agent.fm, Body: agent.body})
		}
	}
	// Hooks: may be a string command or an object with event+command shape.
	applyHooks(manifest.Hooks, pr, cacheDir)
	return nil
}

// discoverSkillDirs scans skillsDir for subdirectories and returns a list of
// absolute paths to each subdirectory (each of which is expected to contain
// a SKILL.md file). The caller resolves the actual SKILL.md via loadSkillEntry.
func discoverSkillDirs(skillsDir string, listDir func(string) ([]os.DirEntry, error)) ([]string, error) {
	entries, err := listDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			paths = append(paths, filepath.Join(skillsDir, e.Name()))
		}
	}
	return paths, nil
}

// applyEntryOverrides merges component fields from a PluginEntry into an
// already-seeded ProjectionResult (strict mode overlay).
func applyEntryOverrides(entry PluginEntry, pr *ProjectionResult, cacheDir string, readFile func(string) ([]byte, error)) error {
	for name, raw := range entry.MCPServers {
		spec := parseMCPSpec(raw, cacheDir)
		pr.MCPServers = append(pr.MCPServers, source.MCPServer{ID: name, Server: spec})
	}
	for name, raw := range entry.LSPServers {
		spec := parseLSPSpec(raw, cacheDir)
		pr.LSPServers = append(pr.LSPServers, source.LSPServer{ID: name, Spec: spec})
	}
	for _, sk := range toStringSlice(entry.Skills) {
		p, err := resolveComponentPath(sk, cacheDir)
		if err != nil {
			return err
		}
		skill, err := loadSkillEntry(p, readFile)
		if err != nil {
			return fmt.Errorf("load skill %q: %w", sk, err)
		}
		if skill != nil {
			pr.Skills = append(pr.Skills, *skill)
		}
	}
	for _, cmd := range toStringSlice(entry.Commands) {
		p, err := resolveComponentPath(cmd, cacheDir)
		if err != nil {
			return err
		}
		command, err := loadMarkdownEntry(p, readFile)
		if err != nil {
			return fmt.Errorf("load command %q: %w", cmd, err)
		}
		if command != nil {
			pr.Commands = append(pr.Commands, source.Command{Name: command.name, Frontmatter: command.fm, Body: command.body})
		}
	}
	for _, ag := range toStringSlice(entry.Agents) {
		p, err := resolveComponentPath(ag, cacheDir)
		if err != nil {
			return err
		}
		agent, err := loadMarkdownEntry(p, readFile)
		if err != nil {
			return fmt.Errorf("load agent %q: %w", ag, err)
		}
		if agent != nil {
			pr.Subagents = append(pr.Subagents, source.Subagent{Name: agent.name, Frontmatter: agent.fm, Body: agent.body})
		}
	}
	applyHooks(entry.Hooks, pr, cacheDir)
	return nil
}

// markdownEntry holds the parsed result of a single markdown file.
type markdownEntry struct {
	name string
	fm   map[string]any
	body string
}

// loadSkillEntry reads a skill path which may be either a directory containing
// SKILL.md or a SKILL.md file directly. Returns nil (no error) if the file is
// simply missing — the caller should skip that entry with a warning.
// Returns an error only for real I/O problems or malformed frontmatter.
func loadSkillEntry(path string, readFile func(string) ([]byte, error)) (*source.Skill, error) {
	// Try directory convention first: <path>/SKILL.md
	skillPath := filepath.Join(path, "SKILL.md")
	data, err := readFile(skillPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Try treating path itself as the file (e.g. skills/foo/SKILL.md directly).
			data, err = readFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					slog.Warn("plugin skill file not found, skipping", "path", path)
					return nil, nil
				}
				return nil, fmt.Errorf("read %s: %w", path, err)
			}
			skillPath = path
		} else {
			return nil, fmt.Errorf("read %s: %w", skillPath, err)
		}
	}

	fm, body, err := source.ParseFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter in %s: %w", skillPath, err)
	}

	// Derive name: prefer frontmatter "name" key, fall back to basename of the resolved path.
	name := ""
	if v, ok := fm["name"].(string); ok && v != "" {
		name = v
	}
	if name == "" {
		name = filepath.Base(path)
	}
	if err := validateProjectedName("skill", name); err != nil {
		return nil, err
	}

	return &source.Skill{Name: name, Frontmatter: fm, Body: body}, nil
}

// loadMarkdownEntry reads a markdown file at path. Returns nil (no error) if
// the file is simply missing — the caller should skip that entry with a
// warning. Returns an error for I/O failures or malformed frontmatter.
func loadMarkdownEntry(path string, readFile func(string) ([]byte, error)) (*markdownEntry, error) {
	data, err := readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("plugin component file not found, skipping", "path", path)
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	fm, body, err := source.ParseFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter in %s: %w", path, err)
	}

	// Derive name: prefer frontmatter "name" key, fall back to basename without .md.
	name := ""
	if v, ok := fm["name"].(string); ok && v != "" {
		name = v
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if err := validateProjectedName("component", name); err != nil {
		return nil, err
	}

	return &markdownEntry{name: name, fm: fm, body: body}, nil
}

// validateProjectedName rejects a skill/subagent/command name that would
// escape its destination directory once joined into a path. Component names
// become path segments at render time, so a name from a hostile plugin
// manifest's frontmatter like "../../evil" must never reach a writer. This
// mirrors the equivalent guard in the source loader's projection twin — the
// runtime path — so the two projection implementations cannot diverge.
func validateProjectedName(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s has an empty name", kind)
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("%s name %q contains a path separator or traversal component", kind, name)
	}
	return nil
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
