// Package marketplace models the Claude marketplace plugin format and provides
// the projection layer that decomposes plugin manifests into canonical source
// model entries. Skills, commands, and subagents are fully loaded from their
// on-disk markdown files (frontmatter + body + clean Name) via the injected
// readFile function so that adapters downstream receive complete, render-ready
// entries. Components a plugin ships in their conventional default locations
// (skills/, commands/, agents/, .mcp.json, .lsp.json, hooks/hooks.json) are
// convention-discovered — matching Claude Code, which auto-discovers those
// defaults whether or not a manifest is present. Per Claude's path-behavior
// rules a listed commands/agents/mcp/lsp/hooks field REPLACES its default scan,
// while a listed `skills` ADDS to the always-scanned skills/ directory.
package marketplace

import (
	"encoding/json"
	"errors"
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
//     component list; a missing plugin.json is fine (the manifest is optional —
//     a curator-defined plugin may declare everything in the entry, and a plugin
//     that ships only conventional directories declares nothing at all).
//   - Convention-based discovery of each component kind's default location: the
//     directory scans cacheDir/skills/*/SKILL.md, cacheDir/commands/*.md,
//     cacheDir/agents/*.md and the config files cacheDir/.mcp.json,
//     cacheDir/.lsp.json, cacheDir/hooks/hooks.json. This mirrors Claude Code's
//     path-behavior rules: for commands/agents/mcp/lsp/hooks a listed manifest
//     field REPLACES the default scan (so discovery runs only when the manifest is
//     silent), whereas `skills` ADDS to the always-scanned skills/ directory (so
//     listed skills and conventional skills are unioned).
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
// filesystem use in tests. Convention-based discovery (the default skills/,
// commands/, and agents/ directory scans) is disabled when using
// ProjectWithReader; use Project for full behavior.
func ProjectWithReader(entry PluginEntry, cacheDir string, readFile func(string) ([]byte, error)) (ProjectionResult, error) {
	return projectWithFuncs(entry, cacheDir, readFile, nil, false)
}

// projectWithFuncs is the internal implementation shared by Project and ProjectWithReader.
// listDir may be nil to disable convention-based discovery of the default skills/,
// commands/, and agents/ directories. When lenient is true, a strict-mode
// same-name conflict is resolved entry-wins with a logged warning instead of a
// hard error — used by read-only/diagnostic commands that must still show state
// (see resolveConflicts).
func projectWithFuncs(entry PluginEntry, cacheDir string, readFile func(string) ([]byte, error), listDir func(string) ([]os.DirEntry, error), lenient bool) (ProjectionResult, error) {
	var pr ProjectionResult

	// Always honour plugin.json when present (a missing one is fine for a
	// curator-defined plugin), then overlay the entry's component config. Union
	// semantics — plugin.json PLUS entry additions/overrides — regardless of the
	// Strict flag, so a non-strict entry never drops the plugin's own components.
	manifestPath := filepath.Join(cacheDir, ".claude-plugin", "plugin.json")
	var manifest PluginManifest
	data, err := readFile(manifestPath)
	switch {
	case err != nil && !os.IsNotExist(err):
		return pr, fmt.Errorf("read plugin.json: %w", err)
	case err == nil:
		if err := json.Unmarshal(data, &manifest); err != nil {
			return pr, fmt.Errorf("parse plugin.json: %w", err)
		}
	}
	// applyManifest runs even when plugin.json is absent: the manifest is
	// optional (Claude Code auto-discovers components in their default locations
	// whether or not one is present), and a zero-value manifest carries no
	// explicit component lists, so convention-based discovery of the default
	// skills/, commands/, and agents/ directories still runs.
	if err := applyManifest(manifest, &pr, cacheDir, readFile, listDir); err != nil {
		return pr, err
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
// listDir may be nil; when non-nil, convention-based discovery fills each
// component kind from its default location — the directory scans
// (cacheDir/skills/*/SKILL.md, cacheDir/commands/*.md, cacheDir/agents/*.md) and
// the conventional config files (cacheDir/.mcp.json, cacheDir/.lsp.json,
// cacheDir/hooks/hooks.json). For commands/agents/mcp/lsp/hooks a manifest list
// REPLACES the scan (discovery runs only when the field is empty); `skills` is
// always scanned and unioned (it ADDS). A zero-value manifest (plugin.json
// absent) therefore still discovers everything, matching Claude Code's
// optional-manifest auto-discovery. listDir==nil (the ProjectWithReader
// in-memory path) keeps projection explicit-list-only, so the file-based
// discovery is gated on it too even though it reads through readFile.
func applyManifest(manifest PluginManifest, pr *ProjectionResult, cacheDir string, readFile func(string) ([]byte, error), listDir func(string) ([]os.DirEntry, error)) error {
	if len(manifest.MCPServers) > 0 {
		for name, raw := range manifest.MCPServers {
			spec := parseMCPSpec(raw, cacheDir)
			pr.MCPServers = append(pr.MCPServers, source.MCPServer{ID: name, Server: spec})
		}
	} else if listDir != nil {
		// Convention-based discovery: when plugin.json lists no mcpServers, read
		// the default .mcp.json (the standard `{"mcpServers":{…}}` shape).
		discoverConventionMCP(cacheDir, readFile, pr)
	}
	if len(manifest.LSPServers) > 0 {
		for name, raw := range manifest.LSPServers {
			spec := parseLSPSpec(raw, cacheDir)
			pr.LSPServers = append(pr.LSPServers, source.LSPServer{ID: name, Spec: spec})
		}
	} else if listDir != nil {
		// Convention-based discovery: .lsp.json is a BARE name→config map (no
		// wrapper), unlike the inline `lspServers` object — see discoverConventionLSP.
		discoverConventionLSP(cacheDir, readFile, pr)
	}

	// Skills: the manifest-listed skills are loaded loudly (a broken one the
	// author named is a hard error), and the default skills/ directory is ALWAYS
	// scanned and unioned — `skills` ADDS to the default scan (unlike
	// commands/agents, which REPLACE it), matching Claude Code's path-behavior
	// rules. A discovered skill that fails to load is skipped with a warning, not
	// fatal (see appendSkillEntries): one malformed SKILL.md in one plugin must
	// not abort projection for every installed plugin. A skill appearing in both
	// the manifest list and the scan resolves to the same dir and collapses in
	// resolveConflicts.
	if err := appendSkillEntries(pr, toStringSlice(manifest.Skills), cacheDir, readFile, listDir, false); err != nil {
		return err
	}
	if listDir != nil {
		discovered, err := discoverSkillDirs(filepath.Join(cacheDir, "skills"), listDir)
		switch {
		case errors.Is(err, errSkillSanityCap):
			// Cap violations are deliberate refusals — propagate loudly. A
			// quiet WARN would defeat the entire point of having the caps.
			return err
		case err != nil:
			slog.Warn("plugin skills convention-discovery failed", "cacheDir", cacheDir, "error", err)
		default:
			if err := appendSkillEntries(pr, discovered, cacheDir, readFile, listDir, true); err != nil {
				return err
			}
		}
	}

	// Commands: a listed `commands` field REPLACES the default commands/ scan, an
	// absent one falls back to it (Claude's plugin convention). Without this, a
	// plugin that ships its command(s) only in the conventional directory (the
	// common case — most plugins omit the manifest field, e.g. the official
	// code-review's commands/code-review.md) projected as "no components".
	commandPaths := toStringSlice(manifest.Commands)
	discoveredCommands := false
	if len(commandPaths) == 0 && listDir != nil {
		discovered, err := discoverFlatMarkdown(filepath.Join(cacheDir, "commands"), listDir)
		if err != nil {
			slog.Warn("plugin commands convention-discovery failed", "cacheDir", cacheDir, "error", err)
		} else {
			commandPaths = discovered
			discoveredCommands = true
		}
	}
	if err := appendMarkdownComponents(commandPaths, cacheDir, readFile, discoveredCommands, "command", func(e *markdownEntry) {
		pr.Commands = append(pr.Commands, source.Command{Name: e.name, Frontmatter: e.fm, Body: e.body})
	}); err != nil {
		return err
	}

	// Agents: same REPLACE convention as commands — a plugin that ships subagents
	// only in the conventional agents/ directory (e.g. the official
	// code-simplifier's agents/code-simplifier.md) would otherwise project nothing.
	agentPaths := toStringSlice(manifest.Agents)
	discoveredAgents := false
	if len(agentPaths) == 0 && listDir != nil {
		discovered, err := discoverFlatMarkdown(filepath.Join(cacheDir, "agents"), listDir)
		if err != nil {
			slog.Warn("plugin agents convention-discovery failed", "cacheDir", cacheDir, "error", err)
		} else {
			agentPaths = discovered
			discoveredAgents = true
		}
	}
	if err := appendMarkdownComponents(agentPaths, cacheDir, readFile, discoveredAgents, "agent", func(e *markdownEntry) {
		pr.Subagents = append(pr.Subagents, source.Subagent{Name: e.name, Frontmatter: e.fm, Body: e.body})
	}); err != nil {
		return err
	}
	// Hooks: may be a string command or an object with event+command shape.
	if manifest.Hooks != nil {
		applyHooks(manifest.Hooks, pr, cacheDir)
	} else if listDir != nil {
		// Convention-based discovery: when plugin.json carries no inline hooks,
		// read the default hooks/hooks.json (the `{"hooks":{…}}` shape).
		discoverConventionHooks(cacheDir, readFile, pr)
	}
	return nil
}

// appendMarkdownComponents loads each markdown component (command or subagent) at
// the given paths and hands the parsed entry to add. The discovered flag selects
// the failure policy: a convention-DISCOVERED path that fails to resolve, parse,
// or validate is logged and skipped, because one malformed/hostile file in one
// plugin must not abort projection for EVERY installed plugin (projection runs
// for all of them inside explain/apply/status/diff) — the same resilience the
// config-file discovery and the missing-file path already have. A manifest- or
// entry-LISTED path instead propagates the error: the author named it explicitly,
// so a broken one is a hard failure (preserving the long-standing listed-component
// error contract).
func appendMarkdownComponents(paths []string, cacheDir string, readFile func(string) ([]byte, error), discovered bool, kind string, add func(*markdownEntry)) error {
	for _, ref := range paths {
		p, err := resolveComponentPath(ref, cacheDir)
		if err != nil {
			if discovered {
				slog.Warn("plugin convention-discovered component skipped (path rejected)", "kind", kind, "ref", ref, "error", err)
				continue
			}
			return err
		}
		entry, err := loadMarkdownEntry(p, readFile)
		if err != nil {
			if discovered {
				slog.Warn("plugin convention-discovered component skipped (unreadable/invalid)", "kind", kind, "ref", ref, "error", err)
				continue
			}
			return fmt.Errorf("load %s %q: %w", kind, ref, err)
		}
		if entry != nil {
			add(entry)
		}
	}
	return nil
}

// appendSkillEntries loads each skill at the given paths into pr.Skills, applying
// the same discovered-vs-listed failure policy as appendMarkdownComponents: a
// convention-DISCOVERED skill whose SKILL.md is missing/malformed is skipped with
// a warning (one bad skill must not abort projection for every plugin), while a
// LISTED skill propagates the error loudly.
func appendSkillEntries(pr *ProjectionResult, paths []string, cacheDir string, readFile func(string) ([]byte, error), listDir func(string) ([]os.DirEntry, error), discovered bool) error {
	for _, ref := range paths {
		p, err := resolveComponentPath(ref, cacheDir)
		if err != nil {
			if discovered {
				slog.Warn("plugin convention-discovered skill skipped (path rejected)", "ref", ref, "error", err)
				continue
			}
			return err
		}
		skill, err := loadSkillEntry(p, readFile, listDir)
		if err != nil {
			if discovered {
				slog.Warn("plugin convention-discovered skill skipped (unreadable/invalid)", "ref", ref, "error", err)
				continue
			}
			return fmt.Errorf("load skill %q: %w", ref, err)
		}
		if skill != nil {
			pr.Skills = append(pr.Skills, *skill)
		}
	}
	return nil
}

// discoverConventionMCP reads a plugin's conventional .mcp.json (the standard
// `{"mcpServers": {…}}` document) and projects each server through the same
// parseMCPSpec the inline plugin.json path uses, so ${CLAUDE_PLUGIN_ROOT}
// resolution and field handling are identical. A missing/unreadable/malformed
// file is a no-op (see readConventionConfig).
func discoverConventionMCP(cacheDir string, readFile func(string) ([]byte, error), pr *ProjectionResult) {
	doc, ok := readConventionConfig(filepath.Join(cacheDir, ".mcp.json"), readFile)
	if !ok {
		return
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	for name, raw := range servers {
		pr.MCPServers = append(pr.MCPServers, source.MCPServer{ID: name, Server: parseMCPSpec(raw, cacheDir)})
	}
}

// discoverConventionLSP reads a plugin's conventional .lsp.json. Unlike the
// inline `lspServers` object, the .lsp.json file is a BARE map of language-server
// name → config (no wrapper), per the plugin spec — so the whole document is the
// server map. A missing/unreadable/malformed file is a no-op.
func discoverConventionLSP(cacheDir string, readFile func(string) ([]byte, error), pr *ProjectionResult) {
	doc, ok := readConventionConfig(filepath.Join(cacheDir, ".lsp.json"), readFile)
	if !ok {
		return
	}
	for name, raw := range doc {
		pr.LSPServers = append(pr.LSPServers, source.LSPServer{ID: name, Spec: parseLSPSpec(raw, cacheDir)})
	}
}

// discoverConventionHooks reads a plugin's conventional hooks/hooks.json (the
// `{"hooks": {…}}` document) and projects its event map through the same
// applyHooks the inline plugin.json path uses. A missing/unreadable/malformed
// file, or one with no "hooks" key, is a no-op.
func discoverConventionHooks(cacheDir string, readFile func(string) ([]byte, error), pr *ProjectionResult) {
	doc, ok := readConventionConfig(filepath.Join(cacheDir, "hooks", "hooks.json"), readFile)
	if !ok {
		return
	}
	if hooks, found := doc["hooks"]; found {
		applyHooks(hooks, pr, cacheDir)
	}
}

// readConventionConfig reads and unmarshals one of a plugin's conventional JSON
// config files (.mcp.json, .lsp.json, hooks/hooks.json) for convention-based
// discovery. ok is false — with NO error surfaced — when the file is absent (a
// plugin need not ship every conventional config) OR when it cannot be read or
// parsed as a JSON object. A single malformed/unreadable config file must drop
// only that file's components, never abort the whole projection: projection runs
// for EVERY installed plugin inside `explain`/`apply`/`status`/`diff`, so a
// propagated error would brick those commands for ALL plugins over one bad file
// in one plugin — the same failure mode the lenient frontmatter parser avoids for
// component markdown. The failure is logged, mirroring how the directory-scan
// discovery paths (discoverFlatMarkdown/discoverSkillDirs) warn-and-skip.
func readConventionConfig(path string, readFile func(string) ([]byte, error)) (map[string]any, bool) {
	data, err := readFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("plugin convention config unreadable; skipping", "path", path, "error", err)
		}
		return nil, false
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		slog.Warn("plugin convention config is not a valid JSON object; skipping", "path", path, "error", err)
		return nil, false
	}
	return doc, true
}

// maxSkillDepth caps how many directories below skillsDir a SKILL.md may live.
// 32 is absurdly generous (the deepest real plugin is 2 levels); the cap exists
// purely as a backstop against a malformed or hostile plugin tarball.
//
// maxSkillLeaves caps the total skills a single plugin may ship. The largest
// legitimate plugins ship a few dozen; 256 is "you have bigger problems than
// this error" territory.
//
// Both fire with a deliberately loud error — a quiet skip would hide the bug.
const (
	maxSkillDepth  = 32
	maxSkillLeaves = 256
)

// errSkillSanityCap marks a deliberate refusal from one of the two caps above.
// The caller (Project's convention-discovery path) normally swallows
// discoverSkillDirs errors with a warning so a transient filesystem hiccup
// doesn't brick projection, but a cap violation is the OPPOSITE of transient:
// it's the whole point. Caller propagates anything that wraps this sentinel.
var errSkillSanityCap = errors.New("skill sanity cap exceeded")

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
//
// Two sanity caps bound a pathological / hostile plugin tarball before it can
// eat the host's stack, memory, or attention span: maxSkillDepth (refuse a
// SKILL.md nested more than 32 directories below skillsDir) and maxSkillLeaves
// (refuse a plugin shipping more than 256 skills total). Real plugins live
// well under both, so these only fire on plugins that are either malformed or
// actively trying something stupid.
func discoverSkillDirs(skillsDir string, listDir func(string) ([]os.DirEntry, error)) ([]string, error) {
	var paths []string
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > maxSkillDepth {
			return fmt.Errorf("\n"+
				"==============================================================\n"+
				"  STOP. a plugin is nesting skills %d+ directories deep.\n"+
				"  agentsync says NOPE.\n"+
				"==============================================================\n"+
				"  path: %s\n"+
				"  cap:  maxSkillDepth = %d\n"+
				"\n"+
				"  the deepest legit plugin in the wild is 2 levels deep\n"+
				"  (notion: skills/notion/<category>/SKILL.md). %d+ is not\n"+
				"  \"edge case\" territory — it's \"did you fall asleep on the\n"+
				"  mkdir key\" territory. restructure the plugin, not the cap.\n"+
				"  [%w]",
				maxSkillDepth+1, dir, maxSkillDepth, maxSkillDepth+1, errSkillSanityCap)
		}
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
			if len(paths) >= maxSkillLeaves {
				return fmt.Errorf("\n"+
					"==============================================================\n"+
					"  STOP. a plugin is trying to ship more than %d skills.\n"+
					"  agentsync says NOPE.\n"+
					"==============================================================\n"+
					"  cap:           maxSkillLeaves = %d\n"+
					"  refused while trying to land skill at: %s\n"+
					"\n"+
					"  the largest legit Claude plugins ship a few dozen skills.\n"+
					"  %d+ is either a plugin author who lost the plot or a\n"+
					"  recursion bug about to eat your machine. either way,\n"+
					"  agentsync refuses to project this. fix the plugin.\n"+
					"  [%w]",
					maxSkillLeaves, maxSkillLeaves, dir, maxSkillLeaves+1, errSkillSanityCap)
			}
			paths = append(paths, dir)
			return nil // leaf skill; its subdirs are bundled files, not skills
		}
		for _, sd := range subdirs {
			if err := walk(sd, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(skillsDir, 0); err != nil {
		return nil, err
	}
	return paths, nil
}

// discoverFlatMarkdown returns the absolute path of every regular *.md file
// directly inside dir, sorted for determinism. It backs the convention-based
// discovery of a plugin's commands/ and agents/ directories — the default
// locations Claude Code auto-loads when plugin.json lists no `commands`/`agents`
// (the common case: a plugin ships those components in the conventional
// directory and omits the manifest field). A missing dir yields no paths and no
// error — a plugin need not ship every component kind.
//
// The scan is deliberately NOT recursive: plugin commands and agents are flat
// markdown files per the plugin spec, so descending would sweep in unrelated or
// nested files. Non-regular entries are skipped — subdirectories, and (because a
// fetched plugin repo is untrusted) symlinks, which IsRegular() reports false for
// — so a symlink can never pull foreign content into the projection, matching the
// symlink guard in collectSkillFiles.
func discoverFlatMarkdown(dir string, listDir func(string) ([]os.DirEntry, error)) ([]string, error) {
	entries, err := listDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if filepath.Ext(e.Name()) != ".md" {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	sort.Strings(paths)
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
// Returns an error only for real I/O problems or frontmatter neither the strict
// nor the lenient YAML parser can read. Frontmatter is parsed the way Claude Code
// reads it (source.ParseFrontmatterWithReport): a `description` with bare colons —
// valid to Claude, invalid to strict YAML — is recovered leniently with a warning
// rather than failing the projection.
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

	fm, body, lenient, err := source.ParseFrontmatterWithReport(data)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter in %s: %w", skillPath, err)
	}
	if lenient {
		slog.Warn("plugin skill frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')", "path", skillPath)
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

// loadMarkdownEntry reads a markdown file at path (a command or subagent).
// Returns nil (no error) if the file is simply missing — the caller should skip
// that entry with a warning. Returns an error for I/O failures or frontmatter
// neither the strict nor the lenient YAML parser can read. Like Claude Code (and
// the adapter Ingest paths), it parses via source.ParseFrontmatterWithReport, so
// a `description` containing bare colons — which strict YAML rejects but Claude
// accepts — is recovered leniently with a warning instead of failing.
func loadMarkdownEntry(path string, readFile func(string) ([]byte, error)) (*markdownEntry, error) {
	data, err := readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("plugin component file not found, skipping", "path", path)
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	fm, body, lenient, err := source.ParseFrontmatterWithReport(data)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter in %s: %w", path, err)
	}
	if lenient {
		slog.Warn("plugin component frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')", "path", path)
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
// Hook entries. Hook format from plugin.json — and the conventional
// hooks/hooks.json — can be:
//   - a plain command string → PreToolUse catch-all
//   - []string → each a PreToolUse catch-all
//   - map[event] → a command string, a group object, or a []group, where a
//     group is the canonical Claude shape {matcher, hooks:[{type,command},…]}
//     or a simplified flat {matcher, command}.
func applyHooks(hooks any, pr *ProjectionResult, cacheDir string) {
	if hooks == nil {
		return
	}
	switch v := hooks.(type) {
	case string:
		appendCommandHook(pr, "PreToolUse", "*", "command", v, cacheDir)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				appendCommandHook(pr, "PreToolUse", "*", "command", s, cacheDir)
			}
		}
	case map[string]any:
		for event, val := range v {
			switch ev := val.(type) {
			case string:
				appendCommandHook(pr, event, "*", "command", ev, cacheDir)
			case map[string]any:
				appendHookGroup(pr, event, ev, cacheDir)
			case []any:
				for _, item := range ev {
					if m, ok := item.(map[string]any); ok {
						appendHookGroup(pr, event, m, cacheDir)
					}
				}
			}
		}
	}
}

// appendHookGroup appends the hook(s) one event-group object describes. It
// handles the canonical Claude shape {matcher, hooks:[{type,command},…]} (one
// Hook per nested entry, all carrying the group's matcher) and the simplified
// flat {matcher, command} shape, distinguished by whether a nested "hooks" array
// is present.
func appendHookGroup(pr *ProjectionResult, event string, group map[string]any, cacheDir string) {
	matcher, _ := group["matcher"].(string)
	if matcher == "" {
		matcher = "*"
	}
	if nested, ok := group["hooks"].([]any); ok {
		for _, h := range nested {
			m, ok := h.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := m["type"].(string)
			if typ == "" {
				typ = "command"
			}
			cmd, _ := m["command"].(string)
			appendCommandHook(pr, event, matcher, typ, cmd, cacheDir)
		}
		return
	}
	typ, _ := group["type"].(string)
	if typ == "" {
		typ = "command"
	}
	cmd, _ := group["command"].(string)
	appendCommandHook(pr, event, matcher, typ, cmd, cacheDir)
}

// appendCommandHook appends a single hook, resolving ${CLAUDE_PLUGIN_ROOT} in the
// command. An entry with no command is skipped rather than projected as an empty,
// no-op hook — this also drops the hook types agentsync's command-only Hook model
// does not represent (http/mcp_tool/prompt/agent), which carry no shell command.
func appendCommandHook(pr *ProjectionResult, event, matcher, typ, command, cacheDir string) {
	if command == "" {
		return
	}
	pr.Hooks = append(pr.Hooks, source.Hook{
		Event:   event,
		Matcher: matcher,
		Type:    typ,
		Command: resolvePluginRoot(command, cacheDir),
	})
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
