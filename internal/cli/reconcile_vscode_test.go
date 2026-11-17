package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/source"
)

func TestReconcileVSCodeTransformedMarkdownRefusesWriteBack(t *testing.T) {
	tests := []struct {
		name    string
		harness string
		source  string
		dest    string
	}{
		{name: "copilot command", harness: "copilot", source: "commands/verify.md", dest: ".copilot/skills/verify/SKILL.md"},
		{name: "copilot agent", harness: "copilot", source: "subagents/reviewer.md", dest: ".copilot/agents/reviewer.agent.md"},
		{name: "copilot instructions", harness: "copilot", source: "memory/fragments/nested/python.instructions.md", dest: ".copilot/instructions/nested/python.instructions.md"},
		{name: "local command", harness: "vscode-local", source: "commands/verify.md", dest: ".vscode-local/prompts/verify.prompt.md"},
		{name: "local agent", harness: "vscode-local", source: "subagents/reviewer.md", dest: ".vscode-local/prompts/reviewer.agent.md"},
		{name: "local instructions", harness: "vscode-local", source: "memory/fragments/nested/python.instructions.md", dest: ".vscode-local/prompts/nested/python.instructions.md"},
		{name: "local global memory", harness: "vscode-local", source: "memory/AGENTS.md", dest: ".vscode-local/prompts/agentsync.instructions.md"},
	}
	for _, tc := range tests {
		for _, mode := range []string{"automatic", "interactive"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				home := filepath.Join(root, ".agentsync")
				c := source.Canonical{
					Memory: source.Memory{Body: "Global rules.\n", Fragments: map[string]string{"nested/python.instructions.md": "---\napplyTo: '**/*.py'\n---\nUse pytest.\n"}},
				}
				if err := source.WriteMemory(home, c.Memory); err != nil {
					t.Fatal(err)
				}
				if err := source.WriteCommand(home, source.Command{Name: "verify", Frontmatter: map[string]any{"description": "Verify changes"}, Body: "Run checks.\n"}); err != nil {
					t.Fatal(err)
				}
				if err := source.WriteSubagent(home, source.Subagent{Name: "reviewer", Frontmatter: map[string]any{"description": "Review changes", "tools": "Read"}, Body: "Review only.\n"}); err != nil {
					t.Fatal(err)
				}
				config := "[agents." + tc.harness + "]\nenabled = true\n[destination_directory_git_backup]\nmode = 'off'\n"
				if tc.harness == "vscode-local" {
					config += "[vscode_local]\nprompts_home = '" + filepath.Join(root, "profile", "prompts") + "'\n"
				}
				if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(config), 0o644); err != nil {
					t.Fatal(err)
				}
				env := map[string]string{"AGENTSYNC_TARGET_ROOT": root}
				mustRun(t, env, "apply")
				srcPath := filepath.Join(home, filepath.FromSlash(tc.source))
				before, err := os.ReadFile(srcPath)
				if err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(root, filepath.FromSlash(tc.dest))
				data, err := os.ReadFile(dest)
				if err != nil {
					t.Fatal(err)
				}
				data = append(data, []byte("\nNative edit.\n")...)
				if err := os.WriteFile(dest, data, 0o644); err != nil {
					t.Fatal(err)
				}
				var out string
				if mode == "automatic" {
					out, err = runCLI(t, env, "reconcile", "--auto-writeback")
				} else {
					out, err = runCLIWithStdin(t, env, "w", "reconcile")
				}
				if err == nil || !strings.Contains(out, "transformed Markdown") || !strings.Contains(out, "canonical") {
					t.Fatalf("expected an actionable refusal: %v\n%s", err, out)
				}
				after, err := os.ReadFile(srcPath)
				if err != nil || string(after) != string(before) {
					t.Fatalf("refused write-back changed canonical source: %v\n%s", err, after)
				}
				if _, err := source.Load(afero.NewOsFs(), home); err != nil {
					t.Fatalf("canonical source no longer loads: %v", err)
				}
				if tc.harness == "copilot" && (strings.HasPrefix(tc.source, "commands/") || strings.HasPrefix(tc.source, "subagents/")) {
					kind, name := "command", "verify"
					if strings.HasPrefix(tc.source, "subagents/") {
						kind, name = "subagent", "reviewer"
					}
					mustRun(t, env, "import", "copilot:"+kind+":"+name)
					captured, err := os.ReadFile(srcPath)
					if err != nil || !strings.Contains(string(captured), "Native edit.") || strings.Contains(string(captured), "agentsync:managed") || strings.Contains(string(captured), "agentsync:command") {
						t.Fatalf("import did not capture clean canonical text: %v\n%s", err, captured)
					}
					mustRun(t, env, "apply")
					if _, err := os.Stat(dest); err != nil {
						t.Fatalf("imported component disappeared on apply: %v", err)
					}
				}
			})
		}
	}
}
