package continuedev_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/continuedev"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestApply_WritesBlockFiles verifies apply commits each component to its own
// block file and leaves a user's foreign block files in the same dirs untouched.
func TestApply_WritesBlockFiles(t *testing.T) {
	tmp := t.TempDir()
	// Pre-existing foreign blocks the user authored.
	mustWrite(t, filepath.Join(tmp, ".continue", "mcpServers", "user-server.yaml"), "name: user-server\n")
	mustWrite(t, filepath.Join(tmp, ".continue", "rules", "user-rule.md"), "be nice\n")

	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Command: "npx"}}},
		Memory:     source.Memory{Body: "agentsync memory\n"},
		Commands:   []source.Command{{Name: "deploy", Body: "deploy it\n"}},
	}
	a := continuedev.New(continuedev.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}

	// Our blocks landed.
	assertExists(t, filepath.Join(tmp, ".continue", "mcpServers", "github.yaml"))
	assertExists(t, filepath.Join(tmp, ".continue", "rules", "agentsync.md"))
	assertExists(t, filepath.Join(tmp, ".continue", "prompts", "deploy.md"))
	// Foreign blocks survive (separate files; agentsync never touches them).
	assertExists(t, filepath.Join(tmp, ".continue", "mcpServers", "user-server.yaml"))
	assertExists(t, filepath.Join(tmp, ".continue", "rules", "user-rule.md"))
}

// TestApply_Reapply_Converges verifies a second apply is byte-identical.
func TestApply_Reapply_Converges(t *testing.T) {
	tmp := t.TempDir()
	c := source.Canonical{MCPServers: []source.MCPServer{{ID: "x", Server: source.MCPServerSpec{Type: "stdio", Command: "y", Args: []string{"-a"}}}}}
	a := continuedev.New(continuedev.Options{TargetRoot: tmp})
	apply := func() []byte {
		ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
		if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(tmp, ".continue", "mcpServers", "x.yaml"))
		return b
	}
	first := apply()
	second := apply()
	if string(first) != string(second) {
		t.Fatalf("re-apply not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}
