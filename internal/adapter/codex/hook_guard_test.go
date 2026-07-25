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
			`[[hooks.PreToolUse]]
matcher = "Bash"
sequential = true
[[hooks.PreToolUse.hooks]]
type = "command"
command = "x"
`, true,
		},
		{
			"semantic: unmodeled handler field",
			`[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = "command"
command = "x"
timeout = 30
`, true,
		},
		{
			"semantic: converted engine shape (unmodeled field wins over the absent-command structural check)",
			`[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = "prompt"
prompt = "review"
`, true,
		},
		{
			"semantic: unmodeled handler field with a non-string command (unmodeled wins over the non-string-command structural check)",
			`[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = "command"
command = 123
timeout = 30
`, true,
		},
		{
			"codex divergence: non-command type with a command is representable — never refused",
			`[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = "prompt"
command = "x"
`, false,
		},
		{
			"structural: event value not an array of tables",
			`[hooks.PreToolUse]
matcher = "Bash"
`, false,
		},
		{
			"structural: matcher not a string",
			`[[hooks.PreToolUse]]
matcher = 5
[[hooks.PreToolUse.hooks]]
type = "command"
command = "x"
`, false,
		},
		{
			"structural: missing hooks array",
			`[[hooks.PreToolUse]]
matcher = "Bash"
`, false,
		},
		{
			"structural: handler type not a string",
			`[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = 5
command = "x"
`, false,
		},
		{
			"structural: handler command not a string",
			`[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = "command"
command = 123
`, false,
		},
		{
			"structural: handler without a command",
			`[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = "command"
`, false,
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
// non-command handler WITH a command is captured verbatim (Type, Matcher, and
// Command all preserved), because renderHooks re-emits it losslessly.
func TestIngest_RefusesMalformedEntryShapes(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name   string
		config string
	}{
		{
			"non-string matcher",
			`[[hooks.PreToolUse]]
matcher = 5
[[hooks.PreToolUse.hooks]]
type = "command"
command = "x"
`,
		},
		{
			"non-string type",
			`[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = 5
command = "x"
`,
		},
		{
			"non-string command",
			`[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = "command"
command = 123
`,
		},
		{
			"absent command",
			`[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = "command"
`,
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
		writeCodexConfig(t, tmp, `[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = "prompt"
command = "x"
`)
		a := codex.New(codex.Options{TargetRoot: tmp})
		out, err := a.Ingest(adapter.ScopeUser, "")
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if len(out.Hooks) != 1 || out.Hooks[0].Type != "prompt" || out.Hooks[0].Command != "x" || out.Hooks[0].Matcher != "Bash" {
			t.Fatalf("representable non-command handler must be captured verbatim, got %+v", out.Hooks)
		}
	})
}
