package codex_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
	"github.com/spxrogers/agentsync/internal/testenv"
)

func writeCodexConfig(t *testing.T, tmp, hooksTOML string) {
	t.Helper()
	cfg := filepath.Join(tmp, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(hooksTOML), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRefusedHookEvents_StructuralVsSemantic mirrors the claude/gemini/cursor
// twins for the Codex implementation of adapter.HookIngestGuard, over Codex's
// TOML config, with the codex-specific divergence pinned: a non-command
// handler TYPE is NOT refused (codex parses-and-skips unknown types and
// renderHooks re-emits Type verbatim, so it round-trips losslessly) — but a
// handler converted to another engine shape (an unmodeled `prompt` field, no
// command) is refused SEMANTICALLY on its unmodeled field, not downgraded to
// a structural skip by its missing command.
func TestRefusedHookEvents_StructuralVsSemantic(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name        string
		config      string // full config.toml content
		wantRefused bool
	}{
		{
			"semantic: unmodeled def field",
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\nsequential = true\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"x\"\n", true,
		},
		{
			"semantic: unmodeled handler field",
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"x\"\ntimeout = 30\n", true,
		},
		{
			"semantic: converted engine shape (unmodeled field wins over the absent-command structural check)",
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n[[hooks.PreToolUse.hooks]]\ntype = \"prompt\"\nprompt = \"review\"\n", true,
		},
		{
			"codex divergence: non-command type with a command is representable — never refused",
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n[[hooks.PreToolUse.hooks]]\ntype = \"prompt\"\ncommand = \"x\"\n", false,
		},
		{
			"structural: event value not an array of tables",
			"[hooks.PreToolUse]\nmatcher = \"Bash\"\n", false,
		},
		{
			"structural: matcher not a string",
			"[[hooks.PreToolUse]]\nmatcher = 5\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"x\"\n", false,
		},
		{
			"structural: missing hooks array",
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n", false,
		},
		{
			"structural: handler type not a string",
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n[[hooks.PreToolUse.hooks]]\ntype = 5\ncommand = \"x\"\n", false,
		},
		{
			"structural: handler command not a string",
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = 123\n", false,
		},
		{
			"structural: handler without a command",
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\n", false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			writeCodexConfig(t, tmp, tt.config)
			a := codex.New(codex.Options{TargetRoot: tmp})
			refused, err := a.RefusedHookEvents(adapter.ScopeUser, "")
			if err != nil {
				t.Fatalf("RefusedHookEvents: %v", err)
			}
			isRefused := len(refused) == 1 && refused[0] == "PreToolUse"
			if tt.wantRefused && !isRefused {
				t.Fatalf("semantic refusal should surface PreToolUse; got %v", refused)
			}
			if !tt.wantRefused && len(refused) != 0 {
				t.Fatalf("this shape must never trigger retirement; got %v", refused)
			}
		})
	}
}

// TestIngest_RefusesMalformedEntryShapes pins the CAPTURE side for codex —
// each malformed shape must leave the event uncaptured instead of captured
// with asStr-coerced fields — plus the codex divergence positive: a
// non-command handler WITH a command is captured verbatim (Type preserved),
// because renderHooks re-emits it losslessly.
func TestIngest_RefusesMalformedEntryShapes(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name   string
		config string
	}{
		{
			"non-string matcher",
			"[[hooks.PreToolUse]]\nmatcher = 5\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"x\"\n",
		},
		{
			"non-string type",
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n[[hooks.PreToolUse.hooks]]\ntype = 5\ncommand = \"x\"\n",
		},
		{
			"non-string command",
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = 123\n",
		},
		{
			"absent command",
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			writeCodexConfig(t, tmp, tt.config)
			a := codex.New(codex.Options{TargetRoot: tmp})
			out, err := a.Ingest(adapter.ScopeUser, "")
			if err != nil {
				t.Fatalf("Ingest: %v", err)
			}
			if len(out.Hooks) != 0 {
				t.Fatalf("malformed shape must leave the event uncaptured, got %+v", out.Hooks)
			}
		})
	}

	t.Run("codex divergence: non-command type with a command is captured verbatim", func(t *testing.T) {
		tmp := t.TempDir()
		writeCodexConfig(t, tmp,
			"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n[[hooks.PreToolUse.hooks]]\ntype = \"prompt\"\ncommand = \"x\"\n")
		a := codex.New(codex.Options{TargetRoot: tmp})
		out, err := a.Ingest(adapter.ScopeUser, "")
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if len(out.Hooks) != 1 || out.Hooks[0].Type != "prompt" || out.Hooks[0].Command != "x" {
			t.Fatalf("representable non-command handler must be captured verbatim, got %+v", out.Hooks)
		}
	})
}
