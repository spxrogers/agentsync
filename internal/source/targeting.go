package source

// FilterForAgent returns c narrowed to the components the named agent should
// receive: every component provided by a plugin that does not target this agent
// (PluginTargetsAgent — the `agents` allowlist and the `native_agents` deferral)
// is dropped. Hand-authored components always survive.
//
// This is the ONE implementation of "what does this agent get", and it has
// exactly ONE caller: render.Plan, via secrets.Resolved.ForAgent, narrowing the
// model before the adapter renders it. That is the enforcement point.
//
// import's capture-refusal filter (pluginProvided) deliberately does NOT use it.
// Symmetry is tempting — refuse exactly what you render — but the destination
// can still hold agentsync's own un-reclaimed output from before a deferral was
// recorded, and a narrowed refusal set would capture that back into the
// canonical source as if the user had written it. See the note at that call
// site; the asymmetry is load-bearing, not an oversight.
//
// The result SHARES component values with c (the filter rebuilds slices, it does
// not deep-copy), so callers must treat both as read-only — which every caller
// already does.
func FilterForAgent(c Canonical, agent string) Canonical {
	out := c
	out.MCPServers = filterSlice(c.MCPServers, func(m MCPServer) bool {
		return PluginTargetsAgent(m.Plugin, m.PluginAgents, m.PluginNativeAgents, agent)
	})
	out.LSPServers = filterSlice(c.LSPServers, func(l LSPServer) bool {
		return PluginTargetsAgent(l.Plugin, l.PluginAgents, l.PluginNativeAgents, agent)
	})
	out.Hooks = filterSlice(c.Hooks, func(h Hook) bool {
		return PluginTargetsAgent(h.Plugin, h.PluginAgents, h.PluginNativeAgents, agent)
	})
	out.Skills = filterSlice(c.Skills, func(s Skill) bool {
		return PluginTargetsAgent(s.Plugin, s.PluginAgents, s.PluginNativeAgents, agent)
	})
	out.Subagents = filterSlice(c.Subagents, func(s Subagent) bool {
		return PluginTargetsAgent(s.Plugin, s.PluginAgents, s.PluginNativeAgents, agent)
	})
	out.Commands = filterSlice(c.Commands, func(cmd Command) bool {
		return PluginTargetsAgent(cmd.Plugin, cmd.PluginAgents, cmd.PluginNativeAgents, agent)
	})
	// The project overlay is merged into the top-level slices before render
	// (project.Merge), so filtering it is belt-and-suspenders for a caller
	// holding an unmerged model — but it must stay in step: an overlay left
	// unfiltered would hand an adapter the very component the top level dropped.
	if c.Project != nil {
		p := FilterForAgent(*c.Project, agent)
		out.Project = &p
	}
	return out
}

// filterSlice returns the elements of in satisfying keep, preserving order. A
// nil input stays nil, and a slice that loses nothing is returned as-is, so the
// common case (no plugins, or every plugin targeting every agent) allocates
// nothing.
func filterSlice[T any](in []T, keep func(T) bool) []T {
	drop := 0
	for _, v := range in {
		if !keep(v) {
			drop++
		}
	}
	if drop == 0 {
		return in
	}
	out := make([]T, 0, len(in)-drop)
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}
