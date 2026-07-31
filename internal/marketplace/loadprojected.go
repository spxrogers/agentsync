package marketplace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// LoadProjected loads the canonical model and expands each installed plugin's
// cached manifest into it, so downstream adapters see plugin components
// transparently. It is the single projecting load: source.Load stays plugin-
// unaware, and every command that needs the full canonical (apply, status,
// diff, reconcile, import, explain, update) calls this.
//
// This is the ONLY plugin projector — it delegates to Project, which honours
// plugin.json hooks AND marketplace-entry inline overrides. Previously the
// loader carried a separate, leaner reimplementation that silently dropped
// both; collapsing to one function is what keeps the two from drifting again.
//
// pluginCacheRoot is <home>/.state/cache/plugins; an empty root skips
// projection (behaving like source.Load).
func LoadProjected(fs afero.Fs, home, pluginCacheRoot string) (source.Canonical, error) {
	return loadProjected(fs, home, pluginCacheRoot, nil, false)
}

// LoadProjectedLenient is LoadProjected for read-only/diagnostic commands
// (status, diff, explain): a strict same-name plugin.json/entry conflict is
// resolved entry-wins with a logged warning instead of a hard error, so those
// commands still show state rather than refusing to run on a conflict they
// exist to surface. Mutating commands (apply, reconcile, import, update) use the
// fatal LoadProjected/LoadProjectedExcluding so they never act on ambiguity.
func LoadProjectedLenient(fs afero.Fs, home, pluginCacheRoot string, disabled []string) (source.Canonical, error) {
	return loadProjected(fs, home, pluginCacheRoot, disabled, true)
}

// LoadProjectedExcluding is LoadProjected with an additional set of plugin IDs
// to skip projecting. At project scope the CLI collects these from the project
// tree's plugins/<id>.toml entries marked `disabled = true` (the dir-model
// successor to the M5 marker's `[plugins] disabled`) and passes them when
// projecting BOTH the user and project homes, so a plugin disabled by the
// project never renders its components in that repo.
//
// disabled is matched against pl.ID (the plugins/<id>.toml filename stem), so it
// composes with the per-plugin `disabled = true` flag below: either path skips
// the same projection.
func LoadProjectedExcluding(fs afero.Fs, home, pluginCacheRoot string, disabled []string) (source.Canonical, error) {
	return loadProjected(fs, home, pluginCacheRoot, disabled, false)
}

// loadProjected is the shared implementation. lenient controls how a strict
// same-name plugin.json/entry conflict is handled (see LoadProjectedLenient).
func loadProjected(fs afero.Fs, home, pluginCacheRoot string, disabled []string, lenient bool) (source.Canonical, error) {
	c, err := source.Load(fs, home)
	if err != nil {
		return c, err
	}
	if pluginCacheRoot == "" {
		return c, nil
	}
	disabledByProject := make(map[string]bool, len(disabled))
	for _, id := range disabled {
		disabledByProject[id] = true
	}
	// Build the marketplace-name → entries index ONCE for this load so entry
	// resolution is O(plugins + marketplaces), not O(plugins × marketplaces):
	// the previous per-plugin scan re-read and re-parsed every cached
	// marketplace.json for EVERY installed plugin (issue #162 item G).
	mpIndex := buildMarketplaceIndex(fs, home)
	for _, pl := range c.Plugins {
		proj, ok, perr := projectOnePlugin(fs, home, pluginCacheRoot, pl, disabledByProject, lenient, mpIndex)
		if perr != nil {
			return c, perr
		}
		if !ok {
			continue
		}
		c.MCPServers = append(c.MCPServers, proj.MCPServers...)
		c.Skills = append(c.Skills, proj.Skills...)
		c.Subagents = append(c.Subagents, proj.Subagents...)
		c.Commands = append(c.Commands, proj.Commands...)
		c.Hooks = append(c.Hooks, proj.Hooks...)
		c.LSPServers = append(c.LSPServers, proj.LSPServers...)
	}
	if err := checkProjectedConflicts(&c, lenient); err != nil {
		return c, err
	}
	return c, nil
}

// ProjectInstalled projects ONE installed plugin's components in isolation,
// running the exact per-plugin step loadProjected uses (disabled check,
// id-traversal guard, manifest-SHA verification, plugin.json + marketplace-entry
// union). ok is false when the plugin contributes nothing this load (it is
// disabled via `plugin disable`).
//
// It exists so a diagnostic command can attribute components to the plugin that
// actually contributes them instead of to the flattened union. The projected
// canonical (LoadProjected) concatenates every plugin's components into one set
// of flat slices with no origin-plugin tag, so a row built from that whole model
// cannot tell which plugin a component (or skip) came from. `explain <id>` builds
// a per-plugin plan from this projection rather than slicing the global
// canonical, so its coverage/skip rows reflect only the named plugin.
//
// lenient mirrors LoadProjectedLenient (a strict plugin.json/entry conflict
// degrades to entry-wins + a logged warning rather than a hard error), which is
// what the read-only/diagnostic callers want.
func ProjectInstalled(fs afero.Fs, home, pluginCacheRoot string, pl source.Plugin, lenient bool) (ProjectionResult, bool, error) {
	if pluginCacheRoot == "" {
		return ProjectionResult{}, false, nil
	}
	// nil index → resolveInstalledEntry falls back to its direct per-call scan,
	// which is fine for this single-plugin diagnostic path (no O(P×M) blow-up).
	return projectOnePlugin(fs, home, pluginCacheRoot, pl, nil, lenient, nil)
}

// projectOnePlugin projects a single installed plugin into its ProjectionResult.
// It is the one per-plugin projection step, shared by the flattening load
// (loadProjected) and the per-plugin diagnostic path (ProjectInstalled), so the
// two can never derive different components for the same plugin. ok is false when
// the plugin contributes nothing this load: it is disabled via `plugin disable`
// (pl.Plugin.Disabled), or disabled by the active project tree
// (disabledByProject[pl.ID], the plugins/<id>.toml `disabled = true` flag).
func projectOnePlugin(fs afero.Fs, home, pluginCacheRoot string, pl source.Plugin, disabledByProject map[string]bool, lenient bool, mpIndex marketplaceIndex) (ProjectionResult, bool, error) {
	if pl.Plugin.Disabled || disabledByProject[pl.ID.Unverified()] {
		return ProjectionResult{}, false, nil
	}
	id, mpName := splitPluginRefPkg(pl.Plugin.ID.Unverified())
	if id == "" {
		id = pl.ID.Unverified()
	}
	// Defense-in-depth: a hand-edited plugins/<id>.toml whose id contains
	// "../" must not let plugin.json reads escape the cache root.
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return ProjectionResult{}, false, fmt.Errorf("project plugin %q: id contains a path-traversal component", id)
	}
	pluginDir := filepath.Join(pluginCacheRoot, id)
	if err := verifyPluginManifestSHA(fs, pluginDir, pl.Plugin.ManifestSHA, id); err != nil {
		return ProjectionResult{}, false, err
	}
	// Read the projected component bodies, directory listings, AND the
	// marketplace-entry resolution (resolveInstalledEntry / the pre-built index —
	// entry overrides can inject components) through the SAME injected fs that the
	// tamper guard hashed (verifyPluginManifestSHA → PluginTreeHash), so on a
	// non-OS fs (tests, or any future virtual fs) the guard and the data it
	// protects can never read DIFFERENT trees. Passing os.ReadFile/os.ReadDir
	// here (the old behavior) hashed the injected fs while projecting the real OS
	// filesystem — an integrity guarantee that was unsound off the real
	// filesystem (issue #162 item E).
	readFile := func(p string) ([]byte, error) { return afero.ReadFile(fs, p) }
	readDir := func(p string) ([]os.DirEntry, error) { return aferoReadDirEntries(fs, p) }
	proj, perr := projectWithFuncs(resolveInstalledEntry(fs, home, id, mpName, mpIndex), pluginDir, readFile, readDir, lenient)
	if perr != nil {
		return ProjectionResult{}, false, fmt.Errorf("project plugin %s: %w", id, perr)
	}
	namespaceProjected(&proj, id, pl.Plugin.Agents, pl.Plugin.NativeAgents)
	return proj, true, nil
}

// namespaceProjected rewrites one plugin's name-keyed components to their
// namespaced form and stamps their provenance.
//
// This is THE fix for cross-plugin name collisions, and it lives here rather
// than in an adapter for a structural reason: the flattening load appends every
// plugin's components into one set of flat slices, dropping the origin plugin.
// By the time any adapter can detect that two components claim one name, the one
// piece of information needed to resolve it is already gone.
//
// Rewriting `Name` — rather than teaching each adapter an "effective name" —
// is what makes this a small change. `Name` is what every adapter derives its
// destination path and identity from, so all render sites become correct with no
// adapter change at all.
//
// The frontmatter `name` key is rewritten in step, WHEN PRESENT, because
// renaming only the struct field would leave the components still colliding:
// the Codex adapter prefers frontmatter `name` over the file stem when deriving
// the agent identity (Codex's `name` IS the identity), and Claude's Agent Skills
// require the frontmatter `name` to match the skill directory. It is left absent
// when it was absent, so this never invents an identity the upstream artifact
// did not declare, and a deliberately-divergent name still survives Render →
// Ingest (issue #144).
//
// Ordering matters: this runs AFTER projectWithFuncs' resolveConflicts, whose
// intra-plugin dedup compares whole components with reflect.DeepEqual. Stamping
// provenance first would make two otherwise-identical duplicates continue to
// compare equal (both get the same stamp) but needlessly couples the two steps;
// keeping the rename last also means the conflict policy still reports the
// upstream names the user would recognise.
//
// Hooks, MCP servers, and LSP servers are deliberately NOT renamed: hooks have
// no name key at all (a canonical hooks/<event>.toml holds many handlers from
// many sources), and MCP/LSP are id-keyed with an existing cross-source
// guard (checkProjectedConflicts) whose hard failure is a deliberate security
// property — a same-id server from two sources can be a silent endpoint hijack,
// which is a case to refuse, not to rename apart.
//
// All three ARE still stamped with provenance, though. They are not renamed, but
// the dest→source paths still need to know a plugin owns them so import/reconcile
// refuse to capture one into the canonical source (which would mint a copy that
// diverges from the plugin's own on its next update). Hooks are stamped per
// HANDLER, the granularity import has to filter at.
//
// agents and nativeAgents are the plugin's `agents` allowlist and
// `native_agents` deferral list (PluginSpec), stamped onto EVERY projected
// component — renamed or not. They travel with the component because the
// flattened canonical drops the association otherwise; render.Plan is the one
// place that reads them. See source.PluginTargetsAgent.
func namespaceProjected(pr *ProjectionResult, plugin string, agents, nativeAgents []string) {
	if plugin == "" {
		return
	}
	for i := range pr.MCPServers {
		pr.MCPServers[i].Plugin = plugin
		pr.MCPServers[i].PluginAgents = agents
		pr.MCPServers[i].PluginNativeAgents = nativeAgents
	}
	for i := range pr.LSPServers {
		pr.LSPServers[i].Plugin = plugin
		pr.LSPServers[i].PluginAgents = agents
		pr.LSPServers[i].PluginNativeAgents = nativeAgents
	}
	for i := range pr.Hooks {
		pr.Hooks[i].Plugin = plugin
		pr.Hooks[i].PluginAgents = agents
		pr.Hooks[i].PluginNativeAgents = nativeAgents
	}
	for i := range pr.Skills {
		pr.Skills[i].Plugin = plugin
		pr.Skills[i].PluginAgents = agents
		pr.Skills[i].PluginNativeAgents = nativeAgents
		pr.Skills[i].BaseName = pr.Skills[i].Name
		pr.Skills[i].Name = source.NamespacedComponentName(plugin, pr.Skills[i].Name)
		pr.Skills[i].Frontmatter = renameFrontmatter(pr.Skills[i].Frontmatter, pr.Skills[i].Name)
	}
	for i := range pr.Subagents {
		pr.Subagents[i].Plugin = plugin
		pr.Subagents[i].PluginAgents = agents
		pr.Subagents[i].PluginNativeAgents = nativeAgents
		pr.Subagents[i].BaseName = pr.Subagents[i].Name
		pr.Subagents[i].Name = source.NamespacedComponentName(plugin, pr.Subagents[i].Name)
		pr.Subagents[i].Frontmatter = renameFrontmatter(pr.Subagents[i].Frontmatter, pr.Subagents[i].Name)
	}
	for i := range pr.Commands {
		pr.Commands[i].Plugin = plugin
		pr.Commands[i].PluginAgents = agents
		pr.Commands[i].PluginNativeAgents = nativeAgents
		pr.Commands[i].BaseName = pr.Commands[i].Name
		pr.Commands[i].Name = source.NamespacedComponentName(plugin, pr.Commands[i].Name)
		pr.Commands[i].Frontmatter = renameFrontmatter(pr.Commands[i].Frontmatter, pr.Commands[i].Name)
	}
}

// renameFrontmatter returns fm with its `name` key set to the namespaced name,
// but ONLY if the key is already present — an absent name is left absent (see
// namespaceProjected). The map is copied rather than mutated in place: the
// projection's frontmatter maps can be shared with the caller's parsed
// artifact, and a rename must not reach back into it.
func renameFrontmatter(fm map[string]any, name string) map[string]any {
	if _, ok := fm["name"]; !ok {
		return fm
	}
	out := make(map[string]any, len(fm))
	for k, v := range fm {
		out[k] = v
	}
	out["name"] = name
	return out
}

// aferoReadDirEntries lists dir through the injected fs and adapts the result to
// []os.DirEntry (afero.ReadDir yields []os.FileInfo). It is what lets projection
// read directory listings through the SAME fs the tamper guard hashes; each
// entry's Info()/Type() come from the injected fs, so mode bits and the symlink
// guard in collectSkillFiles are preserved on both the OS fs and a mem fs.
func aferoReadDirEntries(fs afero.Fs, dir string) ([]os.DirEntry, error) {
	infos, err := afero.ReadDir(fs, dir)
	if err != nil {
		return nil, err
	}
	entries := make([]os.DirEntry, len(infos))
	for i, fi := range infos {
		entries[i] = iofs.FileInfoToDirEntry(fi)
	}
	return entries, nil
}

// checkProjectedConflicts surfaces a silent cross-source hijack. The projected
// canonical unions the user's own servers with EVERY enabled plugin's, but the
// adapters render MCP/LSP into an id-keyed map (last write wins). So two plugins
// — or a plugin and the user's own config — declaring the same server id with
// DIFFERENT content would let the later one silently override the earlier, e.g.
// an untrusted plugin repointing a trusted server's command/url/headers at a
// malicious target. Within a single plugin this is already caught by
// resolveConflicts; this is the union guard across plugins + user. Identical
// duplicates are harmless (render dedups them) and pass. Mutating loads
// (apply/reconcile/import/update) fail closed; lenient read-only loads
// (status/diff/explain) warn so they still show state rather than refuse.
func checkProjectedConflicts(c *source.Canonical, lenient bool) error {
	if id, ok := firstDivergentByKey(c.MCPServers, func(s source.MCPServer) string { return s.ID }, sameMCPRender); ok {
		if !lenient {
			return fmt.Errorf("mcp server %q is provided by more than one source (a plugin and/or your "+
				"own config) with different content; rename or disable one so a plugin cannot silently "+
				"override another server's command/url", id)
		}
		slog.Warn("mcp server provided by multiple sources with different content; render keeps the last", "id", id)
	}
	if id, ok := firstDivergentByKey(c.LSPServers, func(s source.LSPServer) string { return s.ID }, sameLSPRender); ok {
		if !lenient {
			return fmt.Errorf("lsp server %q is provided by more than one source with different content; "+
				"rename or disable one so a plugin cannot silently override another server", id)
		}
		slog.Warn("lsp server provided by multiple sources with different content; render keeps the last", "id", id)
	}
	// Name-keyed text components. Namespacing makes a same-name collision rare,
	// but it cannot make the mapping injective: plugin "a" shipping "b-c" and
	// plugin "a-b" shipping "c" both derive "a-b-c", and a user who hand-authors
	// "feature-dev-code-reviewer" collides with what plugin "feature-dev" derives
	// for its "code-reviewer". Without this guard those cases reach the render
	// pipeline, where two DIVERGENT components abort the run with a message that
	// can name neither origin (a FileOp carries no provenance) — and two
	// IDENTICAL ones are silently deduped by the shared-path dedup, dropping a
	// component with no report at all. That silent drop is the bug this guard
	// exists to prevent; the loud one it merely makes actionable.
	//
	// Identical duplicates stay harmless (render dedups them to one byte-identical
	// write), exactly as for MCP/LSP above.
	if name, ok := firstDivergentByKey(c.Skills, func(s source.Skill) string { return s.Name },
		sameSkillRender); ok {
		if err := nameCollisionError("skill", name, c.Skills, func(s source.Skill) (string, string, string) {
			return s.Name, s.Plugin, s.BaseName
		}, lenient); err != nil {
			return err
		}
	}
	if name, ok := firstDivergentByKey(c.Subagents, func(s source.Subagent) string { return s.Name },
		func(a, b source.Subagent) bool { return sameTextRender(a.Frontmatter, a.Body, b.Frontmatter, b.Body) }); ok {
		if err := nameCollisionError("subagent", name, c.Subagents, func(s source.Subagent) (string, string, string) {
			return s.Name, s.Plugin, s.BaseName
		}, lenient); err != nil {
			return err
		}
	}
	if name, ok := firstDivergentByKey(c.Commands, func(cm source.Command) string { return cm.Name },
		func(a, b source.Command) bool { return sameTextRender(a.Frontmatter, a.Body, b.Frontmatter, b.Body) }); ok {
		if err := nameCollisionError("command", name, c.Commands, func(cm source.Command) (string, string, string) {
			return cm.Name, cm.Plugin, cm.BaseName
		}, lenient); err != nil {
			return err
		}
	}
	return nil
}

// sameTextRender compares the two things a text component renders: its
// frontmatter and its body. Provenance (Plugin/BaseName) is deliberately
// excluded — it never reaches a destination file, so two sources differing only
// on it render identically and are not a collision.
func sameTextRender(afm map[string]any, abody string, bfm map[string]any, bbody string) bool {
	if abody != bbody {
		return false
	}
	if len(afm) == 0 && len(bfm) == 0 {
		return true
	}
	return reflect.DeepEqual(afm, bfm)
}

// sameSkillRender extends sameTextRender with the bundled files, because a skill
// is a DIRECTORY: two skills with an identical SKILL.md but different scripts/
// still render different trees.
func sameSkillRender(a, b source.Skill) bool {
	if !sameTextRender(a.Frontmatter, a.Body, b.Frontmatter, b.Body) {
		return false
	}
	if len(a.Files) == 0 && len(b.Files) == 0 {
		return true
	}
	return reflect.DeepEqual(a.Files, b.Files)
}

// nameCollisionError reports a same-name divergence between two name-keyed
// components, naming each side's ORIGIN — which is the whole point, since the
// colliding pair shares the one thing a bare message would print (the name). It
// is fatal for the mutating loads and a warning for the lenient read-only ones,
// matching the MCP/LSP policy directly above.
func nameCollisionError[T any](kind, name string, items []T, originOf func(T) (itemName, plugin, base string), lenient bool) error {
	var origins []string
	for _, it := range items {
		n, plugin, base := originOf(it)
		if n != name {
			continue
		}
		if plugin != "" {
			origins = append(origins, fmt.Sprintf("plugin %q (as %q)", plugin, base))
			continue
		}
		origins = append(origins, fmt.Sprintf("your own canonical %s %q", kind, name))
	}
	if lenient {
		// name is projection-derived (a plugin id joined to an upstream component
		// name) and reaches this log BEFORE render.Plan's ValidateComponentID, so
		// sanitize it here rather than relying on that waist. The origins are
		// already %q-quoted where this function builds them, just above.
		// NOT "render keeps the last" — that is true for MCP/LSP (an id-keyed map,
		// last write wins) but false here: two name-keyed components rendering to
		// one destination path make the apply pipeline abort. Lenient callers are
		// read-only, so they still SHOW state; the next mutating command fails.
		slog.Warn("component provided by multiple sources with different content; "+
			"apply will refuse until one is renamed or its plugin disabled",
			"kind", kind, "name", untrusted.Sanitize(name), "from", strings.Join(origins, " and "))
		return nil
	}
	return fmt.Errorf("%s %q is provided by more than one source with different content — %s. "+
		"Plugin components are namespaced by their plugin, so this is a genuine clash of the derived "+
		"names: rename your own copy, or run `agentsync plugin disable <id>` for one of the plugins",
		kind, name, strings.Join(origins, " and "))
}

// firstDivergentByKey returns the first key shared by two items the sameRender
// predicate considers DIFFERENT. Duplicates that render identically are ignored.
func firstDivergentByKey[T any](items []T, key func(T) string, sameRender func(a, b T) bool) (string, bool) {
	seen := make(map[string]T, len(items))
	for _, it := range items {
		k := key(it)
		if prev, ok := seen[k]; ok {
			if !sameRender(prev, it) {
				return k, true
			}
			continue
		}
		seen[k] = it
	}
	return "", false
}

// sameMCPRender / sameLSPRender compare only the fields that reach the agent
// destination — the ones a hijack would repoint (type/command/args/url/env/
// headers). The `agents`/`enabled` targeting metadata is excluded — it decides
// WHICH agents get the server and whether it is on, not the server's endpoint
// (what a hijack repoints) — so two sources differing ONLY on it are not a
// divergent override. nil and empty collections compare equal.
func sameMCPRender(a, b source.MCPServer) bool {
	return reflect.DeepEqual(mcpRenderFields(a.Server), mcpRenderFields(b.Server))
}

func mcpRenderFields(s source.MCPServerSpec) source.MCPServerSpec {
	out := source.MCPServerSpec{Type: s.Type, Command: s.Command, URL: s.URL}
	if len(s.Args) > 0 {
		out.Args = s.Args
	}
	if len(s.Env) > 0 {
		out.Env = s.Env
	}
	if len(s.Headers) > 0 {
		out.Headers = s.Headers
	}
	return out
}

func sameLSPRender(a, b source.LSPServer) bool {
	return reflect.DeepEqual(lspRenderFields(a.Spec), lspRenderFields(b.Spec))
}

func lspRenderFields(s source.LSPServerSpec) source.LSPServerSpec {
	out := source.LSPServerSpec{Command: s.Command, URL: s.URL}
	if len(s.Args) > 0 {
		out.Args = s.Args
	}
	if len(s.Env) > 0 {
		out.Env = s.Env
	}
	if len(s.Headers) > 0 {
		out.Headers = s.Headers
	}
	return out
}

// resolveInstalledEntry finds the marketplace entry for an installed plugin
// (id, scoped to marketplace mpName) by scanning the cached marketplace.json
// files and matching on the marketplace's own `name` field plus the plugin
// name. This is what carries entry-level inline overrides into projection.
//
// A bare id (mpName == "") is NOT resolved: guessing the first cached
// marketplace that happens to contain a same-named plugin would inject that
// marketplace's inline overrides as foreign config. It falls back to a bare
// strict entry (plugin.json-only). The same fallback applies on any failure
// (no marketplace cache, unparseable json, plugin not found — e.g. after
// `marketplace remove`), so projection degrades to plugin.json-only rather than
// failing the whole load.
//
// CAVEAT: the entry reflects the marketplace's CURRENT version of the plugin,
// which can differ from the installed version until the next `update` — so its
// inline overrides may be slightly ahead of the installed content. Project
// unions plugin.json with the entry, so this never DROPS the plugin's own
// components; at worst a stale entry adds a slightly-ahead override.
func resolveInstalledEntry(fs afero.Fs, home, id, mpName string, idx marketplaceIndex) PluginEntry {
	if mpName == "" {
		return PluginEntry{Name: untrusted.Wrap(id)}
	}
	// Memoized path: a pre-built index (LoadProjected) resolves without touching
	// the filesystem, so N plugins cost O(1) each instead of re-scanning every
	// marketplace.json. A miss falls through to the bare-entry fallback below,
	// exactly like the scan finding no matching entry.
	if idx != nil {
		for _, e := range idx[mpName] {
			if e.Name.Unverified() == id {
				return e
			}
		}
		return PluginEntry{Name: untrusted.Wrap(id)}
	}
	// Fallback (ProjectInstalled diagnostic path, or any nil-index caller): the
	// direct per-call scan through the same injected fs — any read/parse failure
	// or unmatched entry degrades to a bare strict entry.
	cacheRoot := filepath.Join(home, ".state", "cache", "marketplaces")
	dirs, err := afero.ReadDir(fs, cacheRoot)
	if err != nil {
		return PluginEntry{Name: untrusted.Wrap(id)}
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		data, rerr := afero.ReadFile(fs, filepath.Join(cacheRoot, d.Name(), ".claude-plugin", "marketplace.json"))
		if rerr != nil {
			continue
		}
		var mp Marketplace
		if json.Unmarshal(data, &mp) != nil {
			continue
		}
		if mp.Name != mpName {
			continue
		}
		for _, e := range mp.Plugins {
			if e.Name.Unverified() == id {
				return e
			}
		}
	}
	return PluginEntry{Name: untrusted.Wrap(id)}
}

// marketplaceIndex maps a marketplace's DECLARED name (marketplace.json `name`)
// to its plugin entries. It is built once per LoadProjected (buildMarketplaceIndex)
// so resolveInstalledEntry is O(plugins + marketplaces): the previous code
// re-read and re-parsed every cached marketplace.json for EVERY installed plugin
// (issue #162 item G). A nil index means "not memoized" (the ProjectInstalled
// diagnostic path), which resolveInstalledEntry handles with its direct scan.
type marketplaceIndex map[string][]PluginEntry

// buildMarketplaceIndex reads and parses each cached marketplace.json exactly
// once — through the injected fs, the same one the tamper guard hashes and
// projection reads — indexing every marketplace's entries by its declared name.
// It preserves the scan's fallback semantics precisely: an unreadable cache
// root, an unreadable/unparseable marketplace.json, or a missing entry all
// yield "no match" (resolveInstalledEntry then returns a bare strict entry).
// Entries from multiple cached marketplaces that declare the SAME name are
// concatenated in afero.ReadDir (sorted) order, so a lookup still finds the
// first matching entry — matching the old first-dir-wins scan for the
// pathological name-collision case.
func buildMarketplaceIndex(fs afero.Fs, home string) marketplaceIndex {
	idx := marketplaceIndex{}
	cacheRoot := filepath.Join(home, ".state", "cache", "marketplaces")
	dirs, err := afero.ReadDir(fs, cacheRoot)
	if err != nil {
		return idx
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		data, rerr := afero.ReadFile(fs, filepath.Join(cacheRoot, d.Name(), ".claude-plugin", "marketplace.json"))
		if rerr != nil {
			continue
		}
		var mp Marketplace
		if json.Unmarshal(data, &mp) != nil {
			continue
		}
		idx[mp.Name] = append(idx[mp.Name], mp.Plugins...)
	}
	return idx
}

// verifyPluginManifestSHA checks the on-disk plugin cache against the SHA
// recorded in plugins/<id>.toml at install/update. A mismatch means the cache
// was tampered with (or upstream rolled) since the pin was recorded. The pin is
// a PluginTreeHash over EVERY projected component body (not just plugin.json),
// so a tampered SKILL.md / command markdown with an unchanged plugin.json is
// caught. Returns nil when: expected is empty (hand-managed),
// AGENTSYNC_ALLOW_PLUGIN_DRIFT=1, the cache dir is gone (nothing to verify), or
// the pin is an entry-only plugin (no cached bodies to hash).
//
// A pre-tree-hash pin (a bare sha256 hex with no tree: prefix) covered only
// plugin.json; it is verified under that PRIOR scheme (sha256 of plugin.json)
// so existing installs are not broken — a re-install or `plugin upgrade`
// rewrites it as a tree hash that then covers the bodies.
func verifyPluginManifestSHA(fs afero.Fs, pluginCacheDir, expected, id string) error {
	if expected == "" {
		return nil
	}
	if os.Getenv("AGENTSYNC_ALLOW_PLUGIN_DRIFT") == "1" {
		return nil
	}
	// Entry-only plugins ship no cached bodies; the marketplace entry that
	// defines them isn't available here, so there is nothing to recompute.
	if strings.HasPrefix(expected, entryHashPrefix) {
		return nil
	}
	if strings.HasPrefix(expected, treeHashPrefix) {
		if _, err := fs.Stat(pluginCacheDir); errors.Is(err, os.ErrNotExist) {
			return nil // cache gone; projection will surface the absence
		}
		got, err := PluginTreeHash(fs, pluginCacheDir)
		if err != nil {
			return fmt.Errorf("verify plugin %s manifest SHA: %w", id, err)
		}
		if got != expected {
			return fmt.Errorf("plugin %s manifest SHA mismatch (cache tampered or upstream rolled): "+
				"want %s got %s; run `agentsync plugin upgrade %s` to accept the new manifest, "+
				"or set AGENTSYNC_ALLOW_PLUGIN_DRIFT=1 to bypass this check", id, expected, got, id)
		}
		return nil
	}
	// Legacy bare-hex pin (pre-tree-hash): verify under the PRIOR scheme
	// (sha256 over plugin.json only) so existing installs keep working — they
	// were never body-pinned, and refusing them would brick a plugin whose only
	// offered remediation (`agentsync plugin outdated`) does not re-pin a
	// non-bumping plugin. Re-installing or `agentsync plugin upgrade <id>` rewrites the pin
	// as a tree hash, which DOES cover the bodies going forward.
	data, err := afero.ReadFile(fs, filepath.Join(pluginCacheDir, ".claude-plugin", "plugin.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("verify plugin %s manifest SHA: %w", id, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != expected {
		return fmt.Errorf("plugin %s manifest SHA mismatch (cache tampered or upstream rolled): "+
			"want %s got %s; run `agentsync plugin upgrade %s` to accept the new manifest, "+
			"or set AGENTSYNC_ALLOW_PLUGIN_DRIFT=1 to bypass this check", id, expected, got, id)
	}
	return nil
}
