package cli

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter/generic"
)

// TestRegistryFactory_AllAgentNamesAreRegistered is the C3 dual-list subset
// guard: every name the CLI accepts via validateAgent (allAgentNames) MUST have
// a real registered adapter, or apply/status/diff would fail for that one agent
// while agent add reported it as valid. Pure in-memory (no FS) — no container
// guard required.
func TestRegistryFactory_AllAgentNamesAreRegistered(t *testing.T) {
	reg := registryFactory()
	registered := make(map[string]bool, len(reg.Names()))
	for _, n := range reg.Names() {
		registered[n] = true
	}
	for _, name := range allAgentNames() {
		if !registered[name] {
			t.Errorf("agent %q is accepted by validateAgent (allAgentNames) but has no "+
				"registered adapter; apply/status/diff would fail for it", name)
		}
	}
}

// TestRegistryFactory_NoDuplicateOrMissingAdapters pins the inverse direction:
// the production wiring must construct without a collision (which now panics),
// contain exactly the expected number of adapters, and hold no duplicate names.
func TestRegistryFactory_NoDuplicateOrMissingAdapters(t *testing.T) {
	// Building the production factory must not panic (a Register collision now
	// panics rather than silently mis-registering).
	reg := registryFactory()

	names := reg.Names()
	want := len(deepAdapterNames) + len(generic.Specs())
	if len(names) != want {
		t.Errorf("registryFactory registered %d adapters, want %d (%d deep + %d generic specs)",
			len(names), want, len(deepAdapterNames), len(generic.Specs()))
	}

	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n] {
			t.Errorf("registryFactory registered duplicate adapter name %q", n)
		}
		seen[n] = true
	}
}
