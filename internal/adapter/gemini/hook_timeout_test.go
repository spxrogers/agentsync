package gemini_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// firstHandler walks a rendered settings.json down to the first handler object
// of one event. Each step is checked so a shape change fails the test with the
// step that broke rather than panicking in a chained type assertion.
func firstHandler(t *testing.T, path, event string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, raw)
	}
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("%s: \"hooks\" is %T, want an object\n%s", path, doc["hooks"], raw)
	}
	defs, ok := hooks[event].([]any)
	if !ok || len(defs) == 0 {
		t.Fatalf("%s: hooks[%q] is %T (len 0?), want a non-empty array\n%s", path, event, hooks[event], raw)
	}
	def, ok := defs[0].(map[string]any)
	if !ok {
		t.Fatalf("%s: hooks[%q][0] is %T, want an object\n%s", path, event, defs[0], raw)
	}
	handlers, ok := def["hooks"].([]any)
	if !ok || len(handlers) == 0 {
		t.Fatalf("%s: hooks[%q][0].hooks is %T (len 0?), want a non-empty array\n%s", path, event, def["hooks"], raw)
	}
	h, ok := handlers[0].(map[string]any)
	if !ok {
		t.Fatalf("%s: hooks[%q][0].hooks[0] is %T, want an object\n%s", path, event, handlers[0], raw)
	}
	return h
}

func renderApplyHooks(t *testing.T, a adapter.Adapter, c source.Canonical) {
	t.Helper()
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

// TestRenderHooks_TimeoutIsMilliseconds pins the ON-DISK unit, which is the
// assertion a same-adapter round trip cannot make: render 45 canonical seconds
// and require the bytes Gemini will read to say 45000, because Gemini's hooks
// reference defines `timeout` as "Execution timeout in milliseconds (default:
// 60000)". Emitting 45 here would give the user a 45ms budget.
func TestRenderHooks_TimeoutIsMilliseconds(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	a := gemini.New(gemini.Options{TargetRoot: tmp})
	renderApplyHooks(t, a, source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Matcher: "write_file", Type: "command", Command: "echo a", Timeout: 45},
	}})

	h := firstHandler(t, filepath.Join(tmp, ".gemini", "settings.json"), "BeforeTool")
	if got := h["timeout"]; got != float64(45000) {
		t.Fatalf("45 canonical seconds rendered as %v (%T); want 45000 (milliseconds)", got, got)
	}
}

// TestIngestHooks_TimeoutMillisecondsToSeconds is the other leg: Gemini's OWN
// documented default, 60000ms, must come back as 60 seconds and not as 60000.
// Captured verbatim it would fan out to Claude/Codex/Cursor/Grok — all of which
// count seconds — as a ~16.7 hour timeout.
func TestIngestHooks_TimeoutMillisecondsToSeconds(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	settings := filepath.Join(tmp, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	native := `{
  "hooks": {
    "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": "x", "timeout": 60000 } ] } ]
  }
}`
	if err := os.WriteFile(settings, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := gemini.New(gemini.Options{TargetRoot: tmp}).Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(got.Hooks) != 1 {
		t.Fatalf("expected 1 hook captured, got %+v", got.Hooks)
	}
	if got.Hooks[0].Timeout != 60 {
		t.Fatalf("Gemini's 60000ms default captured as %d; want 60 seconds", got.Hooks[0].Timeout)
	}
}

// TestIngestHooks_SubSecondTimeoutRefused pins that a native value agentsync
// cannot express in whole seconds is refused rather than rounded — silently
// turning 1.5s into 1s or 2s changes how long the user's hook may run.
func TestIngestHooks_SubSecondTimeoutRefused(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	settings := filepath.Join(tmp, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	native := `{
  "hooks": {
    "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": "x", "timeout": 1500 } ] } ]
  }
}`
	if err := os.WriteFile(settings, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	got, err := gemini.New(gemini.Options{TargetRoot: tmp, Stderr: &warn}).Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(got.Hooks) != 0 {
		t.Fatalf("a 1500ms timeout must leave the event uncaptured, got %+v", got.Hooks)
	}
	if !strings.Contains(warn.String(), "cannot represent") {
		t.Fatalf("refusal must say agentsync cannot represent the value; got %q", warn.String())
	}
}

// TestHookTimeout_CrossAdapterUnits is the test the review asked for, and the
// one the original change was missing: ONE canonical timeout rendered by TWO
// adapters, asserting the wire values differ by the 1000x the harnesses
// themselves disagree by. A per-adapter round trip stays green even when both
// of its legs share the same unit mistake, because the error cancels itself
// out; comparing two adapters against each other cannot.
func TestHookTimeout_CrossAdapterUnits(t *testing.T) {
	testenv.RequireContainer(t)
	in := source.Canonical{Hooks: []source.Hook{
		{Event: "PreToolUse", Matcher: "Bash", Type: "command", Command: "echo a", Timeout: 30},
	}}

	claudeRoot := t.TempDir()
	renderApplyHooks(t, claude.New(claude.Options{TargetRoot: claudeRoot}), in)
	claudeHandler := firstHandler(t, filepath.Join(claudeRoot, ".claude", "settings.json"), "PreToolUse")

	geminiRoot := t.TempDir()
	renderApplyHooks(t, gemini.New(gemini.Options{TargetRoot: geminiRoot}), in)
	geminiHandler := firstHandler(t, filepath.Join(geminiRoot, ".gemini", "settings.json"), "BeforeTool")

	claudeTimeout, ok := claudeHandler["timeout"].(float64)
	if !ok {
		t.Fatalf("claude timeout is %T, want a number", claudeHandler["timeout"])
	}
	geminiTimeout, ok := geminiHandler["timeout"].(float64)
	if !ok {
		t.Fatalf("gemini timeout is %T, want a number", geminiHandler["timeout"])
	}
	if claudeTimeout != 30 {
		t.Errorf("claude rendered %v; Claude Code documents seconds, want 30", claudeTimeout)
	}
	if geminiTimeout != 30000 {
		t.Errorf("gemini rendered %v; Gemini CLI documents milliseconds, want 30000", geminiTimeout)
	}
	if geminiTimeout != claudeTimeout*1000 {
		t.Fatalf("one canonical timeout rendered as %v (claude, seconds) and %v (gemini, milliseconds); the two must differ by exactly 1000x", claudeTimeout, geminiTimeout)
	}
}
