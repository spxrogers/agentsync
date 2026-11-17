package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

func TestVSCodeConditionalInstructionsLifecycle(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		dir     string
		project bool
	}{
		{name: "copilot user", config: "[agents.copilot]\nenabled = true\n", dir: ".copilot/instructions"},
		{name: "local user", config: "[agents.vscode-local]\nenabled = true\n", dir: ".vscode-local/prompts"},
		{name: "shared project", config: "[agents.copilot]\nenabled = true\n[agents.vscode-local]\nenabled = true\n", dir: ".github/instructions", project: true},
	}
	for _, tc := range tests {
		for _, edited := range []bool{false, true} {
			name := tc.name + "/unchanged"
			if edited {
				name = tc.name + "/native edit"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				base := root
				var scopeArgs []string
				if tc.project {
					base = filepath.Join(root, "project")
					scopeArgs = []string{"--scope", "project", "--project", base}
				}
				home := filepath.Join(base, ".agentsync")
				mem := source.Memory{Body: "Global rules.\n", Fragments: map[string]string{"nested/old.instructions.md": "---\napplyTo: '**/*.py'\n---\nUse pytest.\n"}}
				if err := source.WriteMemory(home, mem); err != nil {
					t.Fatal(err)
				}
				config := tc.config + "[destination_directory_git_backup]\nmode = 'off'\n"
				if !tc.project && tc.name == "local user" {
					config += "[vscode_local]\nprompts_home = '" + filepath.Join(root, "profile", "prompts") + "'\n"
				}
				if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(config), 0o644); err != nil {
					t.Fatal(err)
				}
				env := map[string]string{"AGENTSYNC_TARGET_ROOT": root}
				apply := append([]string{"apply"}, scopeArgs...)
				mustRun(t, env, apply...)
				dir := filepath.Join(base, filepath.FromSlash(tc.dir), "nested")
				oldDest := filepath.Join(dir, "old.instructions.md")
				foreign := filepath.Join(dir, "personal.instructions.md")
				const personal = "---\napplyTo: '**'\n---\nPersonal rules.\n"
				if err := os.WriteFile(foreign, []byte(personal), 0o644); err != nil {
					t.Fatal(err)
				}
				oldContent, err := os.ReadFile(oldDest)
				if err != nil {
					t.Fatal(err)
				}
				if edited {
					oldContent = append(oldContent, []byte("Native edit to preserve.\n")...)
					if err := os.WriteFile(oldDest, oldContent, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				oldSource := filepath.Join(home, "memory", "fragments", "nested", "old.instructions.md")
				newSource := filepath.Join(filepath.Dir(oldSource), "new.instructions.md")
				if err := os.Rename(oldSource, newSource); err != nil {
					t.Fatal(err)
				}
				mustRun(t, env, apply...)
				if _, err := os.Stat(oldDest); !os.IsNotExist(err) {
					t.Fatalf("renamed instruction remains active: %v", err)
				}
				newDest := filepath.Join(dir, "new.instructions.md")
				if _, err := os.Stat(newDest); err != nil {
					t.Fatalf("renamed instruction not deployed: %v", err)
				}
				if edited {
					found := false
					err := filepath.Walk(filepath.Join(root, ".agentsync", ".state", "backups"), func(path string, info os.FileInfo, err error) error {
						if err != nil {
							return err
						}
						if info.IsDir() || filepath.Base(path) != "old.instructions.md" {
							return nil
						}
						data, err := os.ReadFile(path)
						if err != nil {
							return err
						}
						if string(data) == string(oldContent) {
							found = true
						}
						return nil
					})
					if err != nil || !found {
						t.Fatalf("native edit was not backed up before retirement: %v", err)
					}
				}
				if err := os.Remove(newSource); err != nil {
					t.Fatal(err)
				}
				mustRun(t, env, apply...)
				if _, err := os.Stat(newDest); !os.IsNotExist(err) {
					t.Fatalf("deleted instruction remains active: %v", err)
				}
				if data, err := os.ReadFile(foreign); err != nil || string(data) != personal {
					t.Fatalf("unmanaged sibling was changed: %v", err)
				}
				status := append([]string{"status", "--exit-code"}, scopeArgs...)
				mustRun(t, env, status...)
			})
		}
	}
}

func TestVSCodeConditionalInstructionsKeepUnselectedOwner(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	home := filepath.Join(project, ".agentsync")
	mem := source.Memory{Fragments: map[string]string{"shared.instructions.md": "---\napplyTo: '**'\n---\nShared rules.\n"}}
	if err := source.WriteMemory(home, mem); err != nil {
		t.Fatal(err)
	}
	config := "[agents.copilot]\nenabled = true\n[agents.vscode-local]\nenabled = true\n"
	if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": root}
	mustRun(t, env, "apply", "--scope", "project", "--project", project)
	dest := filepath.Join(project, ".github", "instructions", "shared.instructions.md")
	if err := os.Remove(filepath.Join(home, "memory", "fragments", "shared.instructions.md")); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply", "--agents", "vscode-local", "--scope", "project", "--project", project)
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("instruction still owned by Copilot was removed: %v", err)
	}
	mustRun(t, env, "status", "--exit-code", "--agents", "vscode-local", "--scope", "project", "--project", project)
	mustRun(t, env, "apply", "--agents", "copilot", "--scope", "project", "--project", project)
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("last owner's instruction was not retired: %v", err)
	}
	mustRun(t, env, "status", "--exit-code", "--scope", "project", "--project", project)
}

func TestVSCodeSharedProjectDelegationAgent(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	home := filepath.Join(project, ".agentsync")
	if err := source.WriteSubagent(home, source.Subagent{Name: "coordinator", Frontmatter: map[string]any{"description": "Delegate review", "tools": []string{"read", "agent/runSubagent"}}, Body: "Delegate the review.\n"}); err != nil {
		t.Fatal(err)
	}
	config := "[agents.copilot]\nenabled = true\n[agents.vscode-local]\nenabled = true\n"
	if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": root}
	mustRun(t, env, "apply", "--scope", "project", "--project", project)
	dest := filepath.Join(project, ".github", "agents", "coordinator.agent.md")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	fm, body, err := source.ParseFrontmatter(data)
	if err != nil || !strings.Contains(body, "Delegate the review.") {
		t.Fatalf("shared agent did not retain its body: %v\n%s", err, data)
	}
	tools, ok := fm["tools"].([]any)
	if !ok || len(tools) != 2 || tools[0] != "read" || tools[1] != "agent" {
		t.Fatalf("shared agent did not use the portable delegation alias: %+v", fm)
	}
	mustRun(t, env, "apply", "--scope", "project", "--project", project)
	mustRun(t, env, "status", "--exit-code", "--scope", "project", "--project", project)
}
