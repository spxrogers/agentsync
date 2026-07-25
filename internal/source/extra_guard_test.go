package source_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// tomlPersistedPassthroughs is the authoritative allowlist backing capture's
// claim that "Extra is the only TOML-persisted passthrough surface"
// (normalizeExtraNumbers, internal/capture/capture.go): every map[string]any
// field that go-toml can marshal into the canonical source. Each entry is
// load-bearing twice over — the field's json.Number values are normalized by
// capture.normalizeExtraNumbers AND its strings are scanned by the leak
// backstop (secrets.ResidualSecretCleartext → scanExtraResidual). A NEW
// TOML-persisted map[string]any passthrough field must join both before being
// added here.
var tomlPersistedPassthroughs = map[string]bool{
	"MCPServerSpec.Extra": true,
	"LSPServerSpec.Extra": true,
}

// exemptedPassthroughs classifies the map[string]any fields that carry no
// toml:"-" opt-out yet are deliberately NOT TOML-persisted, with the reason —
// mirroring walkerCovered's "decide in one place" style (TestNewSecretFieldGuard,
// internal/secrets/walk_test.go). Skill.Frontmatter is excluded upstream by its
// explicit toml:"-" tag and never reaches this table.
var exemptedPassthroughs = map[string]string{
	// Subagent and Command render as markdown files with YAML frontmatter
	// (source.WriteSubagent / WriteCommand) — the structs never pass through
	// go-toml, so the json.Number→TOML-string hazard cannot arise, and their
	// frontmatter decodes via jsonkeys.DecodeYAML, which already normalizes
	// numbers.
	"Subagent.Frontmatter": "YAML frontmatter in a markdown file; never TOML-marshaled",
	"Command.Frontmatter":  "YAML frontmatter in a markdown file; never TOML-marshaled",
}

// isMapStringAny reports whether t is exactly map[string]any — the passthrough
// shape that preserves unmodeled native fields verbatim.
func isMapStringAny(t reflect.Type) bool {
	return t.Kind() == reflect.Map &&
		t.Key().Kind() == reflect.String &&
		t.Elem().Kind() == reflect.Interface &&
		t.Elem().NumMethod() == 0
}

// TestExtraIsOnlyTOMLPersistedPassthroughMap is the reflective guard for the
// "Extra is the only TOML-persisted passthrough surface" claim: it walks every
// struct type reachable from source.Canonical and, for each map[string]any
// field whose toml tag is not "-", asserts the field is classified — either as
// one of the two allowed Extra fields (MCPServerSpec/LSPServerSpec, which
// capture normalizes and the leak backstop scans) or as an explicitly exempted
// non-TOML surface. A future TOML-persisted map[string]any passthrough field
// fails this guard until whoever adds it decides, in one place, whether it
// joins the normalization + leak scanning or is exempted with a reason.
func TestExtraIsOnlyTOMLPersistedPassthroughMap(t *testing.T) {
	seen := map[string]bool{}
	visited := map[reflect.Type]bool{}
	var walk func(typ reflect.Type)
	walk = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			walk(typ.Elem())
		case reflect.Struct:
			if visited[typ] {
				return
			}
			visited[typ] = true
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				if isMapStringAny(f.Type) {
					key := typ.Name() + "." + f.Name
					seen[key] = true
					tag, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
					if tag == "-" {
						continue // explicitly opted out of TOML persistence
					}
					if tomlPersistedPassthroughs[key] {
						continue
					}
					if _, exempt := exemptedPassthroughs[key]; exempt {
						continue
					}
					t.Errorf("%s is an unclassified map[string]any passthrough (toml tag %q): "+
						"if it is TOML-persisted it MUST be normalized by capture.normalizeExtraNumbers "+
						"and scanned by the leak backstop (secrets.ResidualSecretCleartext), then added to "+
						"tomlPersistedPassthroughs; if it never passes through go-toml, exempt it in "+
						"exemptedPassthroughs with the reason (or tag it toml:\"-\")", key, f.Tag.Get("toml"))
				}
				walk(f.Type)
			}
		}
	}
	walk(reflect.TypeOf(source.Canonical{}))

	// Non-vacuity: every classified field must still exist where the tables
	// claim. A rename/move would otherwise leave a stale entry silently
	// vouching for a field the walk no longer finds.
	for key := range tomlPersistedPassthroughs {
		if !seen[key] {
			t.Errorf("tomlPersistedPassthroughs lists %s but the walk never found it — update the table", key)
		}
	}
	for key := range exemptedPassthroughs {
		if !seen[key] {
			t.Errorf("exemptedPassthroughs lists %s but the walk never found it — update the table", key)
		}
	}
}
