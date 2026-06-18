package source

import (
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/untrusted"
)

// untrustedClassified is the authoritative classification of every
// string-shaped field on the plugin/marketplace identity structs: a field is
// either untrusted (true → must be untrusted.Text, so a print site sanitizes it
// by construction) or deliberately trusted/plain (false → a plain string).
// TestUntrustedFieldGuard fails if a string-shaped field exists here that is in
// neither column — forcing whoever adds it to decide whether it carries
// fetched/native metadata that must be display-sanitized. This is the guard
// that makes the issue-#102 invariant correct by construction rather than a
// per-site convention nothing enforces.
var untrustedClassified = map[string]map[string]bool{
	"Plugin": {
		"ID": true, // plugins/<id>.toml stem, from a marketplace install
	},
	"PluginSpec": {
		"ID":          true,  // "<name>@<marketplace>", from the install ref
		"Version":     true,  // from the fetched manifest
		"ManifestSHA": false, // agentsync-computed tree/hex, not display text
		"Update":      false, // enum: pinned | track | manual
	},
	"Marketplace": {
		"Name": true, // declared marketplace name (marketplace.json) or URL slug
	},
	"MarketplaceSpec": {
		// URL/Ref are USER-supplied (a `marketplace add <url>` argument), printed
		// with %q — the documented carve-out that stays un-sanitized. Promote one
		// to untrusted.Text only if it ever starts carrying fetched metadata.
		"URL":               false,
		"Ref":               false,
		"DefaultUpdateMode": false, // enum
	},
}

// stringShapedKind reports whether t is a single string-valued field — a plain
// string OR a defined string type such as untrusted.Text (both reflect.String).
// Slices/maps/bools/structs are not the guard's concern: none of the untrusted
// display fields in scope are non-scalar.
func stringShapedKind(t reflect.Type) bool { return t.Kind() == reflect.String }

// TestUntrustedFieldGuard fails when a string-shaped field on a guarded struct
// is unclassified, or when its concrete type disagrees with its classification
// (untrusted=true demands untrusted.Text; untrusted=false demands a plain
// string). Reverting a Text field to string, or adding a new id/version/name
// field without tagging it Text, both trip here.
func TestUntrustedFieldGuard(t *testing.T) {
	textType := reflect.TypeOf(untrusted.Text(""))
	stringType := reflect.TypeOf("")

	structs := map[string]reflect.Type{
		"Plugin":          reflect.TypeOf(Plugin{}),
		"PluginSpec":      reflect.TypeOf(PluginSpec{}),
		"Marketplace":     reflect.TypeOf(Marketplace{}),
		"MarketplaceSpec": reflect.TypeOf(MarketplaceSpec{}),
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
