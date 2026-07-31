package source

// FilterForAgent returns c narrowed to the components the named agent should
// receive: every component provided by a plugin that does not target this agent
// (PluginTargetsAgent — the `agents` allowlist and the `native_agents` deferral)
// is dropped. Hand-authored components always survive.
//
// This is the ONE implementation of "what does this agent get". Two callers need
// it, and they must never disagree:
//
//   - render.Plan, via secrets.Resolved.ForAgent, narrows the model before the
//     adapter renders it — the enforcement point.
//   - import's pluginProvided narrows the projection before deciding which
//     native components it must refuse to capture.
//
// The second is not an optimization, it is correctness. That filter exists
// because an adapter's Ingest cannot tell a file agentsync rendered from a
// plugin apart from one the user hand-wrote, so import refuses to capture a
// component apply projects. If it used the UNfiltered projection it would refuse
// components this agent never receives — a plugin deferred to Claude's own
// plugin manager would block the user from importing their own same-named file
// out of Claude. The refusal set has to be exactly the render set, per agent.
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
