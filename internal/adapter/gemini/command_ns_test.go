package gemini_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestGeminiNamespacedCommandRoundTrip anchors to a spec-complete on-disk command
// tree: a namespaced `commands/git/commit.toml` (Gemini `/git:commit`) alongside a
// flat `commands/deploy.toml`. Ingest must capture the namespace into
// Command.Name as a forward-slash relpath ("git/commit"), and Render must write it
// back to the SAME subdirectory path, byte-identically, with the flat command
// coexisting.
func TestGeminiNamespacedCommandRoundTrip(t *testing.T) {
	root := t.TempDir()
	commandsDir := filepath.Join(root, ".gemini", "commands")
	if err := os.MkdirAll(filepath.Join(commandsDir, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nsFixture := "description = \"Craft a commit\"\nprompt = \"Write a commit message for {{args}}.\\n\"\n"
	flatFixture := "description = \"Deploy\"\nprompt = \"Ship it.\\n\"\n"
	if err := os.WriteFile(filepath.Join(commandsDir, "git", "commit.toml"), []byte(nsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "deploy.toml"), []byte(flatFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	a := gemini.New(gemini.Options{TargetRoot: root})
	c1, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	byName := map[string]source.Command{}
	for _, cmd := range c1.Commands {
		byName[cmd.Name] = cmd
	}
	ns, ok := byName["git/commit"]
	if !ok {
		t.Fatalf("namespaced command should ingest as Name=git/commit; got names %v", commandNames(c1))
	}
	if ns.Body != "Write a commit message for {{args}}.\n" {
		t.Fatalf("namespaced prompt lost: %q", ns.Body)
	}
	if ns.Frontmatter["description"] != "Craft a commit" {
		t.Fatalf("namespaced description lost: %+v", ns.Frontmatter)
	}
	if _, ok := byName["deploy"]; !ok {
		t.Fatalf("flat command must coexist; got names %v", commandNames(c1))
	}

	// Render: the namespaced command must project into the subdirectory layout.
	ops1, _, err := a.Render(secrets.ForRender(c1), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	nsOp1 := findOp(ops1, ".gemini/commands/git/commit.toml")
	if nsOp1 == nil {
		t.Fatalf("namespaced command must render to commands/git/commit.toml; ops=%v", opPaths(ops1))
	}
	if findOp(ops1, ".gemini/commands/deploy.toml") == nil {
		t.Fatalf("flat command must render to commands/deploy.toml; ops=%v", opPaths(ops1))
	}
	var cf struct {
		Description string `toml:"description"`
		Prompt      string `toml:"prompt"`
	}
	if err := toml.Unmarshal(nsOp1.Content, &cf); err != nil {
		t.Fatalf("namespaced command not valid TOML: %v\n%s", err, nsOp1.Content)
	}
	if cf.Description != "Craft a commit" || cf.Prompt != "Write a commit message for {{args}}.\n" {
		t.Fatalf("namespaced TOML wrong: %+v", cf)
	}

	// Apply to disk (creates the git/ subdir), re-ingest, re-render, and assert the
	// on-disk namespaced path exists and the render is byte-stable (round-trip).
	renderApply(t, a, c1, adapter.ScopeUser, "")
	if _, err := os.Stat(filepath.Join(commandsDir, "git", "commit.toml")); err != nil {
		t.Fatalf("namespaced command must land on disk at commands/git/commit.toml: %v", err)
	}
	c2, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	ops2, _, err := a.Render(secrets.ForRender(c2), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("re-render: %v", err)
	}
	nsOp2 := findOp(ops2, ".gemini/commands/git/commit.toml")
	if nsOp2 == nil {
		t.Fatalf("namespaced command lost on re-render; ops=%v", opPaths(ops2))
	}
	if !bytes.Equal(nsOp1.Content, nsOp2.Content) {
		t.Fatalf("namespaced command not byte-identical across round-trip:\n first: %s\nsecond: %s", nsOp1.Content, nsOp2.Content)
	}
}

func commandNames(c source.Canonical) []string {
	out := make([]string, 0, len(c.Commands))
	for _, cmd := range c.Commands {
		out = append(out, cmd.Name)
	}
	return out
}

func opPaths(ops []adapter.FileOp) []string {
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		out = append(out, op.Path)
	}
	return out
}
