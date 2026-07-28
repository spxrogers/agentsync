package render

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
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
		"Reason":    true,  // adapter-authored, but routinely interpolates adapter INPUT
		//                    (codex joins arbitrary subagent frontmatter keys into it,
		//                    which for a plugin component is fetched marketplace data)
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

// TestSkipDetailReasonIsSanitizedOnDisplay is the behavioral half of the
// classification above.
//
// SkipDetail.Reason was documented "adapter-authored (trusted)" and typed as a
// plain string, but adapters INTERPOLATE their input into it — the codex
// subagent adapter joins arbitrary frontmatter keys into its dropped-key
// reason, and for a plugin-projected subagent that frontmatter is fetched
// marketplace data. A raw ESC in a YAML key therefore reached any display site
// that forgot ui.Sanitize. Typing it untrusted.Text makes String() sanitize by
// construction; this pins that it actually does.
func TestSkipDetailReasonIsSanitizedOnDisplay(t *testing.T) {
	hostile := "dropped tools\x1b[31m, \x1b]8;;http://evil\x07model"
	d := skipDetails([]adapter.Skip{{
		Component: "subagent",
		Name:      "reviewer",
		Reason:    hostile,
		Kind:      adapter.SkipReduced,
	}})[0]

	got := d.Reason.String()
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("SkipDetail.Reason.String() still carries a raw ESC, so a display site emits it "+
			"to the terminal verbatim: %q", got)
	}
	// The human content must survive — sanitizing must not gut the message.
	if !strings.Contains(got, "dropped tools") || !strings.Contains(got, "model") {
		t.Fatalf("sanitizing destroyed the reason's readable content: %q", got)
	}
	// Unverified() is the deliberate escape hatch and must still yield the raw
	// bytes, so a caller that genuinely needs them can say so explicitly.
	if d.Reason.Unverified() != hostile {
		t.Errorf("Unverified() must return the raw value; got %q", d.Reason.Unverified())
	}
}
