package generic

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

func TestComponentImportGuardScope(t *testing.T) {
	tests := []struct {
		name      string
		harness   string
		component string
		refuse    bool
	}{
		{name: "copilot hook", harness: "copilot", component: "hook", refuse: true},
		{name: "copilot command", harness: "copilot", component: "command"},
		{name: "copilot subagent", harness: "copilot", component: "subagent"},
		{name: "copilot whole agent", harness: "copilot"},
		{name: "factory hook", harness: "factory", component: "hook"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := New(Spec{Name: tc.harness}, Options{}).CheckComponentImport(tc.component)
			if (err != nil) != tc.refuse {
				t.Fatalf("CheckComponentImport(%q) = %v, refusal expected: %v", tc.component, err, tc.refuse)
			}
		})
	}
}

func TestCopilotHooksAreSkippedDuringIngest(t *testing.T) {
	for _, scope := range []adapter.Scope{adapter.ScopeUser, adapter.ScopeProject} {
		t.Run(scope.String(), func(t *testing.T) {
			home := t.TempDir()
			var spec Spec
			for _, candidate := range Specs() {
				if candidate.Name == "copilot" {
					spec = candidate
				}
			}
			native := filepath.Join(home, ".copilot")
			if scope == adapter.ScopeProject {
				native = filepath.Join(home, ".github")
			}
			agents := filepath.Join(native, "agents")
			if err := os.MkdirAll(agents, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(agents, "reviewer.agent.md"), []byte("---\ndescription: Review changes\n---\nReview only.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			hookPath := filepath.Join(native, "hooks", "agentsync.json")
			if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
				t.Fatal(err)
			}
			var warnings bytes.Buffer
			a := New(spec, Options{TargetRoot: home, Stderr: &warnings})
			for _, content := range []string{`{"version":1,"hooks":{"preToolUse":[{"type":"command","command":"review-hook"}]}}`, "{"} {
				if err := os.WriteFile(hookPath, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				warnings.Reset()
				got, err := a.Ingest(scope, home)
				if err != nil || len(got.Subagents) != 1 || len(got.Hooks) != 0 || !strings.Contains(warnings.String(), "hooks were not captured") {
					t.Fatalf("hook file blocked unrelated ingest: %v %+v %s", err, got, warnings.String())
				}
			}
			if err := os.Remove(hookPath); err != nil {
				t.Fatal(err)
			}
			warnings.Reset()
			if _, err := a.Ingest(scope, home); err != nil || warnings.Len() != 0 {
				t.Fatalf("absent hooks caused an error or warning: %v %s", err, warnings.String())
			}
		})
	}
}

func TestVSCodeFileWriteBackGuardScope(t *testing.T) {
	tests := []struct {
		name    string
		harness string
		id      string
		refuse  bool
	}{
		{name: "copilot command", harness: "copilot", id: "commands/verify.md", refuse: true},
		{name: "copilot agent", harness: "copilot", id: "subagents/reviewer.md", refuse: true},
		{name: "copilot instruction", harness: "copilot", id: "memory/fragments/nested/python.instructions.md", refuse: true},
		{name: "copilot global memory", harness: "copilot", id: "memory/AGENTS.md"},
		{name: "local global memory", harness: "vscode-local", id: "memory/AGENTS.md", refuse: true},
		{name: "local bundled skill", harness: "vscode-local", id: "skills/review/references/check.txt"},
		{name: "copilot skill", harness: "copilot", id: "skills/review/SKILL.md"},
		{name: "other generic command", harness: "zed", id: "commands/verify.md"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op := adapter.FileOp{SourceID: filepath.FromSlash(tc.id)}
			var err error
			if tc.harness == "vscode-local" {
				err = CheckVSCodeFileWriteBack(op, tc.harness)
			} else {
				err = New(Spec{Name: tc.harness}, Options{}).CheckFileWriteBack(op)
			}
			if (err != nil) != tc.refuse {
				t.Fatalf("CheckFileWriteBack(%q) = %v, refusal expected: %v", tc.id, err, tc.refuse)
			}
		})
	}
}

func TestCopilotHarnessResources(t *testing.T) {
	home := t.TempDir()
	var spec Spec
	for _, candidate := range Specs() {
		if candidate.Name == "copilot" {
			spec = candidate
		}
	}
	c := source.Canonical{
		Subagents:  []source.Subagent{{Name: "reviewer", Frontmatter: map[string]any{"description": "Review changes", "tools": "Read, Grep"}, Body: "Review only."}},
		Commands:   []source.Command{{Name: "verify", Frontmatter: map[string]any{"description": "Verify changes"}, Body: "Run checks."}},
		MCPServers: []source.MCPServer{{ID: "example", Server: source.MCPServerSpec{Command: "example-server"}}},
	}
	for _, scope := range []adapter.Scope{adapter.ScopeUser, adapter.ScopeProject} {
		ops, skips, err := New(spec, Options{TargetRoot: home}).Render(secrets.ForRender(c), scope, home)
		if err != nil || len(skips) != 0 || len(ops) != 3 {
			t.Fatalf("scope %v: %v %+v %+v", scope, err, ops, skips)
		}
		var mcp map[string]any
		if err := json.Unmarshal(ops[0].Content, &mcp); err != nil {
			t.Fatal(err)
		}
		key, transport := "mcpServers", "local"
		if scope == adapter.ScopeProject {
			key, transport = "servers", "stdio"
		}
		server := mcp[key].(map[string]any)["example"].(map[string]any)
		if server["type"] != transport {
			t.Fatalf("wrong MCP dialect: %+v", mcp)
		}
		if !strings.HasSuffix(ops[1].Path, "reviewer.agent.md") || !strings.Contains(string(ops[1].Content), "read") {
			t.Fatal("agent projection missing")
		}
		if filepath.Base(ops[2].Path) != "SKILL.md" || !strings.Contains(string(ops[2].Content), "disable-model-invocation: true") {
			t.Fatal("command must become a manual skill")
		}
	}
}

func TestCopilotRefusesLossyAgentAndCommand(t *testing.T) {
	c := source.Canonical{
		Subagents: []source.Subagent{{Name: "restricted", Frontmatter: map[string]any{"description": "Restricted", "disallowedTools": "Bash"}}},
		Commands:  []source.Command{{Name: "restricted", Frontmatter: map[string]any{"tools": []string{"read"}}}},
	}
	ops, skips, err := RenderVSCodeResources(c, "copilot", t.TempDir(), "", adapter.ScopeUser, "")
	if err != nil || len(ops) != 0 || len(skips) != 2 {
		t.Fatalf("lossy conversion accepted: %v %+v %+v", err, ops, skips)
	}
}

func TestVSCodeAgentToolsDoNotMutateSource(t *testing.T) {
	tools := []string{"Read", "Grep"}
	_, reason := vscodeAgentFrontmatter(map[string]any{"description": "Review", "tools": tools}, "copilot")
	if reason != "" || tools[0] != "Read" || tools[1] != "Grep" {
		t.Fatalf("source tools changed: %v %s", tools, reason)
	}
}

func TestCopilotCommandSkillCollisionSkipsOnlyCommand(t *testing.T) {
	c := source.Canonical{Skills: []source.Skill{{Name: "review"}}, Commands: []source.Command{{Name: "review"}}}
	ops, skips, err := RenderVSCodeResources(c, "copilot", t.TempDir(), "", adapter.ScopeUser, "")
	if err != nil || len(ops) != 0 || len(skips) != 1 || skips[0].Component != "command" {
		t.Fatalf("collision failed entire plan: %v %+v %+v", err, ops, skips)
	}
}

func TestVSCodeConditionalInstructions(t *testing.T) {
	home := t.TempDir()
	fragments := map[string]string{"python.instructions.md": "---\napplyTo: '**/*.py'\n---\nUse pytest.\n", "ordinary.md": "Shared text."}
	for _, harness := range []string{"copilot", "vscode-local"} {
		ops, err := RenderVSCodeInstructions(fragments, harness, home, filepath.Join(home, "prompts"), adapter.ScopeUser, "")
		if err != nil || len(ops) != 1 || !strings.Contains(string(ops[0].Content), "**/*.py") {
			t.Fatalf("instructions %s: %v %+v", harness, err, ops)
		}
	}
	if _, err := RenderVSCodeInstructions(map[string]string{"../escape.instructions.md": "x"}, "copilot", home, "", adapter.ScopeUser, ""); err == nil {
		t.Fatal("accepted escaping instruction path")
	}
}

func TestCopilotHooksRequireReviewAndNativeEvents(t *testing.T) {
	c := source.Canonical{Hooks: []source.Hook{{Event: untrusted.Wrap("preToolUse"), Command: "review-command", Timeout: 10}, {Event: untrusted.Wrap("PreToolUse"), Command: "legacy-command"}}}
	home := t.TempDir()
	ops, skips, err := renderCopilotHooks(c, home, adapter.ScopeUser, "")
	if err != nil || len(ops) != 0 || len(skips) != 2 {
		t.Fatal("hooks activated without explicit review")
	}
	c.Config.Copilot.HooksEnabled = true
	ops, skips, err = renderCopilotHooks(c, home, adapter.ScopeUser, "")
	if err != nil || len(ops) != 1 || len(skips) != 1 || strings.Contains(string(ops[0].Content), "legacy-command") || !strings.Contains(string(ops[0].Content), "timeoutSec") {
		t.Fatalf("native hooks: %v %+v %+v", err, ops, skips)
	}
}

func TestCopilotHarnessRoundTrip(t *testing.T) {
	home := t.TempDir()
	var spec Spec
	for _, candidate := range Specs() {
		if candidate.Name == "copilot" {
			spec = candidate
		}
	}
	a := New(spec, Options{TargetRoot: home})
	c := source.Canonical{
		Subagents: []source.Subagent{{Name: "reviewer", Frontmatter: map[string]any{"description": "Review", "tools": []string{"read"}}, Body: "Review only.\n"}},
		Commands:  []source.Command{{Name: "verify", Frontmatter: map[string]any{"description": "Verify"}, Body: "Run checks.\n"}},
		Memory:    source.Memory{Fragments: map[string]string{"nested/python.instructions.md": "---\napplyTo: '**/*.py'\n---\nUse pytest.\n"}},
	}
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil || len(got.Subagents) != 1 || len(got.Commands) != 1 || len(got.Skills) != 0 || len(got.Memory.Fragments) != 1 {
		t.Fatalf("round trip: %v %+v", err, got)
	}
	if got.Commands[0].Body != c.Commands[0].Body || got.Subagents[0].Body != c.Subagents[0].Body {
		t.Fatal("managed markers leaked into canonical text")
	}
	if err := os.WriteFile(filepath.Join(home, ".copilot", "skills", "verify", "extra.txt"), []byte("Keep this file.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ingest(adapter.ScopeUser, ""); err == nil {
		t.Fatal("accepted lossy command import with bundled files")
	}
}
