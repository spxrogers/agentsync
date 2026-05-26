package cli

import (
	"sort"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// undeclaredNativePlugins reports, per agent, the plugins enabled in that
// agent's native config that are NOT declared in the canonical source — a
// read-only nudge surfaced by `status` and `doctor`.
//
// agentsync treats natively-installed plugins as foreign-managed (the design's
// "jointly-owned cache" note), so this never blocks or auto-imports; it just
// points the user at `import <agent>:plugin`. Only agents whose adapter
// implements adapter.PluginIngester are probed (others yield nothing), and a
// discovery error is skipped silently since the nudge is best-effort.
//
// Matching is by plugin NAME — the plugins/<name>.toml stem — not by
// "name@marketplace", because the marketplace id an agent records natively can
// differ from the declared marketplace name agentsync keys its file on. Name
// matching errs toward NOT nagging (a same-named plugin from another
// marketplace counts as declared), which suits a nudge.
func undeclaredNativePlugins(c source.Canonical, reg *adapter.Registry, agents []string) map[string][]string {
	declared := make(map[string]bool, len(c.Plugins))
	for _, pl := range c.Plugins {
		declared[pl.ID] = true
	}
	out := map[string][]string{}
	for _, name := range agents {
		pi, ok := reg.Lookup(name).(adapter.PluginIngester)
		if !ok {
			continue
		}
		_, plugins, err := pi.IngestPlugins(adapter.ScopeUser, "")
		if err != nil {
			continue
		}
		var missing []string
		seen := map[string]bool{}
		for _, pl := range plugins {
			if !pl.Enabled || declared[pl.Name] || seen[pl.Name] {
				continue
			}
			seen[pl.Name] = true
			missing = append(missing, pl.Name)
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			out[name] = missing
		}
	}
	return out
}
