package gemini_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderHooksContent renders c and returns the `.gemini/settings.json` op bytes.
func renderHooksContent(t *testing.T, a *gemini.Adapter, c source.Canonical) []byte {
	t.Helper()
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	op := findOp(ops, ".gemini/settings.json")
	if op == nil {
		t.Fatal("settings.json op missing")
	}
	return op.Content
}

// TestGeminiHookMultiHandlerRoundTrip anchors to a spec-complete on-disk
// settings.json whose `hooks.BeforeTool` has ONE matcher group carrying TWO
// {type, command} handlers. Ingest flattens it to two canonical Hooks sharing
// (event, matcher); render must COALESCE them back into a single group with a
// two-element `hooks` array (not two single-handler groups), and a second
// ingest→render must be byte-identical (idempotent) against the rendered section.
func TestGeminiHookMultiHandlerRoundTrip(t *testing.T) {
	root := t.TempDir()
	geminiDir := filepath.Join(root, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Spec-complete native fixture: one BeforeTool group, two handlers, plus a
	// foreign key (`theme`) the adapter must not disturb.
	fixture := `{
  "theme": "GitHub",
  "hooks": {
    "BeforeTool": [
      {
        "matcher": "write_file",
        "hooks": [
          { "type": "command", "command": "echo one" },
          { "type": "command", "command": "echo two" }
        ]
      }
    ]
  }
}
`
	if err := os.WriteFile(filepath.Join(geminiDir, "settings.json"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}

	a := gemini.New(gemini.Options{TargetRoot: root})
	c1, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// The native 2-handler group flattens to two flat Hooks sharing (event, matcher).
	if len(c1.Hooks) != 2 {
		t.Fatalf("want 2 flat hooks from the 2-handler group, got %d: %+v", len(c1.Hooks), c1.Hooks)
	}

	body1 := renderHooksContent(t, a, c1)
	var ours map[string]any
	if err := json.Unmarshal(body1, &ours); err != nil {
		t.Fatalf("render not JSON: %v\n%s", err, body1)
	}
	groups, ok := ours["hooks"].(map[string]any)["BeforeTool"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("want a SINGLE coalesced group, got %v: %s", ours["hooks"], body1)
	}
	group := groups[0].(map[string]any)
	if group["matcher"] != "write_file" {
		t.Fatalf("matcher lost: %v", group)
	}
	handlers, ok := group["hooks"].([]any)
	if !ok || len(handlers) != 2 {
		t.Fatalf("want a 2-element hooks array, got %v: %s", group["hooks"], body1)
	}
	if handlers[0].(map[string]any)["command"] != "echo one" || handlers[1].(map[string]any)["command"] != "echo two" {
		t.Fatalf("handler order/content wrong: %v", handlers)
	}

	// Idempotent: apply c1's render to disk (merge preserves `theme`), re-ingest,
	// re-render, and assert the owned hooks projection is byte-identical.
	renderApply(t, a, c1, adapter.ScopeUser, "")
	c2, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	body2 := renderHooksContent(t, a, c2)
	if !bytes.Equal(body1, body2) {
		t.Fatalf("render is not idempotent:\n first: %s\nsecond: %s", body1, body2)
	}
	// The foreign key survived the round-trip on disk.
	merged, err := os.ReadFile(filepath.Join(geminiDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(merged, []byte("GitHub")) {
		t.Fatalf("foreign `theme` key must be preserved: %s", merged)
	}
}

// TestGeminiHookRenderOmitsEmptyType asserts a canonical Hook with an empty Type
// renders a handler with NO "type" key — never a stray "type":"".
func TestGeminiHookRenderOmitsEmptyType(t *testing.T) {
	c := source.Canonical{Hooks: []source.Hook{
		{Event: "Stop", Type: "", Command: "echo x"}, // Type deliberately omitted
	}}
	body := renderHooksContent(t, gemini.New(gemini.Options{TargetRoot: t.TempDir()}), c)

	var ours map[string]any
	if err := json.Unmarshal(body, &ours); err != nil {
		t.Fatalf("render not JSON: %v", err)
	}
	group := ours["hooks"].(map[string]any)["AfterAgent"].([]any)[0].(map[string]any)
	handler := group["hooks"].([]any)[0].(map[string]any)
	if _, ok := handler["type"]; ok {
		t.Fatalf("empty Type must be OMITTED, not emitted: %s", body)
	}
	if handler["command"] != "echo x" {
		t.Fatalf("command lost: %v", handler)
	}
	if bytes.Contains(body, []byte(`"type": ""`)) {
		t.Fatalf(`must never emit "type": "": %s`, body)
	}
}

// TestGeminiHookAlwaysFireMatcher pins that a tool event keeps its matcher while
// an always-fire event drops a non-empty matcher with a reported SkipReduced (the
// matcher is never silently emitted into a position Gemini ignores).
func TestGeminiHookAlwaysFireMatcher(t *testing.T) {
	tests := []struct {
		name        string
		hook        source.Hook
		geminiEvent string
		wantMatcher string
		wantSkip    bool
	}{
		{
			name:        "tool event keeps matcher",
			hook:        source.Hook{Event: "PreToolUse", Matcher: "write_file", Type: "command", Command: "echo a"},
			geminiEvent: "BeforeTool",
			wantMatcher: "write_file",
			wantSkip:    false,
		},
		{
			name:        "always-fire BeforeAgent drops matcher with SkipReduced",
			hook:        source.Hook{Event: "UserPromptSubmit", Matcher: "ignored", Type: "command", Command: "echo b"},
			geminiEvent: "BeforeAgent",
			wantMatcher: "",
			wantSkip:    true,
		},
		{
			name:        "always-fire SessionStart drops matcher with SkipReduced",
			hook:        source.Hook{Event: "SessionStart", Matcher: "x", Type: "command", Command: "echo c"},
			geminiEvent: "SessionStart",
			wantMatcher: "",
			wantSkip:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := source.Canonical{Hooks: []source.Hook{tt.hook}}
			ops, skips, err := gemini.New(gemini.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
			if err != nil {
				t.Fatal(err)
			}
			op := findOp(ops, ".gemini/settings.json")
			if op == nil {
				t.Fatal("settings.json op missing")
			}
			var ours map[string]any
			if err := json.Unmarshal(op.Content, &ours); err != nil {
				t.Fatal(err)
			}
			group := ours["hooks"].(map[string]any)[tt.geminiEvent].([]any)[0].(map[string]any)
			if group["matcher"] != tt.wantMatcher {
				t.Fatalf("matcher = %q, want %q: %s", group["matcher"], tt.wantMatcher, op.Content)
			}
			sk := hasSkip(skips, "hook", tt.hook.Event.String(), adapter.SkipReduced)
			if tt.wantSkip && sk == nil {
				t.Fatalf("want a matcher SkipReduced for always-fire event, got %+v", skips)
			}
			if !tt.wantSkip && sk != nil {
				t.Fatalf("did not expect a matcher SkipReduced, got %+v", sk)
			}
		})
	}
}
