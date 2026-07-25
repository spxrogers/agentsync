package claude_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestIngest_RefusesNonStringCommand pins the capture side of the non-string
// "command" guard (round-1 adversarial finding): before the guard, a handler
// like {"type":"command","command":123} passed the matcher/type/unmodeled-key
// checks, asStr coerced the command to "", and the event was CAPTURED as an
// empty-command hook — which the next apply, owning the whole per-event array,
// wrote over the user's native handler (#124 class). The event must now be
// left uncaptured (structural refusal: warned, never retired).
func TestIngest_RefusesNonStringCommand(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	native := `{ "hooks": { "PreToolUse": [ { "matcher": "Bash", "hooks": [ { "type": "command", "command": 123 } ] } ] } }`
	if err := os.WriteFile(settings, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude.New(claude.Options{TargetRoot: tmp})
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.Hooks) != 0 {
		t.Fatalf("non-string command must leave the event uncaptured, got %+v", out.Hooks)
	}
}
