package marketplace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/source"
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
// to skip projecting — the plugins a project marker's `[plugins] disabled`
// list names. Projection runs BEFORE project.Merge, so a marker-disabled
// plugin whose components were already projected would still render even
// though Merge drops its c.Plugins record; skipping projection here is what
// actually suppresses the components.
//
// disabled is matched against pl.ID (the plugins/<id>.toml filename stem) —
// exactly the key project.Merge filters records on (see project.Merge's
// Plugins.Disabled block) — so the projection-skip and the record-filter can
// never disagree (a half-disable that drops the record but ships the
// components, or vice versa).
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
	disabledByMarker := make(map[string]bool, len(disabled))
	for _, id := range disabled {
		disabledByMarker[id] = true
	}
	for _, pl := range c.Plugins {
		if pl.Plugin.Disabled {
			// `plugin disable <id>` — skip projection entirely.
			continue
		}
		if disabledByMarker[pl.ID] {
			// `[plugins] disabled` in the active project marker — skip
			// projection so the plugin's components never render here.
			continue
		}
		id, mpName := splitPluginRefPkg(pl.Plugin.ID)
		if id == "" {
			id = pl.ID
		}
		// Defense-in-depth: a hand-edited plugins/<id>.toml whose id contains
		// "../" must not let plugin.json reads escape the cache root.
		if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
			return c, fmt.Errorf("project plugin %q: id contains a path-traversal component", id)
		}
		pluginDir := filepath.Join(pluginCacheRoot, id)
		if err := verifyPluginManifestSHA(fs, pluginDir, pl.Plugin.ManifestSHA, id); err != nil {
			return c, err
		}
		proj, perr := projectWithFuncs(resolveInstalledEntry(home, id, mpName), pluginDir, os.ReadFile, os.ReadDir, lenient)
		if perr != nil {
			return c, fmt.Errorf("project plugin %s: %w", id, perr)
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
	return nil
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
// headers). The source-only `agents`/`enabled` targeting metadata is excluded:
// render strips it and capture preserves it, so two sources differing ONLY on it
// are not a divergent override. nil and empty collections compare equal.
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
func resolveInstalledEntry(home, id, mpName string) PluginEntry {
	if mpName == "" {
		return PluginEntry{Name: id}
	}
	cacheRoot := filepath.Join(home, ".state", "cache", "marketplaces")
	dirs, err := os.ReadDir(cacheRoot)
	if err != nil {
		return PluginEntry{Name: id}
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(cacheRoot, d.Name(), ".claude-plugin", "marketplace.json"))
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
			if e.Name == id {
				return e
			}
		}
	}
	return PluginEntry{Name: id}
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
// plugin.json and cannot certify the bodies, so it is REFUSED with a re-pin
// instruction rather than silently honoured.
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
	// offered remediation (`agentsync update`) does not re-pin a non-bumping
	// plugin. Re-installing or `agentsync plugin upgrade <id>` rewrites the pin
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
