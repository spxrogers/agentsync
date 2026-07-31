package cli

import (
	"slices"
	"sort"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// nativePluginOwners maps a plugin NAME to the agents whose own plugin manager
// already has it enabled. It is the input to `native_agents`: an agent that
// installs a plugin itself must not also receive agentsync's projection of that
// plugin's components, or every skill/subagent/command lands twice and every
// hook fires twice (the agent serves its own copy from its install dir, which
// agentsync neither writes nor reads).
//
// Every registered PluginIngester is probed, not just the agent being imported
// from: a plugin can be installed natively in several harnesses, and each of
// them needs its own deferral. Agents that are not currently enabled in
// agentsync are probed too — excluding one costs nothing today and is already
// correct if the user enables it later.
//
// Matching is by NAME (the plugins/<id>.toml stem), mirroring
// undeclaredNativePlugins: the marketplace id an agent records natively can
// differ from the declared marketplace name agentsync keys its file on. A
// discovery error is skipped silently — a missing or malformed native config
// means "no plugins discovered", never a failed import.
func nativePluginOwners(reg *adapter.Registry) map[string][]string {
	out := map[string][]string{}
	names := reg.Names()
	sort.Strings(names)
	for _, agentName := range names {
		pi, ok := reg.Lookup(agentName).(adapter.PluginIngester)
		if !ok {
			continue
		}
		_, plugins, err := pi.IngestPlugins(adapter.ScopeUser, "")
		if err != nil {
			continue
		}
		for _, pl := range plugins {
			if !pl.Enabled {
				continue
			}
			key := pl.Name.Unverified()
			if !slices.Contains(out[key], agentName) {
				out[key] = append(out[key], agentName)
			}
		}
	}
	return out
}

// duplicatedNativePlugins reports, per agent, the declared plugins that agent
// installs ITSELF and that agentsync also projects to it — the duplicate: every
// skill, subagent, and command lands twice (once from the agent's own install
// dir, once from agentsync's projection into the agent's standalone paths) and
// every hook handler fires twice.
//
// It is the counterpart to undeclaredNativePlugins, and the safety net for a
// deliberate design choice: apply's output is a pure function of canonical state
// and never probes the destination, so a plugin installed natively AFTER it was
// declared in agentsync cannot be noticed at apply time. `import` seeds
// `native_agents` for what exists at import time; this catches everything after.
//
// A plugin is duplicated for an agent when all of:
//   - the agent's native config has it enabled (matched by NAME, the
//     plugins/<id>.toml stem, as in undeclaredNativePlugins);
//   - the canonical source declares it and it is not disabled;
//   - agentsync projects it to this agent — i.e. the `agents` allowlist targets
//     the agent and `native_agents` does NOT already defer to it.
//
// Read-only and best-effort: a discovery error is skipped, never surfaced as a
// failure, exactly like the sibling nudge.
func duplicatedNativePlugins(c source.Canonical, reg *adapter.Registry, agents []string) map[string][]untrusted.Text {
	declared := make(map[string]source.PluginSpec, len(c.Plugins))
	for _, pl := range c.Plugins {
		if !pl.Plugin.Disabled {
			declared[pl.ID.Unverified()] = pl.Plugin
		}
	}
	if len(declared) == 0 {
		return nil
	}
	out := map[string][]untrusted.Text{}
	for _, name := range agents {
		// The claim this report makes is about agentsync's OWN projection, so an
		// agent agentsync does not render to cannot be duplicating anything —
		// however its native config looks. Checked HERE rather than left to the
		// caller: `status` passes its selected set but `doctor` passes every
		// registered adapter, and the difference produced a warning telling users
		// to uninstall a plugin from an agent agentsync never touches.
		if !c.Config.Agents[name].Enabled {
			continue
		}
		pi, ok := reg.Lookup(name).(adapter.PluginIngester)
		if !ok {
			continue
		}
		_, plugins, err := pi.IngestPlugins(adapter.ScopeUser, "")
		if err != nil {
			continue
		}
		var dupes []untrusted.Text
		seen := map[string]bool{}
		for _, pl := range plugins {
			key := pl.Name.Unverified()
			spec, isDeclared := declared[key]
			if !pl.Enabled || !isDeclared || seen[key] {
				continue
			}
			// The plugin id is non-empty by construction here (it keys a declared
			// plugins/<id>.toml), so this asks the real question: does agentsync
			// project this plugin to this agent?
			if !source.PluginTargetsAgent(key, spec.Agents, spec.DeferredAgents(), name) {
				continue
			}
			seen[key] = true
			dupes = append(dupes, pl.Name)
		}
		if len(dupes) > 0 {
			sort.Slice(dupes, func(i, j int) bool { return dupes[i] < dupes[j] })
			out[name] = dupes
		}
	}
	return out
}

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
