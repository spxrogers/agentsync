package cursor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/cursor"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestRefusedHookEvents_StructuralVsSemantic mirrors the claude/gemini twins
// for the Cursor implementation of adapter.HookIngestGuard, over Cursor's FLAT
// entry shape, with the two renaming-adapter legs: refused events surface
// under their CANONICAL spelling (native "preToolUse" reports as "PreToolUse"
// — the name import retires), and a Cursor-only event with no canonical
// equivalent is NEVER refused even when it carries semantic-refusal content,
// because no canonical hooks/<event>.toml can exist for it.
func TestRefusedHookEvents_StructuralVsSemantic(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name        string
		hooks       string // JSON value of the hooks.json "hooks" object
		wantRefused bool
	}{
		{
			"semantic: unmodeled entry field",
			`{ "preToolUse": [ { "command": "x", "timeout": 30 } ] }`, true,
		},
		{
			"semantic: prompt-type entry",
			`{ "preToolUse": [ { "type": "prompt", "command": "x" } ] }`, true,
		},
		{
			"semantic: prompt-type entry without a command (type wins over the absent-command structural check)",
			`{ "preToolUse": [ { "type": "prompt" } ] }`, true,
		},
		{
			"structural: event value not an array",
			`{ "preToolUse": { "command": "x" } }`, false,
		},
		{
			"structural: entry not an object",
			`{ "preToolUse": [ "oops" ] }`, false,
		},
		{
			"structural: matcher not a string",
			`{ "preToolUse": [ { "command": "x", "matcher": 5 } ] }`, false,
		},
		{
			"structural: type not a string",
			`{ "preToolUse": [ { "command": "x", "type": 5 } ] }`, false,
		},
		{
			"structural: command not a string",
			`{ "preToolUse": [ { "command": 123 } ] }`, false,
		},
		{
			"structural: entry without a command",
			`{ "preToolUse": [ { "matcher": "Bash" } ] }`, false,
		},
		{
			"cursor-only event is never refused, even on semantic content",
			`{ "afterFileEdit": [ { "command": "x", "timeout": 30 } ] }`, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			hooksPath := filepath.Join(tmp, ".cursor", "hooks.json")
			if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
				t.Fatal(err)
			}
			native := `{ "version": 1, "hooks": ` + tt.hooks + ` }`
			if err := os.WriteFile(hooksPath, []byte(native), 0o644); err != nil {
				t.Fatal(err)
			}
			a := cursor.New(cursor.Options{TargetRoot: tmp})
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
// structural coercion guards (cursor twin, over the flat entry shape — see the
// claude version for the full rationale): each shape must leave the event
// uncaptured rather than captured with asStr-coerced fields ("" match-all /
// "" type promoted to command / "" command) the next apply would then write
// over the user's native entry.
func TestIngest_RefusesMalformedEntryShapes(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name  string
		hooks string
	}{
		{"non-string matcher", `{ "preToolUse": [ { "command": "x", "matcher": 5 } ] }`},
		{"non-string type", `{ "preToolUse": [ { "command": "x", "type": 5 } ] }`},
		{"non-string command", `{ "preToolUse": [ { "command": 123 } ] }`},
		{"absent command", `{ "preToolUse": [ { "matcher": "Bash" } ] }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			hooksPath := filepath.Join(tmp, ".cursor", "hooks.json")
			if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(hooksPath, []byte(`{ "version": 1, "hooks": `+tt.hooks+` }`), 0o644); err != nil {
				t.Fatal(err)
			}
			a := cursor.New(cursor.Options{TargetRoot: tmp})
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
