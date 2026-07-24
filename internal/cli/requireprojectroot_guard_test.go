package cli

import (
	"errors"
	"slices"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestEveryAdapterGuardsProjectRoot is the registry-wide behavioral guard that
// every registered adapter honors the (ScopeProject, "") boundary contract:
// Render, Ingest, and — for adapters implementing PluginIngester — IngestPlugins
// MUST return adapter.ErrProjectRootRequired when a project-scope call supplies
// no project root, rather than silently falling through to user-scope
// destinations. The per-adapter TestProjectScope_EmptyProjectErrors tests and
// the adapter-boundary TestRequireProjectRoot helper test each pin a piece; this
// closes the "a new adapter forgets to call RequireProjectRoot in one of its
// scope-resolving methods" gap by exhaustively driving the SAME registered set
// apply uses (via registryFactory).
//
// It mirrors the sibling TestEveryAdapterClassifiesSkips guard, including its
// non-vacuity backstop: the test fails if no adapters were checked at all, or
// if the observed PluginIngester implementors no longer include BOTH "claude"
// and "codex" — issue #131's acceptance criterion names those two by name, and
// a bare non-zero count would stay green if either one silently stopped
// implementing the interface. (A new PluginIngester beyond the two is fine;
// losing one of the two is not.)
func TestEveryAdapterGuardsProjectRoot(t *testing.T) {
	reg := registryFactory()

	var (
		offenders       []string
		adaptersChecked int
		pluginIngesters []string
	)

	for _, name := range reg.Names() {
		a := reg.Lookup(name)
		adaptersChecked++

		if _, _, err := a.Render(secrets.ForRender(source.Canonical{}), adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
			offenders = append(offenders, name+".Render did not return ErrProjectRootRequired at (ScopeProject, \"\"), got: "+errString(err))
		}
		if _, err := a.Ingest(adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
			offenders = append(offenders, name+".Ingest did not return ErrProjectRootRequired at (ScopeProject, \"\"), got: "+errString(err))
		}
		if pi, ok := a.(adapter.PluginIngester); ok {
			pluginIngesters = append(pluginIngesters, name)
			if _, _, err := pi.IngestPlugins(adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
				offenders = append(offenders, name+".IngestPlugins did not return ErrProjectRootRequired at (ScopeProject, \"\"), got: "+errString(err))
			}
		}
	}

	for _, o := range offenders {
		t.Errorf("project-root guard: %s", o)
	}
	// Guard against a vacuous pass: if no adapter was exercised, the assertions
	// above would be meaningless.
	if adaptersChecked == 0 {
		t.Fatal("no adapters registered — registryFactory produced an empty registry")
	}
	// Issue #131's acceptance criterion: at least claude AND codex implement
	// PluginIngester. Assert both BY NAME — a count-only backstop would stay
	// green if one of the two dropped the interface while the other kept it.
	for _, want := range []string{"claude", "codex"} {
		if !slices.Contains(pluginIngesters, want) {
			t.Fatalf("PluginIngester implementors = %v — %q is missing; issue #131 requires at least claude and codex to be exercised by the IngestPlugins branch of this guard", pluginIngesters, want)
		}
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
