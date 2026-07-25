package gemini_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestRefusedHookEvents_StructuralVsSemantic mirrors the claude twin for the
// Gemini implementation of adapter.HookIngestGuard, with two Gemini-specific
// legs: refused events are reported under their CANONICAL spelling (the native
// "BeforeTool" surfaces as "PreToolUse" — the name import retires), and a
// Gemini-only event with no canonical equivalent is NEVER refused even when it
// carries semantic-refusal content, because no canonical hooks/<event>.toml
// can exist for it. The settings fixture carries a JSONC comment on every row,
// pinning that RefusedHookEvents parses settings.json the same way Ingest does.
func TestRefusedHookEvents_StructuralVsSemantic(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name        string
		hooks       string // JSON value of the settings.json "hooks" object
		wantRefused bool
	}{
		{
			"semantic: unmodeled def field",
			`{ "BeforeTool": [ { "matcher": "Bash", "sequential": true, "hooks": [ { "type": "command", "command": "x" } ] } ] }`, true,
		},
		{
			"semantic: unmodeled handler field",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": "x", "timeout": 30 } ] } ] }`, true,
		},
		{
			"semantic: non-command handler",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": "prompt", "command": "x" } ] } ] }`, true,
		},
		{
			"semantic: non-command handler without a command (type wins over the absent-command structural check)",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": "prompt" } ] } ] }`, true,
		},
		{
			"semantic: unmodeled handler field without a command (unmodeled wins over the absent-command structural check)",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": "command", "timeout": 30 } ] } ] }`, true,
		},
		{
			"semantic: typeless converted engine shape (unmodeled field, no type, no command)",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "prompt": "do it" } ] } ] }`, true,
		},
		{
			"structural: event value not an array",
			`{ "BeforeTool": { "matcher": "Bash" } }`, false,
		},
		{
			"structural: definition not an object",
			`{ "BeforeTool": [ "oops" ] }`, false,
		},
		{
			"structural: matcher not a string",
			`{ "BeforeTool": [ { "matcher": 5, "hooks": [ { "type": "command", "command": "x" } ] } ] }`, false,
		},
		{
			"structural: missing hooks array",
			`{ "BeforeTool": [ { "matcher": "Bash" } ] }`, false,
		},
		{
			"structural: handler not an object",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ 5 ] } ] }`, false,
		},
		{
			"structural: handler type not a string",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": 5, "command": "x" } ] } ] }`, false,
		},
		{
			"structural: handler command not a string",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": 123 } ] } ] }`, false,
		},
		{
			"structural: handler without a command",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": "command" } ] } ] }`, false,
		},
		{
			"gemini-only event is never refused, even on semantic content",
			`{ "BeforeModel": [ { "matcher": "x", "hooks": [ { "type": "command", "command": "x", "timeout": 30 } ] } ] }`, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			settings := filepath.Join(tmp, ".gemini", "settings.json")
			if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
				t.Fatal(err)
			}
			jsonc := "{ // gemini reads settings.json as JSONC\n\"hooks\": " + tt.hooks + " }"
			if err := os.WriteFile(settings, []byte(jsonc), 0o644); err != nil {
				t.Fatal(err)
			}
			a := gemini.New(gemini.Options{TargetRoot: tmp})
			refused, err := a.RefusedHookEvents(adapter.ScopeUser, "")
			if err != nil {
				t.Fatalf("RefusedHookEvents: %v", err)
			}
			isRefused := len(refused) == 1 && refused[0] == "PreToolUse"
			if tt.wantRefused && !isRefused {
				t.Fatalf("semantic refusal should surface canonical PreToolUse; got %v", refused)
			}
			if !tt.wantRefused && len(refused) != 0 {
				t.Fatalf("this shape must never trigger retirement; got %v", refused)
			}
		})
	}
}

// TestIngest_RefusesMalformedEntryShapes pins the CAPTURE side of the
// structural coercion guards (gemini twin — see the claude version for the
// full rationale): each shape must leave the event uncaptured rather than
// captured with asStr-coerced fields the next apply would then write over the
// user's native handler.
func TestIngest_RefusesMalformedEntryShapes(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name  string
		hooks string
	}{
		{
			"non-string matcher",
			`{ "BeforeTool": [ { "matcher": 5, "hooks": [ { "type": "command", "command": "x" } ] } ] }`,
		},
		{
			"non-string type",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": 5, "command": "x" } ] } ] }`,
		},
		{
			"non-string command",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": 123 } ] } ] }`,
		},
		{
			"absent command",
			`{ "BeforeTool": [ { "matcher": "Bash", "hooks": [ { "type": "command" } ] } ] }`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			settings := filepath.Join(tmp, ".gemini", "settings.json")
			if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settings, []byte(`{ "hooks": `+tt.hooks+` }`), 0o644); err != nil {
				t.Fatal(err)
			}
			a := gemini.New(gemini.Options{TargetRoot: tmp})
			out, err := a.Ingest(adapter.ScopeUser, "")
			if err != nil {
				t.Fatalf("Ingest: %v", err)
			}
			if len(out.Hooks) != 0 {
				t.Fatalf("malformed shape must leave the event uncaptured, got %+v", out.Hooks)
			}
		})
	}
}
