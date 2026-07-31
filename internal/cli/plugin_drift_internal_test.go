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
}

func (f *fakeIngester) Name() string { return f.name }

func (f *fakeIngester) IngestPlugins(adapter.Scope, string) ([]adapter.NativeMarketplace, []adapter.NativePlugin, error) {
	return nil, []adapter.NativePlugin{{
		Name: untrusted.Wrap("toolkit"), MarketplaceID: "mp", Enabled: f.enabled,
	}}, nil
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
