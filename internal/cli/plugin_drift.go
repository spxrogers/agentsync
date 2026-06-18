package cli

import (
	"sort"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
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
// The native plugin name is carried as untrusted.Text end to end (a plugin
// author influences it, and status/doctor print it), so matching/dedup keys off
// its raw Unverified() value while the value handed to the print sites keeps the
// Text wrapper and sanitizes on display.
func undeclaredNativePlugins(c source.Canonical, reg *adapter.Registry, agents []string) map[string][]untrusted.Text {
	declared := make(map[string]bool, len(c.Plugins))
	for _, pl := range c.Plugins {
		declared[pl.ID.Unverified()] = true
	}
	out := map[string][]untrusted.Text{}
	for _, name := range agents {
		pi, ok := reg.Lookup(name).(adapter.PluginIngester)
		if !ok {
			continue
		}
		_, plugins, err := pi.IngestPlugins(adapter.ScopeUser, "")
		if err != nil {
			continue
		}
		var missing []untrusted.Text
		seen := map[string]bool{}
		for _, pl := range plugins {
			key := pl.Name.Unverified()
			if !pl.Enabled || declared[key] || seen[key] {
				continue
			}
			seen[key] = true
			missing = append(missing, pl.Name)
		}
		if len(missing) > 0 {
			sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
			out[name] = missing
		}
	}
	return out
}
