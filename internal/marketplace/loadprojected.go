package marketplace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	c, err := source.Load(fs, home)
	if err != nil {
		return c, err
	}
	if pluginCacheRoot == "" {
		return c, nil
	}
	for _, pl := range c.Plugins {
		if pl.Plugin.Disabled {
			// `plugin disable <id>` — skip projection entirely.
			continue
		}
		id, _ := splitPluginRefPkg(pl.Plugin.ID)
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
		proj, perr := Project(resolveInstalledEntry(home, pl.Plugin.ID), pluginDir)
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
	return c, nil
}

// resolveInstalledEntry finds the marketplace entry for an installed plugin
// (id or id@marketplace) by scanning the cached marketplace.json files and
// matching on the marketplace's own `name` field plus the plugin name. This is
// what carries entry-level inline overrides into projection.
//
// On any failure (no marketplace cache, unparseable json, or the plugin not
// found — e.g. after `marketplace remove`) it falls back to a bare strict entry
// so projection degrades to plugin.json-only rather than failing the whole
// load. The entry reflects the marketplace's CURRENT version of the plugin,
// which can differ from the installed version until the next `update`.
func resolveInstalledEntry(home, pluginID string) PluginEntry {
	id, mpName := splitPluginRefPkg(pluginID)
	if id == "" {
		id = pluginID
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
		if mpName != "" && mp.Name != mpName {
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

// verifyPluginManifestSHA checks the on-disk plugin.json against the SHA
// recorded in plugins/<id>.toml at install. A mismatch means the plugin cache
// was tampered with (or hand-edited) since install. Returns nil when: expected
// is empty (legacy/hand-managed), AGENTSYNC_ALLOW_PLUGIN_DRIFT=1, or the cached
// plugin.json is missing. (Moved here from the source loader so projection and
// verification live in one package.)
func verifyPluginManifestSHA(fs afero.Fs, pluginCacheDir, expected, id string) error {
	if expected == "" {
		return nil
	}
	if os.Getenv("AGENTSYNC_ALLOW_PLUGIN_DRIFT") == "1" {
		return nil
	}
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
