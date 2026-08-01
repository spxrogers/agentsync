package cli

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// fakeIngester is a minimal PluginIngester reporting one plugin, enabled or not.
// Driving the real adapters would need a whole native config on disk per case;
// this isolates the gate logic, which is what these tests are about. The
// embedded adapter.Adapter is nil — only Name() and IngestPlugins are called.
type fakeIngester struct {
	adapter.Adapter
	name    string
	enabled bool
	// alsoNative are extra plugins this agent installs natively, used to reach
	// the per-plugin gates: with only ONE plugin, an "undeclared" case exits at
	// the len(declared)==0 short-circuit and never reaches them.
	alsoNative []string
}

func (f *fakeIngester) Name() string { return f.name }

func (f *fakeIngester) IngestPlugins(adapter.Scope, string) ([]adapter.NativeMarketplace, []adapter.NativePlugin, error) {
	out := []adapter.NativePlugin{{
		Name: untrusted.Wrap("toolkit"), MarketplaceID: "mp", Enabled: f.enabled,
	}}
	for _, extra := range f.alsoNative {
		out = append(out, adapter.NativePlugin{
			Name: untrusted.Wrap(extra), MarketplaceID: "mp", Enabled: true,
		})
	}
	return nil, out, nil
}

// TestDuplicatedNativePlugins covers every gate deciding whether a plugin is
// reported as installed-in-an-agent AND projected there. Each gate is a way to
// produce a FALSE warning — telling a user to uninstall a plugin from an agent
// where nothing is duplicated — and removing any of them was green under
// mutation before this test existed.
//
// The agent-enabled gate is the one that bit in practice: `status` passes its
// selected set, but `doctor` passes EVERY registered adapter, so with the check
// left to the caller doctor warned about agents agentsync does not render to at
// all, and told the user to uninstall working functionality.
func TestDuplicatedNativePlugins(t *testing.T) {
	testenv.RequireContainer(t)
	claudeOnly := []string{"claude"}
	cases := []struct {
		name        string
		agentOn     bool
		nativelyOn  bool
		declared    bool
		pluginOff   bool
		nativeAgent *[]string
		wantWarn    bool
	}{
		{name: "installed natively and projected — the duplicate", agentOn: true, nativelyOn: true, declared: true, wantWarn: true},
		{name: "agent not enabled in agentsync — nothing is projected there", agentOn: false, nativelyOn: true, declared: true},
		{name: "not installed natively — there is no second copy", agentOn: true, nativelyOn: false, declared: true},
		{name: "not declared in agentsync — that is the UNDECLARED nudge, not this one", agentOn: true, nativelyOn: true},
		{name: "plugin disabled in agentsync — it projects nothing anywhere", agentOn: true, nativelyOn: true, declared: true, pluginOff: true},
		{name: "already deferred — that is the remedy, not the problem", agentOn: true, nativelyOn: true, declared: true, nativeAgent: &claudeOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := source.Canonical{Config: source.Config{
				Agents: map[string]source.Agent{"claude": {Enabled: tc.agentOn}},
			}}
			if tc.declared {
				c.Plugins = []source.Plugin{{
					ID: untrusted.Wrap("toolkit"),
					Plugin: source.PluginSpec{
						ID:           untrusted.Wrap("toolkit@mp"),
						Agents:       []string{"*"},
						NativeAgents: tc.nativeAgent,
						Disabled:     tc.pluginOff,
					},
				}}
			}
			reg := adapter.NewRegistry()
			if err := reg.Register(&fakeIngester{name: "claude", enabled: tc.nativelyOn}); err != nil {
				t.Fatal(err)
			}
			got := duplicatedNativePlugins(c, reg, []string{"claude"})
			if warned := len(got["claude"]) > 0; warned != tc.wantWarn {
				t.Errorf("warned = %v, want %v (got %v)", warned, tc.wantWarn, got)
			}
		})
	}
}

// TestDuplicatedNativePlugins_IgnoresUndeclaredPlugins reaches the per-plugin
// "is this declared in agentsync?" gate, which the matrix above cannot: with a
// single undeclared plugin the function short-circuits on an empty declared set
// long before that gate, so removing it stayed green.
//
// A plugin agentsync does not manage is the UNDECLARED nudge's business ("you
// could import this"), never this warning's — claiming agentsync duplicates a
// plugin it has never heard of, and telling the user to uninstall it, is the
// same false "uninstall this" advice the agent-enabled gate produced.
func TestDuplicatedNativePlugins_IgnoresUndeclaredPlugins(t *testing.T) {
	testenv.RequireContainer(t)
	c := source.Canonical{
		Config: source.Config{Agents: map[string]source.Agent{"claude": {Enabled: true}}},
		Plugins: []source.Plugin{{
			ID:     untrusted.Wrap("toolkit"),
			Plugin: source.PluginSpec{ID: untrusted.Wrap("toolkit@mp"), Agents: []string{"*"}},
		}},
	}
	reg := adapter.NewRegistry()
	// claude installs the declared plugin AND a foreign one agentsync knows
	// nothing about.
	if err := reg.Register(&fakeIngester{name: "claude", enabled: true, alsoNative: []string{"foreign"}}); err != nil {
		t.Fatal(err)
	}
	got := duplicatedNativePlugins(c, reg, []string{"claude"})["claude"]
	if len(got) != 1 || got[0].Unverified() != "toolkit" {
		t.Fatalf("only the DECLARED plugin duplicates; got %v", got)
	}
}

// TestNativePluginOwners_DedupesPerAgent pins the dedupe. One agent can report
// the same plugin NAME from two marketplaces, and the result is written into
// plugins/<id>.toml — usually a committed dotfiles repo — so a duplicated entry
// (`native_agents = ["claude","claude"]`) would be committed and shown back to
// the user on every subsequent install summary.
func TestNativePluginOwners_DedupesPerAgent(t *testing.T) {
	testenv.RequireContainer(t)
	reg := adapter.NewRegistry()
	// alsoNative repeats "toolkit", i.e. the same plugin name a second time from
	// this one agent — what two marketplaces carrying the same name looks like.
	if err := reg.Register(&fakeIngester{name: "claude", enabled: true, alsoNative: []string{"toolkit"}}); err != nil {
		t.Fatal(err)
	}
	if got := nativePluginOwners(reg)["toolkit"]; len(got) != 1 {
		t.Fatalf("owners = %v, want one entry per AGENT", got)
	}
}

// TestNativePluginOwners_ProbesEveryIngester pins the breadth of the probe that
// seeds `native_agents`. Restricting it to the agent being imported FROM passed
// the entire suite: no fixture installs one plugin natively in two harnesses,
// which is exactly the case the breadth exists for. A plugin installed in both
// Claude and Codex needs BOTH deferrals — recording only the one you imported
// from leaves the other duplicating silently.
func TestNativePluginOwners_ProbesEveryIngester(t *testing.T) {
	testenv.RequireContainer(t)
	reg := adapter.NewRegistry()
	for _, f := range []*fakeIngester{
		{name: "claude", enabled: true},
		{name: "codex", enabled: true},
		{name: "opencode", enabled: false}, // an ingester that does NOT have it
	} {
		if err := reg.Register(f); err != nil {
			t.Fatal(err)
		}
	}
	got := nativePluginOwners(reg)["toolkit"]
	if len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Fatalf("owners = %v, want both harnesses that install it natively [claude codex]", got)
	}
}
