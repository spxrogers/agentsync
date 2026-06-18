package render

import (
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/untrusted"
)

// untrustedClassified classifies every string-shaped field on the translation
// report's row structs. The report is printed by apply/verify/explain, so any
// field carrying fetched plugin metadata must be untrusted.Text. See
// internal/source's TestUntrustedFieldGuard for the contract.
var untrustedClassified = map[string]map[string]bool{
	"PluginRow": {
		"Plugin":   true,  // plugin id from fetched marketplace metadata
		"Agent":    false, // trusted registry id (claude, opencode, …)
		"Coverage": false, // enum: full | partial | none | disabled
	},
	"SkipDetail": {
		"Component": false, // adapter-fixed component kind (mcp, command, …)
		"Name":      true,  // component name, plugin-derived (untrusted)
		"Reason":    false, // adapter-authored human reason (trusted)
	},
}

func stringShapedKind(t reflect.Type) bool { return t.Kind() == reflect.String }

func TestUntrustedFieldGuard(t *testing.T) {
	textType := reflect.TypeOf(untrusted.Text(""))
	stringType := reflect.TypeOf("")

	structs := map[string]reflect.Type{
		"PluginRow":  reflect.TypeOf(PluginRow{}),
		"SkipDetail": reflect.TypeOf(SkipDetail{}),
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
