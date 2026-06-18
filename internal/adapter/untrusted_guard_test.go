package adapter_test

import (
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// untrustedClassified classifies every string-shaped field on the native-ingest
// descriptor an adapter's IngestPlugins returns: untrusted (true → untrusted.Text,
// sanitized on print) or deliberately trusted/plain (false). TestUntrustedFieldGuard
// fails on any unclassified field, so a future native-config-derived string can't
// ship as a raw string that a `status`/`doctor` print site would leak. Mirrors the
// identically-named guards in internal/{source,marketplace,render}.
var untrustedClassified = map[string]map[string]bool{
	"NativePlugin": {
		// The plugin name is read back from the agent's own config (a plugin author
		// influences it) and printed in the status/doctor "undeclared native
		// plugins" notes — it MUST sanitize on display, so it is untrusted.Text.
		"Name": true,
		// MarketplaceID is foreign too but is NOT rendered into the status/doctor
		// layout this guard's `Name` covers; its only display sites are `import`'s
		// warn diagnostics, the same plain-string surface as the NativeMarketplace /
		// NativeSource fields below. That import-diagnostics surface is a deliberate
		// plain-string subset — promote these together if it is ever hardened. (Cf.
		// the marketplace guard's PluginEntry free-text subset and source's
		// MarketplaceSpec.URL/Ref carve-out.)
		"MarketplaceID": false,
	},
}

func stringShapedKind(t reflect.Type) bool { return t.Kind() == reflect.String }

// TestUntrustedFieldGuard — see internal/source's identically-named guard for the
// contract. It fails on an unclassified string-shaped field on the native-ingest
// descriptor or a type/classification mismatch (untrusted=true demands
// untrusted.Text; untrusted=false demands a plain string).
func TestUntrustedFieldGuard(t *testing.T) {
	textType := reflect.TypeOf(untrusted.Text(""))
	stringType := reflect.TypeOf("")

	structs := map[string]reflect.Type{
		"NativePlugin": reflect.TypeOf(adapter.NativePlugin{}),
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
