package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// IngestPlugins discovers the marketplaces and enabled plugins recorded in
// Claude's native settings.json — the documented `extraKnownMarketplaces` and
// `enabledPlugins` keys. It is the read side of plugin `import`: the CLI maps
// each result onto an agentsync marketplace source and replays
// `marketplace add` + `plugin install` to capture them.
//
// The built-in `claude-plugins-official` marketplace is auto-available in
// Claude and is NOT listed in extraKnownMarketplaces, so it has no resolvable
// source here. The CLI resolves such a marketplace from agentsync's own
// registered marketplaces instead (run `agentsync marketplace add <source>`);
// only a marketplace registered in neither place is warned about and skipped.
//
// Parsing is lenient: a missing settings.json yields no plugins, and a
// malformed one is treated as "no plugins discovered" rather than failing the
// whole import (matching the hooks/LSP blocks in Ingest). Only a genuine read
// error (e.g. a permission problem) is surfaced.
func (a *Adapter) IngestPlugins(scope adapter.Scope, project string) ([]adapter.NativeMarketplace, []adapter.NativePlugin, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, nil, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	data, err := os.ReadFile(p.Settings)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read %s: %w", p.Settings, err)
	}
	var top map[string]any
	if json.Unmarshal(data, &top) != nil {
		return nil, nil, nil
	}
	return parseExtraKnownMarketplaces(top["extraKnownMarketplaces"]),
		parseEnabledPlugins(top["enabledPlugins"]), nil
}

// parseExtraKnownMarketplaces decodes the settings.json `extraKnownMarketplaces`
// map into NativeMarketplace records, sorted by id for deterministic output.
func parseExtraKnownMarketplaces(v any) []adapter.NativeMarketplace {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make([]adapter.NativeMarketplace, 0, len(m))
	for id, raw := range m {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		src, ok := entry["source"].(map[string]any)
		if !ok {
			continue
		}
		out = append(out, adapter.NativeMarketplace{
			ID: id,
			Source: adapter.NativeSource{
				Type:    asStr(src["source"]),
				Repo:    asStr(src["repo"]),
				URL:     asStr(src["url"]),
				Path:    asStr(src["path"]),
				Ref:     asStr(src["ref"]),
				Package: asStr(src["package"]),
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// parseEnabledPlugins decodes the settings.json `enabledPlugins` map into
// NativePlugin records, sorted by (name, marketplace) for deterministic output.
func parseEnabledPlugins(v any) []adapter.NativePlugin {
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
			Enabled:       enabledTruthy(raw),
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

// splitPluginKey splits an enabledPlugins key "plugin@marketplace" into its
// parts. A key with no "@" yields an empty marketplace id (the CLI then warns
// it cannot resolve a source).
func splitPluginKey(key string) (name, mpID string) {
	if i := strings.LastIndex(key, "@"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// enabledTruthy reports whether an enabledPlugins value means "enabled". The
// schema allows a bool, an array of strings, or null: true or a non-empty array
// counts as enabled; false, null, and an empty array count as disabled.
func enabledTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case []any:
		return len(t) > 0
	default:
		return false
	}
}
