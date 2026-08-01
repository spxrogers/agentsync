package source_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// pluginComponentSet builds a canonical holding one component of every kind,
// each stamped as provided by `plugin` with the given targeting, plus one
// hand-authored component of every kind (no Plugin stamp). Both halves matter:
// the filter must drop the first when the agent is not targeted and must NEVER
// drop the second.
func pluginComponentSet(plugin string, agents, native []string) source.Canonical {
	return source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "mine"},
			{ID: "theirs", Plugin: plugin, PluginAgents: agents, PluginNativeAgents: native},
		},
		LSPServers: []source.LSPServer{
			{ID: "mine"},
			{ID: "theirs", Plugin: plugin, PluginAgents: agents, PluginNativeAgents: native},
		},
		Hooks: []source.Hook{
			{Event: "PreToolUse", Command: "mine"},
			{Event: "PreToolUse", Command: "theirs", Plugin: plugin, PluginAgents: agents, PluginNativeAgents: native},
		},
		Skills: []source.Skill{
			{Name: "mine"},
			{Name: "theirs", Plugin: plugin, PluginAgents: agents, PluginNativeAgents: native},
		},
		Subagents: []source.Subagent{
			{Name: "mine"},
			{Name: "theirs", Plugin: plugin, PluginAgents: agents, PluginNativeAgents: native},
		},
		Commands: []source.Command{
			{Name: "mine"},
			{Name: "theirs", Plugin: plugin, PluginAgents: agents, PluginNativeAgents: native},
		},
	}
}

// counts returns, per kind, how many components survived — as a flat slice in a
// fixed order so a single mismatch names the kind.
func counts(c source.Canonical) map[string]int {
	return map[string]int{
		"mcp":      len(c.MCPServers),
		"lsp":      len(c.LSPServers),
		"hook":     len(c.Hooks),
		"skill":    len(c.Skills),
		"subagent": len(c.Subagents),
		"command":  len(c.Commands),
	}
}

// TestFilterForAgent covers the two gates across every component kind at once.
// Per-kind coverage is the point: the filter is six near-identical closures, and
// the way it breaks is one kind silently not being filtered — which projects
// that kind to an agent the plugin was narrowed away from, re-creating the
// duplicate for exactly one component type.
func TestFilterForAgent(t *testing.T) {
	cases := []struct {
		name        string
		agents      []string
		native      []string
		target      string
		wantSurvive int // per kind: 2 = plugin component kept, 1 = only the hand-authored one
	}{
		{
			name:   "no targeting — the plugin reaches every agent",
			agents: nil, native: nil, target: "claude", wantSurvive: 2,
		},
		{
			name:   "wildcard allowlist reaches every agent",
			agents: []string{"*"}, target: "claude", wantSurvive: 2,
		},
		{
			name:   "allowlist naming this agent",
			agents: []string{"claude", "codex"}, target: "claude", wantSurvive: 2,
		},
		{
			name:   "allowlist excluding this agent",
			agents: []string{"codex"}, target: "claude", wantSurvive: 1,
		},
		{
			name:   "deferral naming this agent — it installs the plugin itself",
			agents: []string{"*"}, native: []string{"claude"}, target: "claude", wantSurvive: 1,
		},
		{
			name:   "deferral naming a DIFFERENT agent leaves this one alone",
			agents: []string{"*"}, native: []string{"codex"}, target: "claude", wantSurvive: 2,
		},
		{
			name:   "both gates must pass — allowlist ok, deferral claims it",
			agents: []string{"claude"}, native: []string{"claude"}, target: "claude", wantSurvive: 1,
		},
		{
			name:   `"*" in the deferral list is a literal, not a wildcard`,
			agents: []string{"*"}, native: []string{"*"}, target: "claude", wantSurvive: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := source.FilterForAgent(pluginComponentSet("toolkit", tc.agents, tc.native), tc.target)
			for kind, n := range counts(got) {
				if n != tc.wantSurvive {
					t.Errorf("%s: %d survived, want %d", kind, n, tc.wantSurvive)
				}
			}
			// The hand-authored half must survive every case, unconditionally —
			// the gates are properties of PLUGIN installation.
			if len(got.Skills) == 0 || got.Skills[0].Name != "mine" {
				t.Errorf("a hand-authored component was filtered: %+v", got.Skills)
			}
		})
	}
}

// TestFilterForAgent_FiltersTheProjectOverlay pins the recursion. The overlay is
// merged into the top-level slices before render, so this looks redundant — and
// it is exactly the kind of "belt and suspenders" that rots silently. A caller
// holding an UNMERGED project model would otherwise be handed the very component
// the top level just dropped.
func TestFilterForAgent_FiltersTheProjectOverlay(t *testing.T) {
	overlay := pluginComponentSet("toolkit", []string{"codex"}, nil)
	c := pluginComponentSet("toolkit", []string{"codex"}, nil)
	c.Project = &overlay

	got := source.FilterForAgent(c, "claude")
	if got.Project == nil {
		t.Fatal("the overlay was dropped entirely, not filtered")
	}
	for kind, n := range counts(*got.Project) {
		if n != 1 {
			t.Errorf("project overlay %s: %d survived, want 1 (only the hand-authored one)", kind, n)
		}
	}
}

// TestFilterForAgent_NoOpSharesTheInput documents the aliasing contract: when
// nothing is filtered the slices are returned as-is rather than copied. Callers
// must treat both models as read-only. If this ever needs to change, the change
// is a deliberate one — not something to discover from a mutation bug.
func TestFilterForAgent_NoOpSharesTheInput(t *testing.T) {
	c := pluginComponentSet("toolkit", []string{"*"}, nil)
	got := source.FilterForAgent(c, "claude")
	if len(got.Skills) != len(c.Skills) {
		t.Fatalf("nothing should have been filtered; got %d of %d skills", len(got.Skills), len(c.Skills))
	}
	c.Skills[0].Name = "mutated"
	if got.Skills[0].Name != "mutated" {
		t.Error("the no-op path is expected to share backing storage with its input; " +
			"if that changed deliberately, update this test and the FilterForAgent doc")
	}
}

// TestAgentTargeted covers the allowlist predicate on its own, including the
// documented default that an empty/absent list means every agent.
func TestAgentTargeted(t *testing.T) {
	cases := []struct {
		name   string
		agents []string
		agent  string
		want   bool
	}{
		{name: "nil means all", agents: nil, agent: "claude", want: true},
		{name: "empty means all", agents: []string{}, agent: "claude", want: true},
		{name: "wildcard", agents: []string{"*"}, agent: "claude", want: true},
		{name: "named", agents: []string{"codex", "claude"}, agent: "claude", want: true},
		{name: "not named", agents: []string{"codex"}, agent: "claude", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := source.AgentTargeted(tc.agents, tc.agent); got != tc.want {
				t.Errorf("AgentTargeted(%v, %q) = %v, want %v", tc.agents, tc.agent, got, tc.want)
			}
		})
	}
}

// TestPluginTargetsAgent_HandAuthoredAlwaysTargets pins the exemption at the
// predicate level, where it is one early return that a refactor could drop
// without any per-kind test noticing: a hand-authored component has no Plugin,
// so neither gate applies to it.
func TestPluginTargetsAgent_HandAuthoredAlwaysTargets(t *testing.T) {
	// Deliberately hostile inputs: an allowlist that excludes the agent AND a
	// deferral that claims it. With no plugin, neither may bite.
	if !source.PluginTargetsAgent("", []string{"codex"}, []string{"claude"}, "claude") {
		t.Error("a component with no providing plugin must render for every agent")
	}
	if source.PluginTargetsAgent("toolkit", []string{"codex"}, []string{"claude"}, "claude") {
		t.Error("the same targeting must bite when a plugin DOES provide the component")
	}
}

// TestPluginSpec_DeferredAgents pins the accessor that flattens the on-disk
// absent/empty distinction for consumers. Only the import prompt gate reads the
// pointer itself; everything else must see both as "defer to nobody".
func TestPluginSpec_DeferredAgents(t *testing.T) {
	empty := []string{}
	populated := []string{"claude"}
	if got := (source.PluginSpec{}).DeferredAgents(); got != nil {
		t.Errorf("absent key: got %v, want nil", got)
	}
	if got := (source.PluginSpec{NativeAgents: &empty}).DeferredAgents(); len(got) != 0 {
		t.Errorf("explicitly empty: got %v, want no agents", got)
	}
	if got := (source.PluginSpec{NativeAgents: &populated}).DeferredAgents(); len(got) != 1 || got[0] != "claude" {
		t.Errorf("populated: got %v, want [claude]", got)
	}
}
