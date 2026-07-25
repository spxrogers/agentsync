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
	// Modify the captured command so the rendered PostToolUse DIFFERS from the
	// native fixture. Otherwise a no-op Apply (or a stubbed Render) would leave
	// "echo after" in place and the round-trip assertion below would pass
	// vacuously — a correct Apply must materialize this new value through the
	// owned-key merge while leaving the guarded foreign events untouched.
	out.Hooks[0].Command = "echo after (rerendered)"

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

	t.Run("clean hook round-trips through a real merge", func(t *testing.T) {
		h := handler(t, "PostToolUse")
		// The rerendered value proves Apply actually wrote via the owned-key merge
		// (a no-op Apply would leave the native "echo after").
		if h["type"] != "command" || h["command"] != "echo after (rerendered)" {
			t.Errorf("PostToolUse did not round-trip through a real render/merge: %+v", h)
		}
	})

	t.Run("no empty-command handler emitted", func(t *testing.T) {
		// A regression would emit {"type":"prompt","command":""} (or any
		// command:"") for a guarded handler. Walk the PARSED object (not the
		// formatted bytes) so the check is independent of the encoder's spacing.
		for event, raw := range hooks {
			defs, _ := raw.([]any)
			for _, rawDef := range defs {
				def, _ := rawDef.(map[string]any)
				hs, _ := def["hooks"].([]any)
				for _, rawH := range hs {
					h, _ := rawH.(map[string]any)
					if cmd, ok := h["command"].(string); ok && cmd == "" {
						t.Errorf("event %q has a handler with an empty command: %+v", event, h)
					}
				}
			}
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

			// A settings.json op must never carry an empty-command handler. Parse
			// the op payload and walk it (not a spacing-coupled byte substring) so
			// the negative assertion can't pass vacuously if the encoder changes.
			var renderedHooks bool
			for _, op := range ops {
				if !strings.HasSuffix(op.Path, "settings.json") {
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal(op.Content, &payload); err != nil {
					t.Fatalf("parse settings.json op: %v\n%s", err, op.Content)
				}
				hooksObj, _ := payload["hooks"].(map[string]any)
				if len(hooksObj) > 0 {
					renderedHooks = true
				}
				for event, raw := range hooksObj {
					defs, _ := raw.([]any)
					for _, rawDef := range defs {
						def, _ := rawDef.(map[string]any)
						hs, _ := def["hooks"].([]any)
						for _, rawH := range hs {
							h, _ := rawH.(map[string]any)
							if cmd, ok := h["command"].(string); ok && cmd == "" {
								t.Errorf("event %q op emitted an empty-command handler: %+v", event, h)
							}
						}
					}
				}
			}
			if renderedHooks != tt.wantRender {
				t.Errorf("rendered hooks op = %v, want %v", renderedHooks, tt.wantRender)
			}
		})
	}
}

// TestIngest_HookGuardWarnsAndSkips captures the adapter's stderr and asserts
// that EVERY kind of unrepresentable hook event — an unmodeled field, a
// non-command handler, and a structurally malformed (non-object) definition or
// handler — is left out of the ingested model AND produces an "event not
// captured" warning, honouring the "warn on anything it cannot capture"
// contract.
func TestIngest_HookGuardWarnsAndSkips(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name      string
		hooks     string // the JSON value of the settings.json "hooks" object
		wantWarns []string
	}{
		{
			name:      "unmodeled handler field",
			hooks:     `{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": "echo before", "timeout": 30 } ] } ] }`,
			wantWarns: []string{"unmodeled fields (\"timeout\")", "event not captured"},
		},
		{
			name:      "non-command handler",
			hooks:     `{ "Notification": [ { "matcher": "", "hooks": [ { "type": "prompt", "command": "notify me" } ] } ] }`,
			wantWarns: []string{`"prompt"-type handler`, "event not captured"},
		},
		{
			name:      "malformed definition (not an object)",
			hooks:     `{ "PreToolUse": [ "echo hi" ] }`,
			wantWarns: []string{"malformed definition", "event not captured"},
		},
		{
			name:      "malformed handler (not an object)",
			hooks:     `{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ "echo hi" ] } ] }`,
			wantWarns: []string{"malformed handler", "event not captured"},
		},
		{
			name:      "hooks value is not an array",
			hooks:     `{ "PreToolUse": [ { "matcher": "Bash", "hooks": { "not": "an array" } } ] }`,
			wantWarns: []string{`without a valid "hooks" array`, "event not captured"},
		},
		{
			name:      "hooks value is null",
			hooks:     `{ "PreToolUse": [ { "matcher": "Bash", "hooks": null } ] }`,
			wantWarns: []string{`without a valid "hooks" array`, "event not captured"},
		},
		{
			name:      "hooks key absent",
			hooks:     `{ "PreToolUse": [ { "matcher": "Bash" } ] }`,
			wantWarns: []string{`without a valid "hooks" array`, "event not captured"},
		},
		{
			name:      "event value is not an array",
			hooks:     `{ "PreToolUse": { "matcher": "Bash" } }`,
			wantWarns: []string{"value is not an array", "event not captured"},
		},
		{
			name:      "handler type is not a string",
			hooks:     `{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": 5, "command": "x" } ] } ] }`,
			wantWarns: []string{`"type" is not a string`, "event not captured"},
		},
		{
			name:      "matcher is not a string",
			hooks:     `{ "PreToolUse": [ { "matcher": 5, "hooks": [ { "type": "command", "command": "x" } ] } ] }`,
			wantWarns: []string{`"matcher" is not a string`, "event not captured"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			settings := filepath.Join(tmp, ".claude", "settings.json")
			if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settings, []byte(`{ "hooks": `+tt.hooks+` }`), 0o644); err != nil {
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
			for _, want := range tt.wantWarns {
				if !strings.Contains(got, want) {
					t.Errorf("missing warning %q in:\n%s", want, got)
				}
			}
		})
	}
}

// TestRefusedHookEvents_StructuralVsSemantic pins the retirement gate's
// classification at the contract surface: RefusedHookEvents returns ONLY
// semantically refused events (unmodeled fields, non-command handlers on
// well-formed entries). Every structurally-malformed shape — a settings.json
// typo — warns at Ingest but must NOT appear here, because import deletes the
// canonical hooks/<event>.toml for every returned event and a native typo must
// never be destructive. One subtest per structural refusal site in ingestHooks.
func TestRefusedHookEvents_StructuralVsSemantic(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name        string
		hooks       string // JSON value of the settings.json "hooks" object
		wantRefused bool
	}{
		{
			"semantic: unmodeled def field",
			`{ "PreToolUse": [ { "matcher": "Bash", "sequential": true, "hooks": [ { "type": "command", "command": "x" } ] } ] }`, true,
		},
		{
			"semantic: unmodeled handler field",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": "x", "timeout": 30 } ] } ] }`, true,
		},
		{
			"semantic: non-command handler",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "prompt", "command": "x" } ] } ] }`, true,
		},
		{
			"semantic: non-command handler without a command (type wins over the absent-command structural check)",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "prompt" } ] } ] }`, true,
		},
		{
			"structural: event value not an array",
			`{ "PreToolUse": { "matcher": "Bash" } }`, false,
		},
		{
			"structural: definition not an object",
			`{ "PreToolUse": [ "oops" ] }`, false,
		},
		{
			"structural: matcher not a string",
			`{ "PreToolUse": [ { "matcher": 5, "hooks": [ { "type": "command", "command": "x" } ] } ] }`, false,
		},
		{
			"structural: missing hooks array",
			`{ "PreToolUse": [ { "matcher": "Bash" } ] }`, false,
		},
		{
			"structural: handler not an object",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ 5 ] } ] }`, false,
		},
		{
			"structural: handler type not a string",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": 5, "command": "x" } ] } ] }`, false,
		},
		{
			"structural: handler command not a string",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": 123 } ] } ] }`, false,
		},
		{
			"structural: handler without a command",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "command" } ] } ] }`, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			settings := filepath.Join(tmp, ".claude", "settings.json")
			if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settings, []byte(`{ "hooks": `+tt.hooks+` }`), 0o644); err != nil {
				t.Fatal(err)
			}
			a := claude.New(claude.Options{TargetRoot: tmp})
			refused, err := a.RefusedHookEvents(adapter.ScopeUser, "")
			if err != nil {
				t.Fatalf("RefusedHookEvents: %v", err)
			}
			isRefused := len(refused) == 1 && refused[0] == "PreToolUse"
			if tt.wantRefused && !isRefused {
				t.Fatalf("semantic refusal should surface PreToolUse; got %v", refused)
			}
			if !tt.wantRefused && len(refused) != 0 {
				t.Fatalf("structural malformation must never trigger retirement; got %v", refused)
			}
		})
	}
}
