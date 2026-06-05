package codex

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// IngestPlugins discovers the plugins Codex records in ~/.codex/config.toml. The
// read side of plugin `import`: each enabled plugin is mapped onto an agentsync
// marketplace source and `marketplace add` + `plugin install` are replayed.
//
// Codex records per-plugin enable-state under quoted `[plugins."<name>@<source>"]`
// tables (an `enabled` bool; default-on when the field is absent) — the same
// `name@source` shape as Claude's `enabledPlugins`. Unlike Claude, Codex does
// NOT record a marketplace's fetch source in a documented config location
// (marketplaces are added with `codex plugin marketplace add <source>` and read
// from fixed paths like ~/.agents/plugins/marketplace.json), so this returns NO
// NativeMarketplaces. `import` then resolves each plugin's marketplace from
// agentsync's own registered marketplaces (run `agentsync marketplace add
// <source>` first), warning + skipping any it cannot resolve — exactly how
// Claude's auto-available built-in marketplace is handled.
//
// Parsing is lenient: a missing config.toml yields no plugins, and a malformed
// one is treated as "no plugins discovered" rather than failing the whole
// import. Only a genuine read error (e.g. a permission problem) is surfaced.
func (a *Adapter) IngestPlugins(scope adapter.Scope, project string) ([]adapter.NativeMarketplace, []adapter.NativePlugin, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, nil, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	data, err := os.ReadFile(p.Config)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read %s: %w", p.Config, err)
	}
	var top map[string]any
	if toml.Unmarshal(data, &top) != nil {
		return nil, nil, nil
	}
	return nil, parsePluginTables(top["plugins"]), nil
}

// parsePluginTables decodes the config.toml `[plugins.*]` tables into
// NativePlugin records, sorted by (name, marketplace) for deterministic output.
// A table present without an explicit `enabled = false` counts as enabled.
func parsePluginTables(v any) []adapter.NativePlugin {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make([]adapter.NativePlugin, 0, len(m))
	for key, raw := range m {
		name, mpID := splitPluginKey(key)
		out = append(out, adapter.NativePlugin{
			Name:          name,
			MarketplaceID: mpID,
			Enabled:       pluginEnabled(raw),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].MarketplaceID < out[j].MarketplaceID
	})
	return out
}

// splitPluginKey splits a plugins-table key "plugin@marketplace" into its parts.
// A key with no "@" yields an empty marketplace id (the CLI then warns it cannot
// resolve a source).
func splitPluginKey(key string) (name, mpID string) {
	if i := strings.LastIndex(key, "@"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// pluginEnabled reports whether a [plugins."x@y"] table means "enabled". An
// explicit `enabled = false` disables; anything else (absent field, true) is
// enabled — the table's presence is the install record.
func pluginEnabled(raw any) bool {
	entry, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	if e, ok := entry["enabled"].(bool); ok {
		return e
	}
	return true
}
