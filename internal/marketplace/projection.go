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
	"reflect"
	"sort"
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
//   - Then overlays any component fields on the PluginEntry itself.
//
// This is a UNION: plugin.json PLUS entry additions. The entry.Strict flag
// governs how a same-name CONFLICT between the two is resolved (see
// resolveConflicts): strict (the default) errors so a packaging disagreement is
// never silently guessed; non-strict lets the entry override. Union semantics
// never silently drop a plugin's declared components — the pre-Strict-policy
// non-strict path used to IGNORE plugin.json entirely, dropping a plugin's own
// components after an upstream strict-flip.
//
// ${CLAUDE_PLUGIN_ROOT} in command/url strings is replaced with cacheDir so
// non-Claude adapters can resolve binary paths.
func Project(entry PluginEntry, cacheDir string) (ProjectionResult, error) {
	return projectWithFuncs(entry, cacheDir, os.ReadFile, os.ReadDir, false)
}

// ProjectWithReader is like Project but uses a caller-supplied readFile function
// for loading plugin.json and component markdown files. This enables in-memory
// filesystem use in tests. Convention-based discovery (skills/ directory scan)
// is disabled when using ProjectWithReader; use Project for full behavior.
func ProjectWithReader(entry PluginEntry, cacheDir string, readFile func(string) ([]byte, error)) (ProjectionResult, error) {
	return projectWithFuncs(entry, cacheDir, readFile, nil, false)
}

// projectWithFuncs is the internal implementation shared by Project and ProjectWithReader.
// listDir may be nil to disable convention-based skills discovery. When lenient
// is true, a strict-mode same-name conflict is resolved entry-wins with a logged
// warning instead of a hard error — used by read-only/diagnostic commands that
// must still show state (see resolveConflicts).
func projectWithFuncs(entry PluginEntry, cacheDir string, readFile func(string) ([]byte, error), listDir func(string) ([]os.DirEntry, error), lenient bool) (ProjectionResult, error) {
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
	if err := applyEntryOverrides(entry, &pr, cacheDir, readFile, listDir); err != nil {
		return pr, err
	}
	strict := entry.Strict == nil || *entry.Strict
	if err := resolveConflicts(&pr, strict, lenient); err != nil {
		return pr, err
	}
	return pr, nil
}

// resolveConflicts collapses same-identity components that the union (plugin.json
// PLUS entry) can produce, applying the Strict-flag conflict policy. Without
// collapsing, an entry that re-declares a same-named plugin.json
// skill/subagent/command yields TWO canonical entries that render to the SAME
// dest path — and apply's cross-agent divergence guard then aborts the whole
// run. Hooks have no override key, so they are deduped only on EXACT content
// (two genuinely-distinct hooks for one event both survive) and are not subject
// to the conflict policy.
func resolveConflicts(pr *ProjectionResult, strict, lenient bool) error {
	var err error
	if pr.MCPServers, err = dedupOrConflict(pr.MCPServers, func(s source.MCPServer) string { return s.ID }, strict, lenient, "mcp server"); err != nil {
		return err
	}
	if pr.LSPServers, err = dedupOrConflict(pr.LSPServers, func(s source.LSPServer) string { return s.ID }, strict, lenient, "lsp server"); err != nil {
		return err
	}
	if pr.Skills, err = dedupOrConflict(pr.Skills, func(s source.Skill) string { return s.Name }, strict, lenient, "skill"); err != nil {
		return err
	}
	if pr.Subagents, err = dedupOrConflict(pr.Subagents, func(s source.Subagent) string { return s.Name }, strict, lenient, "subagent"); err != nil {
		return err
	}
	if pr.Commands, err = dedupOrConflict(pr.Commands, func(c source.Command) string { return c.Name }, strict, lenient, "command"); err != nil {
		return err
	}
	pr.Hooks = dedupHooks(pr.Hooks)
	return nil
}

// dedupOrConflict collapses same-key components produced by the union. When two
// entries share a key (a plugin.json definition AND a marketplace-entry one):
//   - identical content → silently dedup to one.
//   - differing content, strict mode → a CONFLICT. Fatal (lenient=false, the
//     mutating commands) returns a hard error refusing to guess. Lenient
//     (lenient=true, read-only/diagnostic commands like status/diff/explain)
//     resolves it entry-wins with a logged warning so the command can still show
//     state instead of refusing to run on a conflict it exists to surface.
//   - differing content, non-strict mode → the entry wins (the documented
//     lenient curator override).
//
// Keeping the LAST occurrence yields entry-wins because applyEntryOverrides runs
// after applyManifest; order is preserved by the position of that last entry.
func dedupOrConflict[T any](items []T, key func(T) string, strict, lenient bool, kind string) ([]T, error) {
	if len(items) < 2 {
		return items, nil
	}
	first := make(map[string]T, len(items))
	lastIdx := make(map[string]int, len(items))
	for i, it := range items {
		k := key(it)
		lastIdx[k] = i
		if prev, ok := first[k]; ok {
			if strict && !reflect.DeepEqual(prev, it) {
				if !lenient {
					return nil, fmt.Errorf("plugin %s %q is defined twice with different content "+
						"(plugin.json and the marketplace entry disagree); resolve it upstream, or set "+
						`"strict": false on the marketplace entry to let the entry override`, kind, k)
				}
				slog.Warn("plugin component conflict resolved entry-wins (strict; shown leniently)",
					"kind", kind, "name", k)
			}
			continue
		}
		first[k] = it
	}
	out := make([]T, 0, len(items))
	for i, it := range items {
		if lastIdx[key(it)] == i {
			out = append(out, it)
		}
	}
	return out, nil
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
		skill, err := loadSkillEntry(p, readFile, listDir)
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

// discoverSkillDirs walks skillsDir and returns the path of every directory that
// directly contains a SKILL.md. It recurses through intermediate *grouping*
// directories so a plugin that nests skills (e.g. notion's
// skills/notion/<category>/SKILL.md) is discovered — the previous one-level scan
// returned the grouping dir itself, which has no SKILL.md, and loadSkillEntry
// then tried to read a directory as a file and hard-failed the whole projection.
// A directory that holds a SKILL.md is treated as a leaf skill and NOT descended
// into, so the skill's own bundled subdirs (scripts/, references/, assets/) are
// left for collectSkillFiles rather than mistaken for nested skills. The caller
// resolves the actual SKILL.md via loadSkillEntry.
func discoverSkillDirs(skillsDir string, listDir func(string) ([]os.DirEntry, error)) ([]string, error) {
	var paths []string
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := listDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		hasSkillMD := false
		var subdirs []string
		for _, e := range entries {
			if e.IsDir() {
				subdirs = append(subdirs, filepath.Join(dir, e.Name()))
				continue
			}
			if e.Name() == "SKILL.md" && e.Type().IsRegular() {
				hasSkillMD = true
			}
		}
		if hasSkillMD {
			paths = append(paths, dir)
			return nil // leaf skill; its subdirs are bundled files, not skills
		}
		for _, sd := range subdirs {
			if err := walk(sd); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(skillsDir); err != nil {
		return nil, err
	}
	return paths, nil
}

// isDirVia reports whether path is a directory, probed through the injected
// listDir: listing a directory succeeds, listing a regular file errors
// (ENOTDIR), and a missing path errors (IsNotExist) — both treated as not a
// directory.
func isDirVia(path string, listDir func(string) ([]os.DirEntry, error)) bool {
	_, err := listDir(path)
	return err == nil
}

// applyEntryOverrides merges component fields from a PluginEntry into an
// already-seeded ProjectionResult (strict mode overlay).
func applyEntryOverrides(entry PluginEntry, pr *ProjectionResult, cacheDir string, readFile func(string) ([]byte, error), listDir func(string) ([]os.DirEntry, error)) error {
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
		skill, err := loadSkillEntry(p, readFile, listDir)
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
//
// A skill is a DIRECTORY, not just SKILL.md: when listDir is non-nil, every
// other file under the skill directory (scripts/, references/, assets/, nested
// files) is captured verbatim into Skill.Files so a plugin-bundled skill is not
// lossy on apply. listDir is nil only on the ProjectWithReader/in-memory path
// (tests), where bundled-file capture is disabled just like convention-based
// discovery.
func loadSkillEntry(path string, readFile func(string) ([]byte, error), listDir func(string) ([]os.DirEntry, error)) (*source.Skill, error) {
	// Try directory convention first: <path>/SKILL.md
	skillPath := filepath.Join(path, "SKILL.md")
	data, err := readFile(skillPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", skillPath, err)
		}
		// <path>/SKILL.md is absent. If `path` is itself a directory (a grouping
		// dir, or a skill dir with no SKILL.md), there is nothing to load — skip
		// with a warning rather than hard-fail trying to read a directory as a
		// file (which surfaces as "is a directory", not os.IsNotExist). Otherwise
		// treat `path` as a direct SKILL.md file (skills/foo/SKILL.md listed
		// verbatim in the manifest).
		if listDir != nil && isDirVia(path, listDir) {
			slog.Warn("plugin skill path is a directory with no SKILL.md, skipping", "path", path)
			return nil, nil
		}
		data, err = readFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				slog.Warn("plugin skill file not found, skipping", "path", path)
				return nil, nil
			}
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		skillPath = path
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

	var files []source.SkillFile
	if listDir != nil {
		files, err = collectSkillFiles(filepath.Dir(skillPath), readFile, listDir)
		if err != nil {
			return nil, fmt.Errorf("collect bundled files for skill %q: %w", name, err)
		}
	}

	return &source.Skill{Name: name, Frontmatter: fm, Body: body, Files: files}, nil
}

// collectSkillFiles recursively gathers every regular file under skillDir other
// than SKILL.md into source.SkillFile entries (slash-separated relative path,
// preserved mode), sorted by path. It is the projection-layer analog of
// source.ReadSkillFiles, implemented over the injected readFile/listDir funcs so
// the in-memory test path can opt out by passing a nil listDir. Symlinks are
// skipped — a fetched plugin repo is untrusted, and following a symlink (e.g.
// skills/x/evil -> /etc) must never pull foreign content into the projection.
func collectSkillFiles(skillDir string, readFile func(string) ([]byte, error), listDir func(string) ([]os.DirEntry, error)) ([]source.SkillFile, error) {
	var files []source.SkillFile
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := listDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, e := range entries {
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if err := walk(full); err != nil {
					return err
				}
				continue
			}
			if !e.Type().IsRegular() {
				continue
			}
			rel, err := filepath.Rel(skillDir, full)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == "SKILL.md" {
				continue
			}
			data, err := readFile(full)
			if err != nil {
				return err
			}
			mode := uint32(0o644)
			if info, infoErr := e.Info(); infoErr == nil {
				mode = uint32(info.Mode().Perm())
			}
			files = append(files, source.SkillFile{Path: rel, Content: data, Mode: mode})
		}
		return nil
	}
	if err := walk(skillDir); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
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
	// Normalise present-but-empty maps to nil so an explicit `"env":{}` /
	// `"headers":{}` compares equal to an omitted one. The union conflict check
	// uses reflect.DeepEqual, which treats a nil map and an empty map as
	// different — without this, two semantically-identical servers (one omitting
	// env, the other declaring it empty) spuriously trip the strict conflict.
	if len(spec.Env) == 0 {
		spec.Env = nil
	}
	if len(spec.Headers) == 0 {
		spec.Headers = nil
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
	// See parseMCPSpec: normalise empty maps to nil so the reflect.DeepEqual
	// conflict check treats an explicit empty env/headers as equal to an omitted
	// one.
	if len(spec.Env) == 0 {
		spec.Env = nil
	}
	if len(spec.Headers) == 0 {
		spec.Headers = nil
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
