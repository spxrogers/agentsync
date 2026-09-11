package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// pluginSpecPreserved classifies every field of source.PluginSpec that a
// re-install must carry forward from the existing plugins/<id>.toml. It is the
// counterpart of pluginInstallRefresh, which names the fields an install
// recomputes; together the two must account for the struct exactly.
//
// The value is a short note on WHY the field is user-authored state, so the map
// stays a classification rather than a checklist.
var pluginSpecPreserved = map[string]string{
	"Update":       "the user's update policy (pinned|track|manual)",
	"Agents":       "the user's fan-out allowlist — resetting it re-broadens a narrowed one (#140)",
	"NativeAgents": "the deferral record; re-seeding it stops projecting to an adopted agent",
	"Disabled":     "`plugin disable` state — resetting it silently re-enables the plugin",
}

// TestPluginSpecFieldsAreClassified fails when source.PluginSpec grows a field
// that the install path neither refreshes nor preserves.
//
// This is the guard the preserve path never had. `installPluginInto` used to
// merge the existing file field by field, so a new lifecycle field was dropped
// by DEFAULT: it silently reverted to its zero value on every re-install and
// re-import — the #140 bug class, and the "the model drifts from its artifact"
// failure this repo treats as a bug. The merge is now wholesale, but the
// classification still has to be honest, so this mirrors TestNewSecretFieldGuard
// (internal/secrets): reflect over the struct, insist every field is accounted
// for, and tell the author exactly what to do.
func TestPluginSpecFieldsAreClassified(t *testing.T) {
	refreshed := map[string]bool{}
	rt := reflect.TypeOf(pluginInstallRefresh{})
	for i := 0; i < rt.NumField(); i++ {
		refreshed[rt.Field(i).Name] = true
	}

	st := reflect.TypeOf(source.PluginSpec{})
	for i := 0; i < st.NumField(); i++ {
		name := st.Field(i).Name
		_, isPreserved := pluginSpecPreserved[name]
		if refreshed[name] && isPreserved {
			t.Errorf("source.PluginSpec.%s is classified BOTH refreshed and preserved; it must be exactly one", name)
			continue
		}
		if !refreshed[name] && !isPreserved {
			t.Errorf("source.PluginSpec.%s is unclassified: add it to pluginInstallRefresh if a (re-)install "+
				"recomputes it from the fetched marketplace metadata, or to pluginSpecPreserved if it is "+
				"user-authored lifecycle state a re-install must carry forward", name)
		}
	}

	// The refresh struct must not name a field the spec does not have: a rename
	// on the canonical side would otherwise leave applyTo stamping a field that
	// no longer exists (a compile error) or, worse, a stale classification here.
	for name := range refreshed {
		if _, ok := st.FieldByName(name); !ok {
			t.Errorf("pluginInstallRefresh.%s names no field of source.PluginSpec", name)
		}
	}
	for name := range pluginSpecPreserved {
		if _, ok := st.FieldByName(name); !ok {
			t.Errorf("pluginSpecPreserved classifies %q, which is not a field of source.PluginSpec", name)
		}
	}
}

// plantSentinels fills every field of spec with a distinguishable non-zero
// value, by reflection, so the round-trip below can prove each one survives.
// Planting reflectively (rather than from a literal) is what makes the proof
// survive a NEW field: the field is planted, asserted, and — if the preserve
// path drops it — the assertion fails, with no test edit needed.
//
// A new field that is left UNCLASSIFIED, of a kind this function can plant
// (string, bool, []string, *[]string), is skipped by the round-trip below, so
// one cause yields exactly one failure — TestPluginSpecFieldsAreClassified's.
// A field of any OTHER kind additionally fails
// TestInstallPreservesEveryLifecycleField here, at the Fatalf, by design: the
// proof must be taught the kind before it can claim to cover the field.
func plantSentinels(t *testing.T, spec *source.PluginSpec) {
	t.Helper()
	v := reflect.ValueOf(spec).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		name := v.Type().Field(i).Name
		sentinel := "sentinel-" + name
		switch {
		case f.Kind() == reflect.String:
			f.SetString(sentinel)
		case f.Kind() == reflect.Bool:
			f.SetBool(true)
		case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.String:
			f.Set(reflect.ValueOf([]string{sentinel}).Convert(f.Type()))
		case f.Kind() == reflect.Pointer && f.Type().Elem().Kind() == reflect.Slice &&
			f.Type().Elem().Elem().Kind() == reflect.String:
			p := reflect.New(f.Type().Elem())
			p.Elem().Set(reflect.ValueOf([]string{sentinel}).Convert(f.Type().Elem()))
			f.Set(p)
		default:
			t.Fatalf("source.PluginSpec.%s has kind %s, which this test cannot plant a sentinel in; "+
				"teach plantSentinels that kind so the preserve proof still covers every field", name, f.Kind())
		}
	}
}

// TestInstallPreservesEveryLifecycleField proves the classification is real:
// every field pluginSpecPreserved claims is carried forward actually survives a
// re-install, ON DISK, and every field pluginInstallRefresh claims is recomputed
// actually replaces what was there. Without it the map could claim a coverage
// the merge does not deliver — the same reason internal/secrets pairs its field
// guard with TestWalkerVisitsEveryCoveredField.
//
// The oracle is the artifact, not the model: the sentinels are written as real
// plugins/<id>.toml bytes and read back from the file installPluginInto wrote.
func TestInstallPreservesEveryLifecycleField(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	writeMarketplaceCacheFixture(t, home, "test-mp", "demo")

	var planted source.PluginSpec
	plantSentinels(t, &planted)
	pluginPath := filepath.Join(home, "plugins", "demo.toml")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := toml.Marshal(source.Plugin{Plugin: planted})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pluginPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := installPluginInto(home, "demo", "test-mp", []string{"seed-agent"}); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	after, err := readPluginTOML(pluginPath)
	if err != nil {
		t.Fatalf("read back the written artifact: %v", err)
	}

	refreshed := map[string]bool{}
	rt := reflect.TypeOf(pluginInstallRefresh{})
	for i := 0; i < rt.NumField(); i++ {
		refreshed[rt.Field(i).Name] = true
	}
	got := reflect.ValueOf(after.Plugin)
	want := reflect.ValueOf(planted)
	for i := 0; i < got.NumField(); i++ {
		name := got.Type().Field(i).Name
		gotF, wantF := got.Field(i).Interface(), want.Field(i).Interface()
		switch why, preserved := pluginSpecPreserved[name]; {
		case preserved:
			if !reflect.DeepEqual(gotF, wantF) {
				t.Errorf("re-install did not preserve %s (%s): on disk %#v, want the existing %#v",
					name, why, gotF, wantF)
			}
		case refreshed[name]:
			if reflect.DeepEqual(gotF, wantF) {
				t.Errorf("re-install did not refresh %s: it still holds the stale on-disk value %#v", name, gotF)
			}
		default:
			// Unclassified: TestPluginSpecFieldsAreClassified reports it. Asserting
			// here too would add a second, more confusing failure for one cause.
		}
	}
	// The caller's native_agents seed must NOT override an existing list — the
	// sentinel above is an existing one. (Asserted separately because the loop
	// above would also pass if the seed happened to equal the sentinel.)
	if after.Plugin.NativeAgents == nil || (*after.Plugin.NativeAgents)[0] == "seed-agent" {
		t.Errorf("the caller's native_agents seed overwrote an existing deferral list: %v", after.Plugin.NativeAgents)
	}
}

// writeMarketplaceCacheFixture lays down the marketplace cache tree that
// installPluginInto resolves against: the entry it reads and the plugin
// directory the relative fetcher copies from.
func writeMarketplaceCacheFixture(t *testing.T, home, mpName, plugin string) {
	t.Helper()
	root := marketplaceCacheDir(home, mpName)
	mpJSON := `{"name": "` + mpName + `", "owner": {"name": "tester"}, "plugins": [` +
		`{"name": "` + plugin + `", "source": "./plugins/` + plugin + `"}]}`
	mustWrite(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), mpJSON)
	mustWrite(t, filepath.Join(root, "plugins", plugin, ".claude-plugin", "plugin.json"),
		`{"name": "`+plugin+`", "version": "1.0.0"}`)
}
