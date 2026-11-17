package vscodelocal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/generic"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/testenv"
)

func TestMain(m *testing.M) { testenv.MustRunInContainer(); os.Exit(m.Run()) }

func TestProfileSelection(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"Code/User/prompts", "Code/User/profiles/selected/prompts", "custom-user-data/User/prompts", "portable/data/user-data/User/prompts", "shared-default-prompts"} {
		home := filepath.Join(root, filepath.FromSlash(rel))
		target, err := ResolveTarget(home, "")
		if err != nil || target != filepath.Join(home, FileName) {
			t.Fatalf("explicit profile %s: %s, %v", rel, target, err)
		}
		redirected, err := ResolveTarget(home, filepath.Join(root, "isolated"))
		if err != nil || redirected != filepath.Join(root, "isolated", ".vscode-local", "prompts", FileName) {
			t.Fatalf("redirect escapes sandbox: %s, %v", redirected, err)
		}
	}
	for _, bad := range []string{"", "relative/prompts"} {
		if _, err := ResolveTarget(bad, ""); err == nil {
			t.Fatalf("accepted ambiguous profile %q", bad)
		}
	}
}

func TestEnvironmentProfileSelection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTSYNC_TEST_PROFILE", root)
	for _, value := range []string{"${env:AGENTSYNC_TEST_PROFILE}/prompts", "${AGENTSYNC_TEST_PROFILE}/prompts", "$AGENTSYNC_TEST_PROFILE/prompts"} {
		target, err := ResolveTarget(value, "")
		if err != nil || target != filepath.Join(root, "prompts", FileName) {
			t.Fatalf("resolve %q: %q, %v", value, target, err)
		}
		redirect := filepath.Join(root, "sandbox")
		target, err = ResolveTarget(value, redirect)
		if err != nil || target != filepath.Join(redirect, ".vscode-local", "prompts", FileName) {
			t.Fatalf("redirect %q: %q, %v", value, target, err)
		}
	}
	t.Setenv("AGENTSYNC_TEST_PROFILE", "")
	if _, err := ResolveTarget("${env:AGENTSYNC_TEST_PROFILE}/prompts", ""); err == nil {
		t.Fatal("accepted empty environment variable")
	}
	t.Setenv("AGENTSYNC_TEST_PROFILE", "relative")
	if _, err := ResolveTarget("${env:AGENTSYNC_TEST_PROFILE}/prompts", ""); err == nil {
		t.Fatal("accepted relative expanded path")
	}
}

func TestInstructionsPreservationAndIdempotence(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "User", "prompts")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, "personal.instructions.md")
	personal := []byte("---\napplyTo: '**'\n---\nKeep my own instructions.\n")
	if err := os.WriteFile(other, personal, 0o644); err != nil {
		t.Fatal(err)
	}
	c := source.Canonical{Memory: source.Memory{Body: "# Current edited rules\nNew user content.\n"}}
	c.Config.VSCodeLocal.PromptsHome = home
	a := New(Options{})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil || len(ops) != 1 {
		t.Fatalf("render: %v %+v", err, ops)
	}
	if !strings.HasPrefix(string(ops[0].Content), Frontmatter) || !strings.Contains(string(ops[0].Content), c.Memory.Body) {
		t.Fatal("frontmatter/body missing")
	}
	if strings.Count(string(ops[0].Content), "applyTo:") != 1 {
		t.Fatal("duplicate frontmatter")
	}
	st := state.New()
	canonical := filepath.Join(root, ".agentsync")
	w := render.NewWriter(st, canonical, root, adapter.ScopeUser, "", a.Name())
	if err := a.Apply(ops, w); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(ops[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	repeat := render.NewWriter(st, canonical, root, adapter.ScopeUser, "", a.Name())
	if err := a.Apply(ops, repeat); err != nil {
		t.Fatal(err)
	}
	if !repeat.Unchanged()[ops[0].Path] {
		t.Fatal("second apply rewrote equivalent Windows permissions")
	}
	preview := render.NewPreviewWriter(st, canonical, root, adapter.ScopeUser, "", a.Name())
	if err := a.Apply(ops, preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.WouldChange()) != 0 {
		t.Fatal("repeat preview planned a write")
	}
	after, err := os.Stat(ops[0].Path)
	if err != nil || !first.ModTime().Equal(after.ModTime()) {
		t.Fatal("mtime changed")
	}
	got, err := os.ReadFile(other)
	if err != nil || string(got) != string(personal) {
		t.Fatal("unrelated instructions modified")
	}
	if err := os.WriteFile(ops[0].Path, []byte("User-owned collision\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collision := render.NewWriter(st, canonical, root, adapter.ScopeUser, "", a.Name())
	if err := a.Apply(ops, collision); err == nil {
		t.Fatal("overwrote unmanaged same-name file")
	}
	got, err = os.ReadFile(ops[0].Path)
	if err != nil || string(got) != "User-owned collision\n" {
		t.Fatal("collision content lost")
	}
	if _, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, ""); err != adapter.ErrProjectRootRequired {
		t.Fatal("missing project guard")
	}
	scoped, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, root)
	if err != nil || len(scoped) != 1 || len(skips) != 0 || scoped[0].Path != filepath.Join(root, ".github", "instructions", FileName) {
		t.Fatal("unexpected project-scope write")
	}
}

func TestLocalHarnessResources(t *testing.T) {
	root := t.TempDir()
	c := source.Canonical{
		Subagents:  []source.Subagent{{Name: "reviewer", Frontmatter: map[string]any{"description": "Review changes", "tools": []string{"read"}}, Body: "Review only."}},
		Commands:   []source.Command{{Name: "verify", Body: "Run checks."}},
		Skills:     []source.Skill{{Name: "inspect", Body: "Inspect changes.", Files: []source.SkillFile{{Path: "references/check.txt", Content: []byte("Preserve checks.\n"), Mode: 0o644}}}},
		MCPServers: []source.MCPServer{{ID: "example", Server: source.MCPServerSpec{Command: "example-server"}}},
	}
	c.Config.VSCodeLocal.PromptsHome = filepath.Join(root, "profile", "prompts")
	ops, skips, err := New(Options{RedirectRoot: root}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil || len(skips) != 0 || len(ops) != 5 {
		t.Fatalf("render: %v %+v %+v", err, ops, skips)
	}
	for _, op := range ops {
		if !strings.HasPrefix(op.Path, root+string(filepath.Separator)) {
			t.Fatalf("escaped sandbox: %s", op.Path)
		}
	}
	if !strings.HasSuffix(ops[0].Path, "reviewer.agent.md") || !strings.HasSuffix(ops[1].Path, "verify.prompt.md") || !strings.HasSuffix(ops[4].Path, "mcp.json") {
		t.Fatalf("wrong resource locations: %+v", ops)
	}
	if err := New(Options{RedirectRoot: root}).Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".copilot", "skills", "inspect", "references", "check.txt"))
	if err != nil || string(data) != "Preserve checks.\n" {
		t.Fatalf("bundled skill file was lost: %q %v", data, err)
	}
}

func TestSharedProjectAgentIsIdentical(t *testing.T) {
	root := t.TempDir()
	c := source.Canonical{Subagents: []source.Subagent{{Name: "reviewer", Frontmatter: map[string]any{"description": "Review", "tools": []string{"read"}}, Body: "Review only.\n"}}}
	localOps, _, err := New(Options{}).Render(secrets.ForRender(c), adapter.ScopeProject, root)
	if err != nil {
		t.Fatal(err)
	}
	copilotOps, _, err := generic.RenderVSCodeResources(c, "copilot", root, "", adapter.ScopeProject, root)
	if err != nil || len(localOps) != 1 || len(copilotOps) != 1 || localOps[0].Path != copilotOps[0].Path || string(localOps[0].Content) != string(copilotOps[0].Content) {
		t.Fatalf("shared project agent conflicts: %v %+v %+v", err, localOps, copilotOps)
	}
}

func TestProfileMCPOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTSYNC_TEST_PROFILE", root)
	config := source.VSCodeLocalConfig{PromptsHome: "${env:AGENTSYNC_TEST_PROFILE}/default/prompts", MCPFile: "${env:AGENTSYNC_TEST_PROFILE}/named/mcp.json"}
	path, err := config.ResolveMCPFile()
	if err != nil || path != filepath.Join(root, "named", "mcp.json") {
		t.Fatalf("profile MCP override: %q %v", path, err)
	}
	c := source.Canonical{MCPServers: []source.MCPServer{{ID: "example", Server: source.MCPServerSpec{Command: "example-server"}}}}
	c.Config.VSCodeLocal = config
	ops, _, err := New(Options{RedirectRoot: root}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil || len(ops) != 1 || ops[0].Path != filepath.Join(root, ".vscode-local", "mcp.json") {
		t.Fatalf("MCP override escaped sandbox: %v %+v", err, ops)
	}
}
