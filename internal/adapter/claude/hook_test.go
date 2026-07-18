package claude_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestIngest_HookArtifactRoundTrip is artifact-anchored: it seeds a
// spec-complete settings.json on disk (with hook events the canonical model
// cannot fully represent) and asserts the ON-DISK result of a full
// import→apply round-trip. Approach A leaves unrepresentable events entirely
// uncaptured, so the render pipeline never owns their /hooks/<event> array and
// the user's native entries survive byte-for-byte. Only the clean
// command-only event round-trips, and no {type,command:""} handler is ever
// emitted.
func TestIngest_HookArtifactRoundTrip(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	// (a) PreToolUse: a command handler carrying an unmodeled field (timeout).
	// (b) Notification: a non-command ("prompt") handler.
	// (c) PostToolUse: a clean command-only event.
	native := `{
  "hooks": {
    "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": "echo before", "timeout": 30 } ] } ],
    "Notification": [ { "matcher": "", "hooks": [ { "type": "prompt", "command": "notify me" } ] } ],
    "PostToolUse": [ { "matcher": "Write", "hooks": [ { "type": "command", "command": "echo after" } ] } ]
  }
}`
	if err := os.WriteFile(settings, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}

	a := claude.New(claude.Options{TargetRoot: tmp})
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	// Only the clean command-only event is captured.
	if len(out.Hooks) != 1 || out.Hooks[0].Event != "PostToolUse" {
		t.Fatalf("expected only the clean PostToolUse event captured, got %+v", out.Hooks)
	}

	ops, _, err := a.Render(secrets.ForRender(out), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	finalRaw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("re-read settings.json: %v", err)
	}
	var final map[string]any
	if err := json.Unmarshal(finalRaw, &final); err != nil {
		t.Fatalf("parse final settings.json: %v\n%s", err, finalRaw)
	}
	hooks, ok := final["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("final settings.json has no hooks object:\n%s", finalRaw)
	}

	// handler navigates to hooks[event][0].hooks[0], the only handler in each
	// fixture event.
	handler := func(t *testing.T, event string) map[string]any {
		t.Helper()
		defs, ok := hooks[event].([]any)
		if !ok || len(defs) == 0 {
			t.Fatalf("event %q missing from final file:\n%s", event, finalRaw)
		}
		def, ok := defs[0].(map[string]any)
		if !ok {
			t.Fatalf("event %q def[0] not an object", event)
		}
		hs, ok := def["hooks"].([]any)
		if !ok || len(hs) == 0 {
			t.Fatalf("event %q has no handlers", event)
		}
		h, ok := hs[0].(map[string]any)
		if !ok {
			t.Fatalf("event %q handler[0] not an object", event)
		}
		return h
	}

	t.Run("extra-field hook left untouched", func(t *testing.T) {
		h := handler(t, "PreToolUse")
		if h["command"] != "echo before" {
			t.Errorf("PreToolUse command corrupted: %+v", h)
		}
		// The unmodeled timeout field must survive verbatim (it was never
		// captured, so the array was never owned/overwritten).
		if _, ok := h["timeout"]; !ok {
			t.Errorf("PreToolUse lost its unmodeled timeout field: %+v", h)
		}
	})

	t.Run("non-command handler left untouched", func(t *testing.T) {
		h := handler(t, "Notification")
		if h["type"] != "prompt" || h["command"] != "notify me" {
			t.Errorf("Notification prompt handler corrupted: %+v", h)
		}
	})

	t.Run("clean hook round-trips", func(t *testing.T) {
		h := handler(t, "PostToolUse")
		if h["type"] != "command" || h["command"] != "echo after" {
			t.Errorf("PostToolUse did not round-trip: %+v", h)
		}
	})

	t.Run("no empty-command handler emitted", func(t *testing.T) {
		// A regression would emit {"type":"prompt","command":""} (or any
		// command:"") for a guarded handler. Assert none appears anywhere.
		if bytes.Contains(finalRaw, []byte(`"command": ""`)) || bytes.Contains(finalRaw, []byte(`"command":""`)) {
			t.Errorf("final settings.json contains an empty-command handler:\n%s", finalRaw)
		}
	})
}

// TestRenderHooks_SkipsNonCommandHandler asserts Render reports a dropped Skip
// for a non-command handler and never emits an empty-command entry, while
// command and empty-type handlers render normally (empty type is
// command-compatible, per the Gemini precedent).
func TestRenderHooks_SkipsNonCommandHandler(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name       string
		hookType   string
		wantSkip   bool
		wantRender bool
	}{
		{name: "command handler renders", hookType: "command", wantSkip: false, wantRender: true},
		{name: "empty type renders", hookType: "", wantSkip: false, wantRender: true},
		{name: "prompt handler skipped", hookType: "prompt", wantSkip: true, wantRender: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			in := source.Canonical{
				Hooks: []source.Hook{{
					Event:   "PreToolUse",
					Matcher: "Bash",
					Type:    tt.hookType,
					Command: "echo hi",
				}},
			}
			a := claude.New(claude.Options{TargetRoot: tmp})
			ops, skips, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			var gotHookSkip bool
			for _, s := range skips {
				if s.Component == "hook" && s.Name == "PreToolUse" {
					gotHookSkip = true
					if s.Kind != adapter.SkipDropped {
						t.Errorf("hook skip kind = %v, want SkipDropped", s.Kind)
					}
				}
			}
			if gotHookSkip != tt.wantSkip {
				t.Errorf("got hook skip = %v, want %v (skips=%+v)", gotHookSkip, tt.wantSkip, skips)
			}

			// A settings.json op must never carry an empty-command handler.
			var renderedHooks bool
			for _, op := range ops {
				if !strings.HasSuffix(op.Path, "settings.json") {
					continue
				}
				if bytes.Contains(op.Content, []byte(`"hooks"`)) {
					renderedHooks = true
				}
				if bytes.Contains(op.Content, []byte(`"command": ""`)) {
					t.Errorf("settings.json op emitted an empty-command handler:\n%s", op.Content)
				}
			}
			if renderedHooks != tt.wantRender {
				t.Errorf("rendered hooks op = %v, want %v", renderedHooks, tt.wantRender)
			}
		})
	}
}

// TestIngest_HookGuardWarnsAndSkips captures the adapter's stderr and asserts
// that an event with an unmodeled handler field is absent from the ingested
// model and produces an "event not captured" warning.
func TestIngest_HookGuardWarnsAndSkips(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	native := `{
  "hooks": {
    "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": "echo before", "timeout": 30 } ] } ]
  }
}`
	if err := os.WriteFile(settings, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	a := claude.New(claude.Options{TargetRoot: tmp, Stderr: &warn})
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.Hooks) != 0 {
		t.Fatalf("guarded event should be absent from ingested hooks, got %+v", out.Hooks)
	}
	got := warn.String()
	for _, want := range []string{`unmodeled fields (timeout)`, `event not captured`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing warning %q in:\n%s", want, got)
		}
	}
}
