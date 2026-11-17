package cli_test

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

func TestImportCopilotHooksDoNotBlockOtherComponents(t *testing.T) {
	selectors := []struct {
		name     string
		selector string
		refuse   bool
	}{
		{name: "command", selector: "copilot:command:verify"},
		{name: "subagent", selector: "copilot:subagent:reviewer"},
		{name: "whole agent", selector: "copilot"},
		{name: "all hooks", selector: "copilot:hook", refuse: true},
		{name: "named hook", selector: "copilot:hook:preToolUse", refuse: true},
	}
	for _, projectScope := range []bool{false, true} {
		for _, dryRun := range []bool{false, true} {
			for _, tc := range selectors {
				scopeName, mode := "user", "capture"
				if projectScope {
					scopeName = "project"
				}
				if dryRun {
					mode = "dry run"
				}
				t.Run(scopeName+"/"+mode+"/"+tc.name, func(t *testing.T) {
					root := t.TempDir()
					base := root
					var scopeArgs []string
					if projectScope {
						base = filepath.Join(root, "project")
						scopeArgs = []string{"--scope", "project", "--project", base}
					}
					home := filepath.Join(base, ".agentsync")
					if err := source.WriteMemory(home, source.Memory{Body: "Global rules.\n"}); err != nil {
						t.Fatal(err)
					}
					if err := source.WriteCommand(home, source.Command{Name: "verify", Frontmatter: map[string]any{"description": "Verify changes"}, Body: "Run checks.\n"}); err != nil {
						t.Fatal(err)
					}
					if err := source.WriteSubagent(home, source.Subagent{Name: "reviewer", Frontmatter: map[string]any{"description": "Review changes"}, Body: "Review only.\n"}); err != nil {
						t.Fatal(err)
					}
					if err := source.WriteHooks(home, "preToolUse", []source.Hook{{Event: untrusted.Wrap("preToolUse"), Type: "command", Command: "review-hook"}}); err != nil {
						t.Fatal(err)
					}
					config := "[agents.copilot]\nenabled = true\n[copilot]\nhooks_enabled = true\n[destination_directory_git_backup]\nmode = 'off'\n"
					if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(config), 0o644); err != nil {
						t.Fatal(err)
					}
					env := map[string]string{"AGENTSYNC_TARGET_ROOT": root}
					mustRun(t, env, append([]string{"apply"}, scopeArgs...)...)
					nativeRoot := filepath.Join(root, ".copilot")
					commandDest := filepath.Join(nativeRoot, "skills", "verify", "SKILL.md")
					agentDest := filepath.Join(nativeRoot, "agents", "reviewer.agent.md")
					if projectScope {
						nativeRoot = filepath.Join(base, ".github")
						commandDest = filepath.Join(nativeRoot, "skills", "verify", "SKILL.md")
						agentDest = filepath.Join(nativeRoot, "agents", "reviewer.agent.md")
					}
					hookDest := filepath.Join(nativeRoot, "hooks", "agentsync.json")
					hookSource := filepath.Join(home, "hooks", "preToolUse.toml")
					readHookOwnership := func() map[state.Key]state.KeyEntry {
						t.Helper()
						st, err := state.Load(filepath.Join(root, ".agentsync", ".state", "targets.json"))
						if err != nil {
							t.Fatal(err)
						}
						owned := map[state.Key]state.KeyEntry{}
						for key, entry := range st.Keys {
							if key.Path == paths.HomeRelative(root, hookDest) {
								owned[key] = entry
							}
						}
						return owned
					}
					ownershipBefore := readHookOwnership()
					if len(ownershipBefore) == 0 {
						t.Fatal("apply did not record managed hook ownership")
					}
					hookBefore, err := os.ReadFile(hookDest)
					if err != nil {
						t.Fatal(err)
					}
					sourceBefore, err := os.ReadFile(hookSource)
					if err != nil {
						t.Fatal(err)
					}
					for _, path := range []string{commandDest, agentDest} {
						data, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, append(data, []byte("\nNative edit.\n")...), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					args := append([]string{"import", tc.selector}, scopeArgs...)
					if dryRun {
						args = append(args, "--dry-run")
					}
					out, err := runCLI(t, env, args...)
					if tc.refuse {
						if err == nil || !strings.Contains(err.Error(), "copilot hooks import/write-back is unsupported") {
							t.Fatalf("expected explicit hook import refusal: %v\n%s", err, out)
						}
					} else if err != nil || !strings.Contains(out, "hooks were not captured") {
						t.Fatalf("hook file blocked unrelated import or was silently skipped: %v\n%s", err, out)
					}
					for _, check := range []struct {
						path string
						want []byte
					}{{path: hookDest, want: hookBefore}, {path: hookSource, want: sourceBefore}} {
						got, err := os.ReadFile(check.path)
						if err != nil || string(got) != string(check.want) {
							t.Fatalf("import changed hook file %s: %v", check.path, err)
						}
					}
					if !maps.Equal(ownershipBefore, readHookOwnership()) {
						t.Fatal("import changed existing hook ownership")
					}
					for _, kind := range []string{"commands", "subagents"} {
						name, selected := "verify", tc.name == "command"
						if kind == "subagents" {
							name, selected = "reviewer", tc.name == "subagent"
						}
						selected = selected || tc.name == "whole agent"
						data, err := os.ReadFile(filepath.Join(home, kind, name+".md"))
						if err != nil {
							t.Fatal(err)
						}
						wantEdit := selected && !dryRun && !tc.refuse
						if strings.Contains(string(data), "Native edit.") != wantEdit || strings.Contains(string(data), "agentsync:managed") || strings.Contains(string(data), "agentsync:command") {
							t.Fatalf("unexpected canonical %s after import: %s", kind, data)
						}
					}
				})
			}
		}
	}
}
