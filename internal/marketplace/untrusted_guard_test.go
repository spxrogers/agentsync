package marketplace

import (
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/untrusted"
)

// untrustedClassified classifies every string-shaped field on the
// fetched-metadata structs: untrusted (true → untrusted.Text, sanitized on
// print) or deliberately trusted/plain (false). TestUntrustedFieldGuard fails on
// any unclassified field, so a new field carrying marketplace.json / report data
// can't slip in as a raw string that a print site would leak. Mirrors
// internal/source's guard and secrets.TestNewSecretFieldGuard.
var untrustedClassified = map[string]map[string]bool{
	"Bump": {
		"ID":         true,  // plugin id (fetched/installed)
		"From":       true,  // installed version
		"To":         true,  // candidate version (fetched)
		"UpdateMode": false, // enum: pinned | track | manual
	},
	"SHAWarning": {
		"ID":          true,  // plugin id
		"Version":     true,  // re-uploaded version
		"RecordedSHA": false, // agentsync-computed hash
		"FetchedSHA":  false, // agentsync-computed hash
	},
	"PluginEntry": {
		"Name":    true, // marketplace.json plugin name (printed)
		"Version": true, // marketplace.json plugin version (printed)
		// The remaining strings come from marketplace.json too but are free-text
		// metadata NOT rendered into any terminal layout today. They are a
		// deliberate plain-string subset: promote one to untrusted.Text BEFORE
		// adding a print site for it (the guard cannot catch a leak of a plain
		// string). Kept plain to bound the refactor to the printed fields #102
		// scopes (ids/versions/names).
		"Description": false,
		"Homepage":    false,
		"Repository":  false,
		"License":     false,
		"Category":    false,
	},
}

func stringShapedKind(t reflect.Type) bool { return t.Kind() == reflect.String }

// TestUntrustedFieldGuard — see internal/source's identically-named guard for
// the contract. It fails on an unclassified string-shaped field or a
// type/classification mismatch.
func TestUntrustedFieldGuard(t *testing.T) {
	textType := reflect.TypeOf(untrusted.Text(""))
	stringType := reflect.TypeOf("")

	structs := map[string]reflect.Type{
		"Bump":        reflect.TypeOf(Bump{}),
		"SHAWarning":  reflect.TypeOf(SHAWarning{}),
		"PluginEntry": reflect.TypeOf(PluginEntry{}),
	}

	for name, typ := range structs {
		cover := untrustedClassified[name]
		if cover == nil {
			t.Fatalf("struct %s has no classification map", name)
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !stringShapedKind(f.Type) {
				continue
			}
			want, ok := cover[f.Name]
			if !ok {
				t.Errorf("%s.%s is a string-shaped field with no untrusted classification; "+
					"add it to untrustedClassified (true → untrusted.Text, false → plain string)", name, f.Name)
				continue
			}
			switch {
			case want && f.Type != textType:
				t.Errorf("%s.%s is classified untrusted but is %s, want untrusted.Text", name, f.Name, f.Type)
			case !want && f.Type != stringType:
				t.Errorf("%s.%s is classified trusted but is %s, want plain string", name, f.Name, f.Type)
			}
		}
	}
}
