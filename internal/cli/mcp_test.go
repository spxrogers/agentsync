package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestMCP_AddStdio_WritesCanonicalTOML(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")

	out, err := runCLI(t, env, "mcp", "add", "github",
		"--command", "npx",
		"--args", "-y,@modelcontextprotocol/server-github")
	if err != nil {
		t.Fatalf("mcp add: %v\n%s", err, out)
	}

	p := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	var m source.MCPServer
	if err := toml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse written toml: %v\n%s", err, data)
	}
	if m.Server.Type != "stdio" {
		t.Errorf("type = %q, want stdio", m.Server.Type)
	}
	if m.Server.Command != "npx" {
		t.Errorf("command = %q, want npx", m.Server.Command)
	}
	want := []string{"-y", "@modelcontextprotocol/server-github"}
	if len(m.Server.Args) != len(want) {
		t.Fatalf("args len = %d, want %d (%v)", len(m.Server.Args), len(want), m.Server.Args)
	}
	for i, a := range want {
		if m.Server.Args[i] != a {
			t.Errorf("args[%d] = %q, want %q", i, m.Server.Args[i], a)
		}
	}
}

func TestMCP_AddRejectsDuplicates(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "mcp", "add", "github", "--command", "npx", "--args", "-y,server")
	out, err := runCLI(t, env, "mcp", "add", "github", "--command", "npx", "--args", "-y,server")
	if err == nil {
		t.Fatalf("second add should refuse; got:\n%s", out)
	}
}

func TestMCP_AddHTTPRequiresURL(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, err := runCLI(t, env, "mcp", "add", "x", "--type", "http")
	if err == nil {
		t.Fatal("http add without --url should fail")
	}
}

// TestMCP_AddRejectsEmptyAgents is the regression for an explicitly empty
// --agents silently becoming "all agents" (nil allowlist) instead of erroring.
func TestMCP_AddRejectsEmptyAgents(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, err := runCLI(t, env, "mcp", "add", "x", "--command", "npx", "--agents", "")
	if err == nil {
		t.Fatal("mcp add with explicitly empty --agents should be rejected")
	}
	if !strings.Contains(err.Error(), "--agents cannot be empty") {
		t.Fatalf("expected empty-agents error, got: %v", err)
	}
}

func TestMCP_ListAndRemove(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "mcp", "add", "github", "--command", "npx", "--args", "-y,server")
	_, _ = runCLI(t, env, "mcp", "add", "gitlab", "--command", "npx", "--args", "-y,server")

	out, err := runCLI(t, env, "mcp", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"github", "gitlab"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q\n%s", want, out)
		}
	}

	if _, err := runCLI(t, env, "mcp", "remove", "github"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "mcp", "github.toml")); !os.IsNotExist(err) {
		t.Fatalf("github.toml should be removed; stat err = %v", err)
	}
}

func TestMCP_IDValidation(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	bad := []string{"../escape", "a/b", "a\\b"}
	for _, id := range bad {
		_, err := runCLI(t, env, "mcp", "add", id, "--command", "x", "--args", "y")
		if err == nil {
			t.Errorf("id %q should be rejected", id)
		}
	}
}
