package vscodelocal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
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
	if err != nil || len(scoped) != 0 || len(skips) != 1 {
		t.Fatal("unexpected project-scope write")
	}
}
