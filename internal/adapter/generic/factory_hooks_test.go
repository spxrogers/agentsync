package generic_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/generic"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

func factoryRegistered(t *testing.T, opts generic.Options) (adapter.Adapter, adapter.HookIngestGuard) {
	t.Helper()
	a := generic.Register(factorySpec(t), opts)
	g, ok := a.(adapter.HookIngestGuard)
	if !ok {
		t.Fatal("factory registry value must implement HookIngestGuard")
	}
	return a, g
}

func TestRegister_OnlyFactoryImplementsHookGuard(t *testing.T) {
	for _, spec := range generic.Specs() {
		a := generic.Register(spec, generic.Options{})
		_, ok := a.(adapter.HookIngestGuard)
		if spec.Name == "factory" && !ok {
			t.Fatal("factory must implement HookIngestGuard")
		}
		if spec.Name != "factory" && ok {
			t.Fatalf("%s implements HookIngestGuard; only a spec that declares hooks may", spec.Name)
		}
	}
}

func factorySpec(t *testing.T) generic.Spec {
	t.Helper()
	for _, s := range generic.Specs() {
		if s.Name == "factory" {
			return s
		}
	}
	t.Fatal("factory spec missing")
	return generic.Spec{}
}

func TestRefusedHookEvents_RequiresProjectRoot(t *testing.T) {
	_, g := factoryRegistered(t, generic.Options{})
	if _, err := g.RefusedHookEvents(adapter.ScopeProject, ""); err == nil {
		t.Fatal("project scope with an empty root must be rejected")
	}
}

func TestApply_FactoryHooks_SeedsSettingsWhenHooksJSONAbsent(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".factory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{
	  "hooksDisabled": false,
	  "hooks": {
	    "PreToolUse": [ { "matcher": "Execute", "commandRegex": "^git ", "hooks": [ { "type": "command", "command": "echo audit" } ] } ],
	    "Stop": [ { "matcher": "", "hooks": [ { "type": "command", "command": "echo stop" } ] } ]
	  }
	}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	a := generic.New(factorySpec(t), generic.Options{TargetRoot: tmp})
	c := source.Canonical{Hooks: []source.Hook{
		{Event: untrusted.Wrap("SessionStart"), Type: "command", Command: "echo session"},
	}}
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	var hooksOp *adapter.FileOp
	for i := range ops {
		if strings.HasSuffix(ops[i].Path, "hooks.json") {
			hooksOp = &ops[i]
		}
	}
	if hooksOp == nil {
		t.Fatal("no hooks.json op")
	}
	if strings.Contains(string(hooksOp.Content), "commandRegex") || strings.Contains(string(hooksOp.Content), "echo audit") {
		t.Fatalf("settings hooks leaked into op.Content and would be recorded as owned:\n%s", hooksOp.Content)
	}
	for i := 0; i < 2; i++ {
		if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, want := range []string{"commandRegex", "echo audit", "echo stop", "echo session"} {
			if !strings.Contains(text, want) {
				t.Fatalf("apply %d dropped %q from hooks.json:\n%s", i+1, want, text)
			}
		}
	}
}

func TestRender_Factory_UserMemory(t *testing.T) {
	tmp := t.TempDir()
	a := generic.New(factorySpec(t), generic.Options{TargetRoot: tmp})
	ops, skips, err := a.Render(secrets.ForRender(source.Canonical{Memory: source.Memory{Body: "policy\n"}}), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	op := findOp(ops, ".factory/AGENTS.md")
	if op == nil || op.Path != filepath.Join(tmp, ".factory", "AGENTS.md") {
		t.Fatalf("factory user memory op missing or misplaced: %+v", ops)
	}
	if source.StripManagedBanner(string(op.Content)) != "policy\n" {
		t.Fatalf("factory user memory content = %q", op.Content)
	}
	for _, s := range skips {
		if s.Component == "memory" {
			t.Fatalf("factory user memory must not be skipped: %+v", s)
		}
	}
}

func TestRender_FactoryHooks_SecondsAndForeignKeys(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	hooksPath := filepath.Join(tmp, ".factory", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := []byte("{\n  \"hooksDisabled\": false,\n  \"showHookOutput\": false\n}\n")
	if err := os.WriteFile(hooksPath, existing, 0o644); err != nil {
		t.Fatal(err)
	}
	a := generic.New(factorySpec(t), generic.Options{TargetRoot: tmp})
	c := source.Canonical{Hooks: []source.Hook{
		{Event: untrusted.Wrap("PreToolUse"), Matcher: "Execute", Type: "command", Command: "echo hi", Timeout: 30},
		{Event: untrusted.Wrap("PostCompact"), Type: "command", Command: "echo no"},
	}}
	ops, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	var sawPostCompact bool
	for _, s := range skips {
		if s.Component == "hook" && s.Name == "PostCompact" {
			sawPostCompact = true
		}
	}
	if !sawPostCompact {
		t.Fatalf("PostCompact must be skipped, not written; skips=%v", skips)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse hooks.json: %v\n%s", err, raw)
	}
	if doc["hooksDisabled"] != false || doc["showHookOutput"] != false {
		t.Fatalf("foreign keys not preserved: %#v", doc)
	}
	if _, ok := doc["PostCompact"]; ok {
		t.Fatal("PostCompact was written; Factory has no such event")
	}
	groups, _ := doc["PreToolUse"].([]any)
	if len(groups) != 1 {
		t.Fatalf("PreToolUse = %#v", doc["PreToolUse"])
	}
	group := groups[0].(map[string]any)
	handlers := group["hooks"].([]any)
	handler := handlers[0].(map[string]any)
	if handler["timeout"] != float64(30) {
		t.Fatalf("timeout = %#v; Factory counts seconds, want 30", handler["timeout"])
	}
	if handler["command"] != "echo hi" {
		t.Fatalf("command = %#v", handler["command"])
	}
}

func TestIngest_FactoryHooks_FromSettingsFallback(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	settings := filepath.Join(tmp, ".factory", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "hooks": {
	    "SessionStart": [ { "matcher": "", "hooks": [ { "type": "command", "command": "bd prime", "timeout": 3 } ] } ]
	  }
	}`
	if err := os.WriteFile(settings, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a := generic.New(factorySpec(t), generic.Options{TargetRoot: tmp})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hooks) != 1 || got.Hooks[0].Command != "bd prime" || got.Hooks[0].Timeout != 3 {
		t.Fatalf("settings.json fallback = %+v", got.Hooks)
	}
	if got.Hooks[0].Event.Unverified() != "SessionStart" {
		t.Fatalf("event = %q", got.Hooks[0].Event.Unverified())
	}
}

func TestIngest_FactoryHooks_CommandRegexRefusesEvent(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	hooksPath := filepath.Join(tmp, ".factory", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "PreToolUse": [ { "matcher": "Execute", "commandRegex": "^git ", "hooks": [ { "type": "command", "command": "echo hi" } ] } ]
	}`
	if err := os.WriteFile(hooksPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	a, g := factoryRegistered(t, generic.Options{TargetRoot: tmp, Stderr: &warn})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hooks) != 0 {
		t.Fatalf("commandRegex must leave the event uncaptured, got %+v", got.Hooks)
	}
	refused, err := g.RefusedHookEvents(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(refused) != 1 || refused[0] != "PreToolUse" {
		t.Fatalf("semantic refusal = %v", refused)
	}
	if !strings.Contains(warn.String(), "commandRegex") {
		t.Fatalf("warning = %q", warn.String())
	}
}

// TestRefusedHookEvents_PostCompactIsNotRetired pins the shared-file rule.
// PostCompact is not a Droid event, so a native PostCompact array must not be
// refused: import retires every refused event's hooks/<event>.toml for every
// agent, and Claude, Codex, and Grok still use that file.
func TestRefusedHookEvents_PostCompactIsNotRetired(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	hooksPath := filepath.Join(tmp, ".factory", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "PostCompact": [ { "hooks": [ { "type": "command", "command": "echo after" } ] } ]
	}`
	if err := os.WriteFile(hooksPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a, g := factoryRegistered(t, generic.Options{TargetRoot: tmp})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hooks) != 0 {
		t.Fatalf("PostCompact must not be captured, got %+v", got.Hooks)
	}
	refused, err := g.RefusedHookEvents(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range refused {
		if event == "PostCompact" {
			t.Fatal("refusing PostCompact would delete the shared canonical hook file")
		}
	}
}

// TestIngest_FactoryHooks_HooksJSONWinsOverSettings pins that settings.json is
// only the fallback for an absent hooks.json. Once hooks.json exists, Droid
// ignores settings.json hooks, and so does import.
func TestIngest_FactoryHooks_HooksJSONWinsOverSettings(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".factory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksBody := `{
	  "SessionStart": [ { "hooks": [ { "type": "command", "command": "from-hooks-json", "timeout": 5 } ] } ]
	}`
	settingsBody := `{
	  "hooks": {
	    "SessionStart": [ { "hooks": [ { "type": "command", "command": "from-settings", "timeout": 9 } ] } ]
	  }
	}`
	if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(hooksBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settingsBody), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := generic.New(factorySpec(t), generic.Options{TargetRoot: tmp}).Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hooks) != 1 || got.Hooks[0].Command != "from-hooks-json" || got.Hooks[0].Timeout != 5 {
		t.Fatalf("hooks.json must win over settings.json, got %+v", got.Hooks)
	}
}

// TestRefusedHookEvents_ExplicitZeroTimeout pins that timeout 0 is a semantic
// refusal. Timeout == 0 means "no timeout key", so capturing 0 and re-rendering
// it would drop the key and silently take Droid's 60s default.
func TestRefusedHookEvents_ExplicitZeroTimeout(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	hooksPath := filepath.Join(tmp, ".factory", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "PreToolUse": [ { "matcher": "Execute", "hooks": [ { "type": "command", "command": "echo hi", "timeout": 0 } ] } ]
	}`
	if err := os.WriteFile(hooksPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a, g := factoryRegistered(t, generic.Options{TargetRoot: tmp})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hooks) != 0 {
		t.Fatalf("timeout 0 must leave the event uncaptured, got %+v", got.Hooks)
	}
	refused, err := g.RefusedHookEvents(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(refused) != 1 || refused[0] != "PreToolUse" {
		t.Fatalf("explicit zero must be a semantic refusal, got %v", refused)
	}
}

func TestRender_FactoryHooks_ProjectScope(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	project := t.TempDir()
	a := generic.New(factorySpec(t), generic.Options{TargetRoot: home})
	c := source.Canonical{Hooks: []source.Hook{
		{Event: untrusted.Wrap("PreToolUse"), Matcher: "Execute", Type: "command", Command: "echo proj", Timeout: 8},
	}}
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, project)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(project, ".factory", "hooks.json")
	op := findOp(ops, ".factory/hooks.json")
	if op == nil || op.Path != want {
		t.Fatalf("project hooks path = %+v, want %s", ops, want)
	}
	if strings.Contains(op.Path, filepath.Join(home, ".factory")) {
		t.Fatal("project scope wrote the user hooks file")
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	got, err := a.Ingest(adapter.ScopeProject, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hooks) != 1 || got.Hooks[0].Command != "echo proj" || got.Hooks[0].Timeout != 8 {
		t.Fatalf("project ingest = %+v", got.Hooks)
	}
	if _, err := os.Stat(filepath.Join(home, ".factory", "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("user hooks.json should not exist, stat err=%v", err)
	}
}
