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
