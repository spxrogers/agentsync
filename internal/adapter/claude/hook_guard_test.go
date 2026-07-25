package claude_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestIngest_RefusesMalformedEntryShapes pins the CAPTURE side of the
// structural coercion guards (round-1 adversarial + round-2 findings): before
// the guards, a handler whose matcher/type/command was a non-string — or whose
// command was absent — passed the unmodeled-key check, asStr coerced the field
// ("" match-all / "" type promoted to command / "" command), and the event was
// CAPTURED wrong; the next apply, owning the whole per-event array, then wrote
// the coerced values over the user's native handler (#124 class). Each shape
// must leave the event uncaptured (structural: warned, never retired — the
// refusal side is pinned by TestRefusedHookEvents_StructuralVsSemantic).
func TestIngest_RefusesMalformedEntryShapes(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name  string
		hooks string // JSON value of the settings.json "hooks" object
	}{
		{
			"non-string matcher",
			`{ "PreToolUse": [ { "matcher": 5, "hooks": [ { "type": "command", "command": "x" } ] } ] }`,
		},
		{
			"non-string type",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": 5, "command": "x" } ] } ] }`,
		},
		{
			"non-string command",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": 123 } ] } ] }`,
		},
		{
			"absent command",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "command" } ] } ] }`,
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
			"semantic: unmodeled handler field without a command (unmodeled wins over the absent-command structural check)",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "command", "timeout": 30 } ] } ] }`, true,
		},
		{
			"semantic: typeless converted engine shape (unmodeled field, no type, no command)",
			`{ "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "prompt": "do it" } ] } ] }`, true,
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
