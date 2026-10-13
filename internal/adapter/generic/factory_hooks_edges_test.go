package generic_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/generic"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/testenv"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// readHooksJSON parses a Factory hooks.json. Assertions go through the parsed
// document rather than substring matches, which pass on a value that landed
// under the wrong event or in a key Droid never reads.
func readHooksJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	doc, err := jsonkeys.DecodeObject(raw)
	if err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, raw)
	}
	return doc
}

// firstDef returns the first matcher group of an event, failing (not
// panicking) on any shape mismatch.
func firstDef(t *testing.T, doc map[string]any, event string) map[string]any {
	t.Helper()
	defs, ok := doc[event].([]any)
	if !ok || len(defs) == 0 {
		t.Fatalf("hooks.json %q is %T (len 0?), want a non-empty array: %#v", event, doc[event], doc)
	}
	def, ok := defs[0].(map[string]any)
	if !ok {
		t.Fatalf("hooks.json %q[0] is %T, want an object", event, defs[0])
	}
	return def
}

// eventCommands lists every handler command under one event, in order. An
// absent event is an empty list.
func eventCommands(t *testing.T, doc map[string]any, event string) []string {
	t.Helper()
	raw, present := doc[event]
	if !present {
		return nil
	}
	defs, ok := raw.([]any)
	if !ok {
		t.Fatalf("hooks.json %q is %T, want an array", event, raw)
	}
	var out []string
	for i, d := range defs {
		def, ok := d.(map[string]any)
		if !ok {
			t.Fatalf("hooks.json %q[%d] is %T, want an object", event, i, d)
		}
		handlers, ok := def["hooks"].([]any)
		if !ok {
			t.Fatalf("hooks.json %q[%d].hooks is %T, want an array", event, i, def["hooks"])
		}
		for j, h := range handlers {
			handler, ok := h.(map[string]any)
			if !ok {
				t.Fatalf("hooks.json %q[%d].hooks[%d] is %T, want an object", event, i, j, h)
			}
			cmd, _ := handler["command"].(string)
			out = append(out, cmd)
		}
	}
	return out
}

func writeFactoryFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func renderApplyFactory(t *testing.T, a adapter.Adapter, c source.Canonical) error {
	t.Helper()
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return a.Apply(ops, adapter.PassThroughWriter{})
}

// TestApply_FactoryHooks_SeedWarnsWhenAnOwnedEventReplacesUserHooks is the
// review's repro. settings.json has a Stop handler; canonical also renders
// Stop. Creating hooks.json makes Droid stop reading settings.json, and the
// canonical array replaces the seeded one, so the user's handler silently
// stops running. Nothing is deleted, but the user must hear about it.
//
// The SessionStart row is the control that keeps the warning honest: the same
// handler written without "matcher" (as settings.json commonly has it) is what
// `agentsync import factory` captures and `apply` re-renders WITH one. Warning
// there would fire on the ordinary import-then-apply path.
func TestApply_FactoryHooks_SeedWarnsWhenAnOwnedEventReplacesUserHooks(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".factory")
	settingsPath := writeFactoryFile(t, dir, "settings.json", `{
	  "hooks": {
	    "Stop":         [ { "matcher": "", "hooks": [ { "type": "command", "command": "echo user-stop" } ] } ],
	    "SessionStart": [ { "hooks": [ { "command": "bd prime", "timeout": 3 } ] } ]
	  }
	}`)
	settingsBefore, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	a := generic.New(factorySpec(t), generic.Options{TargetRoot: tmp, Stderr: &warn})
	err = renderApplyFactory(t, a, source.Canonical{Hooks: []source.Hook{
		{Event: untrusted.Wrap("Stop"), Type: "command", Command: "echo canonical-stop"},
		{Event: untrusted.Wrap("SessionStart"), Type: "command", Command: "bd prime", Timeout: 3},
	}})
	if err != nil {
		t.Fatal(err)
	}

	doc := readHooksJSON(t, filepath.Join(dir, "hooks.json"))
	if got := eventCommands(t, doc, "Stop"); len(got) != 1 || got[0] != "echo canonical-stop" {
		t.Fatalf("Stop = %v; agentsync owns Stop, so hooks.json carries the canonical handler only", got)
	}

	out := warn.String()
	for _, want := range []string{`"Stop"`, "settings.json", "stop running", "hooks/Stop.toml"} {
		if !strings.Contains(out, want) {
			t.Fatalf("replacement warning must contain %q so the user can act on it; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"SessionStart"`) {
		t.Fatalf("SessionStart is the same handler on both sides; warning about it is a false alarm on the import-then-apply path:\n%s", out)
	}

	settingsAfter, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(settingsBefore, settingsAfter) {
		t.Fatal("settings.json must be left exactly as it was; the user's handler is reported, never edited")
	}

	// A second apply has nothing left to seed (hooks.json exists), so it must
	// not repeat the warning.
	warn.Reset()
	if err := renderApplyFactory(t, a, source.Canonical{Hooks: []source.Hook{
		{Event: untrusted.Wrap("Stop"), Type: "command", Command: "echo canonical-stop"},
	}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(warn.String(), "stop running") {
		t.Fatalf("the warning belongs to the apply that creates hooks.json, not every apply after:\n%s", warn.String())
	}
}

// TestFactoryHooks_MalformedFiles pins what each malformed native shape does
// on all three paths. The invariants, per row: import never captures a guess
// and never REFUSES (a refusal retires the shared canonical file for every
// agent, and a typo must not do that); apply never replaces a hooks.json it
// cannot parse; a bad settings.json never blocks writing hooks.json and is
// never edited.
func TestFactoryHooks_MalformedFiles(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name          string
		hooksJSON     string // "" = absent
		settingsJSON  string // "" = absent
		wantIngestErr bool   // import refuses loudly rather than reading nothing
		wantApplyErr  bool   // apply refuses rather than overwrite what it cannot parse
		wantWarn      string // substring of a warning from ingest or apply
	}{
		{name: "hooks.json is not valid JSON", hooksJSON: `{not json`, wantIngestErr: true, wantApplyErr: true},
		{name: "hooks.json root is an array", hooksJSON: `[ { "Stop": [] } ]`, wantIngestErr: true, wantApplyErr: true},
		{
			name:      "hooks.json event value is not an array",
			hooksJSON: `{ "Stop": { "hooks": [] } }`,
			wantWarn:  "is not an array",
		},
		{
			name:          "settings.json is not valid JSON",
			settingsJSON:  `{not json`,
			wantIngestErr: true,
			wantWarn:      "could not parse",
		},
		{
			name:          "settings.json root is an array",
			settingsJSON:  `[ { "hooks": {} } ]`,
			wantIngestErr: true,
			wantWarn:      "could not parse",
		},
		{
			// The phrase is apply's, not import's: import warns about this shape
			// too, so matching its wording would not catch a silent apply.
			name:         `settings.json "hooks" is not an object`,
			settingsJSON: `{ "hooks": [ "echo hi" ] }`,
			wantWarn:     "nothing from it is carried into hooks.json",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			dir := filepath.Join(tmp, ".factory")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			hooksPath := filepath.Join(dir, "hooks.json")
			settingsPath := filepath.Join(dir, "settings.json")
			if tc.hooksJSON != "" {
				writeFactoryFile(t, dir, "hooks.json", tc.hooksJSON)
			}
			if tc.settingsJSON != "" {
				writeFactoryFile(t, dir, "settings.json", tc.settingsJSON)
			}

			var warn bytes.Buffer
			a, g := factoryRegistered(t, generic.Options{TargetRoot: tmp, Stderr: &warn})

			got, err := a.Ingest(adapter.ScopeUser, "")
			if tc.wantIngestErr {
				if err == nil {
					t.Fatalf("Ingest read a malformed file as empty; it must refuse loudly. hooks=%+v", got.Hooks)
				}
			} else {
				if err != nil {
					t.Fatalf("Ingest: %v", err)
				}
				if len(got.Hooks) != 0 {
					t.Fatalf("a malformed shape must leave hooks uncaptured, got %+v", got.Hooks)
				}
			}
			refused, rerr := g.RefusedHookEvents(adapter.ScopeUser, "")
			if rerr != nil && !tc.wantIngestErr {
				t.Fatalf("RefusedHookEvents: %v", rerr)
			}
			if len(refused) != 0 {
				t.Fatalf("a malformed shape is structural and must never be refused (refusal retires shared canonical config): %v", refused)
			}

			var hooksBefore, settingsBefore []byte
			if tc.hooksJSON != "" {
				hooksBefore = []byte(tc.hooksJSON)
			}
			if tc.settingsJSON != "" {
				settingsBefore = []byte(tc.settingsJSON)
			}
			aerr := renderApplyFactory(t, a, source.Canonical{Hooks: []source.Hook{
				{Event: untrusted.Wrap("Stop"), Type: "command", Command: "echo canonical-stop"},
			}})
			if tc.wantApplyErr {
				if aerr == nil {
					t.Fatal("apply must refuse a hooks.json it cannot parse rather than replace it")
				}
				after, err := os.ReadFile(hooksPath)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(after, hooksBefore) {
					t.Fatalf("a refused apply must leave hooks.json byte-identical; got:\n%s", after)
				}
			} else {
				if aerr != nil {
					t.Fatalf("apply: %v", aerr)
				}
				if cmds := eventCommands(t, readHooksJSON(t, hooksPath), "Stop"); len(cmds) != 1 || cmds[0] != "echo canonical-stop" {
					t.Fatalf("Stop = %v, want the canonical handler", cmds)
				}
			}
			if settingsBefore != nil {
				after, err := os.ReadFile(settingsPath)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(after, settingsBefore) {
					t.Fatal("settings.json must never be edited")
				}
			}
			if tc.wantWarn != "" && !strings.Contains(warn.String(), tc.wantWarn) {
				t.Fatalf("want a warning containing %q, got:\n%s", tc.wantWarn, warn.String())
			}
		})
	}
}

// TestFactoryHooks_OrphanCleanupKeepsForeignKeys drives the real render
// pipeline (Plan, Apply, PruneStaleState, RecordOpsState) against the real
// Factory adapter. A hook removed from the canonical source must disappear
// from hooks.json — partially, while another event is still rendered, and
// entirely, through the synthesized cleanup op — while every key agentsync
// never wrote (hooksDisabled, showHookOutput, a user's SessionEnd) survives.
func TestFactoryHooks_OrphanCleanupKeepsForeignKeys(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	home := filepath.Join(tmp, ".agentsync")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, ".factory")
	hooksPath := writeFactoryFile(t, dir, "hooks.json", `{
	  "hooksDisabled": false,
	  "showHookOutput": true,
	  "SessionEnd": [ { "hooks": [ { "type": "command", "command": "echo user-end" } ] } ]
	}`)

	reg := adapter.NewRegistry()
	if err := reg.Register(generic.Register(factorySpec(t), generic.Options{TargetRoot: tmp})); err != nil {
		t.Fatal(err)
	}
	st := state.New()
	apply := func(hooks ...source.Hook) map[string]any {
		t.Helper()
		plan, err := render.Plan(secrets.ForRender(source.Canonical{Hooks: hooks}), reg, []string{"factory"}, adapter.ScopeUser, "", st, tmp)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if _, _, _, err := render.Apply(plan, reg, st, home, tmp, adapter.ScopeUser, ""); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		ops := plan.PerAgent["factory"].Ops
		render.PruneStaleState(st, tmp, "factory", adapter.ScopeUser, "", ops, render.NewSharedDests(plan, tmp))
		if err := render.RecordOpsState(st, tmp, "factory", adapter.ScopeUser, "", ops); err != nil {
			t.Fatalf("RecordOpsState: %v", err)
		}
		return readHooksJSON(t, hooksPath)
	}
	assertForeignKept := func(stage string, doc map[string]any) {
		t.Helper()
		if doc["hooksDisabled"] != false || doc["showHookOutput"] != true {
			t.Fatalf("%s: foreign top-level keys not preserved: %#v", stage, doc)
		}
		if got := eventCommands(t, doc, "SessionEnd"); len(got) != 1 || got[0] != "echo user-end" {
			t.Fatalf("%s: the user's SessionEnd = %v; agentsync never owned it", stage, got)
		}
	}
	pre := source.Hook{Event: untrusted.Wrap("PreToolUse"), Matcher: "Execute", Type: "command", Command: "echo pre"}
	stop := source.Hook{Event: untrusted.Wrap("Stop"), Type: "command", Command: "echo stop"}

	doc := apply(pre, stop)
	if got := eventCommands(t, doc, "Stop"); len(got) != 1 {
		t.Fatalf("setup: Stop = %v", got)
	}
	assertForeignKept("initial apply", doc)

	doc = apply(pre)
	if _, ok := doc["Stop"]; ok {
		t.Fatalf("Stop was removed from canonical but is still in hooks.json: %#v", doc["Stop"])
	}
	if got := eventCommands(t, doc, "PreToolUse"); len(got) != 1 || got[0] != "echo pre" {
		t.Fatalf("PreToolUse = %v; it is still rendered", got)
	}
	assertForeignKept("after removing Stop", doc)

	doc = apply()
	if _, ok := doc["PreToolUse"]; ok {
		t.Fatalf("every canonical hook is gone, so the cleanup op must remove PreToolUse: %#v", doc["PreToolUse"])
	}
	assertForeignKept("after removing every hook", doc)
}

// factoryResolver is a fixed secret backend for the round trip below.
type factoryResolver map[string]string

func (r factoryResolver) Resolve(key string) (string, error) {
	if v, ok := r[key]; ok {
		return v, nil
	}
	return "", fmt.Errorf("unknown secret %q", key)
}

// TestFactoryHooks_SecretCommandRoundTrip pins the secret boundary for Factory
// hooks end to end. A hook command is secret-bearing (walkSecretFields), so
// apply writes the RESOLVED value — Droid has to run it — and the trip back
// through Ingest must re-reference it to ${secret:…} with nothing left for the
// leak backstop to refuse. A regression here persists a live credential into
// ~/.agentsync, often a committed dotfiles repo.
func TestFactoryHooks_SecretCommandRoundTrip(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	const live = "fct_LIVE_7f3a9c21d4"
	const templated = "deploy --token ${secret:factory.token}"
	sec := factoryResolver{"factory.token": live}
	env := factoryResolver{}

	canonical := source.Canonical{Hooks: []source.Hook{
		{Event: untrusted.Wrap("SessionStart"), Type: "command", Command: templated, Timeout: 5},
	}}
	resolved, err := secrets.SubstituteCanonical(canonical, sec, env)
	if err != nil {
		t.Fatalf("SubstituteCanonical: %v", err)
	}
	a := generic.New(factorySpec(t), generic.Options{TargetRoot: tmp})
	ops, _, err := a.Render(resolved, adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}

	doc := readHooksJSON(t, filepath.Join(tmp, ".factory", "hooks.json"))
	if got := eventCommands(t, doc, "SessionStart"); len(got) != 1 || got[0] != "deploy --token "+live {
		t.Fatalf("hooks.json SessionStart = %v; Droid must receive the resolved command", got)
	}

	ingested, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ingested.Hooks) != 1 || ingested.Hooks[0].Command != "deploy --token "+live {
		t.Fatalf("ingested hooks = %+v", ingested.Hooks)
	}
	secrets.ReReferenceCanonical(&ingested, &canonical, sec, env)
	if got := ingested.Hooks[0].Command; got != templated {
		t.Fatalf("re-referenced command = %q, want %q (a live secret would be written back)", got, templated)
	}
	if leaks := secrets.ResidualSecretCleartext(&ingested, &canonical, sec, env); len(leaks) != 0 {
		t.Fatalf("leak backstop would refuse this write-back: %v", leaks)
	}
}
