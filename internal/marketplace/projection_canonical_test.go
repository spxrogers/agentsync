package marketplace_test

import (
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestProjectionResultAsCanonicalCoversEveryField is the reflective guard for
// the six-field copy: every exported field of ProjectionResult must reach the
// source.Canonical that AsCanonical() returns.
//
// This is the CLAUDE.md "model drifts from its artifact" class in miniature. The
// copy used to live in three CLI call sites, so adding a seventh component kind
// to ProjectionResult would have left all three compiling and silently dropping
// it — a plugin's new component kind would project into the plan for a real
// apply and vanish from `explain`, `plugin explain` and `plugin poll`'s
// lossiness probe, with nothing to fail.
//
// The guard works by NAME, not by position: it fills every field of a fresh
// ProjectionResult with a two-element slice, runs the copy, and requires the
// same-named field on source.Canonical to be non-empty afterwards. A field
// added to ProjectionResult with no Canonical counterpart fails the lookup arm
// instead — which is the right failure: a projected component kind the canonical
// model cannot hold is a schema change, not a copy bug.
//
// It also pins the documented aliasing contract: both methods copy slice
// headers, so each copied list must share its backing array with the
// projection's. A deep copy would pass the coverage arm and silently change
// what a caller that later appends to or mutates the projection observes.
// Each field is filled with TWO elements, not one, so a copy of a one-element
// prefix (r.X[:1] — same first-element address, wrong length) fails the length
// arm rather than passing the aliasing arm. A zero-sized element type would
// let a fresh MakeSlice and a deep copy share the runtime's zero-size base
// address and pass the aliasing arm vacuously; every component type is a
// struct with fields, so none is zero-sized today.
func TestProjectionResultAsCanonicalCoversEveryField(t *testing.T) {
	var r marketplace.ProjectionResult
	rv := reflect.ValueOf(&r).Elem()
	rt := rv.Type()

	// Fill each field with exactly two zero-valued elements, so "did it arrive"
	// is a length check that cannot be satisfied by a nil slice or by a
	// one-element prefix of the right backing array.
	for i := 0; i < rt.NumField(); i++ {
		f := rv.Field(i)
		if f.Kind() != reflect.Slice {
			t.Fatalf("%s: ProjectionResult fields are component slices; %s is %s — "+
				"teach this guard what a non-slice field means before adding one",
				rt.Field(i).Name, rt.Field(i).Name, f.Kind())
		}
		f.Set(reflect.MakeSlice(f.Type(), 2, 2))
	}
	if rt.NumField() == 0 {
		t.Fatal("ProjectionResult has no fields — the guard would pass vacuously")
	}

	check := func(t *testing.T, what string, got source.Canonical) {
		t.Helper()
		cv := reflect.ValueOf(got)
		for i := 0; i < rt.NumField(); i++ {
			name := rt.Field(i).Name
			cf := cv.FieldByName(name)
			if !cf.IsValid() {
				t.Errorf("%s: ProjectionResult.%s has no source.Canonical.%s to copy into — "+
					"a projected component kind the canonical model cannot hold is a schema change",
					what, name, name)
				continue
			}
			if cf.Len() != 2 {
				t.Errorf("%s: ProjectionResult.%s was not copied whole (Canonical.%s has %d entries, want 2) — "+
					"add it to ProjectionResult.%s, as a header copy of the full list", what, name, name, cf.Len(), what)
				continue
			}
			if cf.Pointer() != rv.Field(i).Pointer() {
				t.Errorf("%s: ProjectionResult.%s was deep-copied — the documented contract is a header copy "+
					"that aliases the projection's backing array", what, name)
			}
		}
	}

	check(t, "AsCanonical()", r.AsCanonical())

	// ReplaceComponentsIn must cover exactly the same set, and must leave the
	// non-component fields of its target alone — that is the whole reason it
	// exists rather than an assignment.
	target := source.Canonical{
		Config: source.Config{Agents: map[string]source.Agent{"claude": {Enabled: true}}},
		Memory: source.Memory{Body: "keep me"},
		// THREE plugins, not two: the "was it copied" arm below looks for
		// exactly two elements, so a pre-filled field of length two could mask
		// a field ReplaceComponentsIn forgot to copy.
		Plugins: []source.Plugin{{}, {}, {}},
	}
	r.ReplaceComponentsIn(&target)
	check(t, "ReplaceComponentsIn()", target)
	if target.Memory.Body != "keep me" {
		t.Errorf("ReplaceComponentsIn clobbered Memory: %q", target.Memory.Body)
	}
	if len(target.Config.Agents) != 1 {
		t.Errorf("ReplaceComponentsIn clobbered Config.Agents: %v", target.Config.Agents)
	}
	if len(target.Plugins) != 3 {
		t.Errorf("ReplaceComponentsIn clobbered Plugins: %v", target.Plugins)
	}
}
