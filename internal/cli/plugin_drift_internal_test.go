package cli

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/testenv"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// fakeIngester is a minimal PluginIngester, here only so
// unexaminedPluginAgents sees an agent WITH a native plugin manager. The
// reports that interrogate a PluginIngester's answers moved to
// internal/adapter and carry their own richer stub.
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

type fakePlain struct {
	adapter.Adapter
	name string
}

func (f *fakePlain) Name() string { return f.name }

// TestUnexaminedPluginAgents pins both properties of the scoping note's input
// set, DETERMINISTICALLY.
//
// The ordering half was previously pinned end-to-end by running `status` in a
// loop and hoping map iteration varied. It does not vary enough: Go's iteration
// over the small agents map is biased, so a sweep measured a dropped
// sort.Strings escaping ~36% of isolated runs — a coin flip dressed as a
// regression test. Calling the function directly with a REVERSED `enabled`
// makes the sort load-bearing on every run instead of most of them.
func TestUnexaminedPluginAgents(t *testing.T) {
	testenv.RequireContainer(t)
	reg := adapter.NewRegistry()
	for _, a := range []adapter.Adapter{
		&fakeIngester{name: "claude", enabled: true},
		&fakeIngester{name: "codex", enabled: true},
		&fakePlain{name: "opencode"},
	} {
		if err := reg.Register(a); err != nil {
			t.Fatal(err)
		}
	}

	// `enabled` deliberately arrives UNSORTED, as it does in status.go where it
	// is built by ranging a map. "opencode" is selected (so excluded as
	// examined); the other two are narrowed away.
	got := unexaminedPluginAgents(reg, []string{"codex", "opencode", "claude"}, []string{"opencode"})
	want := []string{"claude", "codex"}
	if len(got) != len(want) {
		t.Fatalf("unexamined = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexamined = %v, want %v (sorted); position %d differs", got, want, i)
		}
	}

	// The capability filter: a selected-away agent with no plugin manager is not
	// a gap, so it must not appear even though it was narrowed away.
	got = unexaminedPluginAgents(reg, []string{"claude", "opencode"}, []string{"claude"})
	if len(got) != 0 {
		t.Fatalf("opencode has no native plugin manager and cannot hide a duplicate; got %v", got)
	}
}

// TestLosslessTargetingCaveat pins the wording of the `--lossless` caveat.
//
// It is the whole point of the change: the refusal itself was already correct
// and already safe, but undiagnosable — a user saw an upgrade blocked over an
// agent they knew did not receive the plugin, with nothing in the message to
// search for. So the caveat has to name the gap (targeting is not honoured),
// give a way to check (`plugin explain`), and give a way out (drop the flag).
// A caveat missing any one of those is back to being a dead end.
//
// This half pins only what the const SAYS. That the const actually reaches the
// user is TestLosslessCaveatReachesTheUser (package cli_test), which keys on
// the `plugin explain` token required below — neither test is sufficient alone,
// and dropping either reopens a gap a deletion sweep found in both.
func TestLosslessTargetingCaveat(t *testing.T) {
	for _, want := range []string{
		// Both keys, as one phrase. Asserting "agents" separately would be
		// vacuous — it is a substring of "native_agents", so it passes on a
		// caveat that never mentions the allowlist at all.
		"`agents` / `native_agents`",
		"plugin explain", // how to check whether the agent even receives it
		"--lossless",     // how to proceed once you know
	} {
		if !strings.Contains(losslessTargetingCaveat, want) {
			t.Errorf("the caveat must mention %q; got:\n%s", want, losslessTargetingCaveat)
		}
	}
	// It must not read as a bug report about the plugin: the refusal is safe,
	// and the user's upgrade may be perfectly fine.
	if strings.Contains(strings.ToLower(losslessTargetingCaveat), "error") {
		t.Errorf("the caveat qualifies a safe refusal; it should not read as an error:\n%s", losslessTargetingCaveat)
	}
}
