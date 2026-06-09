package windsurf_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/windsurf"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestApply_MCP_PreservesForeignServers verifies merge-json-keys preserves a
// hand-authored mcp_config.json's foreign servers.
func TestApply_MCP_PreservesForeignServers(t *testing.T) {
	tmp := t.TempDir()
	mcpPath := filepath.Join(tmp, ".codeium", "windsurf", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{ "mcpServers": { "user-server": { "command": "mine" } } }`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := source.Canonical{MCPServers: []source.MCPServer{{ID: "ours", Server: source.MCPServerSpec{Command: "npx"}}}}
	a := windsurf.New(windsurf.Options{TargetRoot: tmp})
	ops, _, _ := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mcpPath)
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	servers := got["mcpServers"].(map[string]any)
	if _, ok := servers["user-server"]; !ok {
		t.Fatalf("foreign server clobbered: %v", servers)
	}
	if _, ok := servers["ours"]; !ok {
		t.Fatalf("our server not added: %v", servers)
	}
}

// TestApply_ProjectScope_WritesRulesAndWorkflows verifies the project-scope
// markdown writes land and a user's foreign rule file is untouched.
func TestApply_ProjectScope_WritesRulesAndWorkflows(t *testing.T) {
	proj := t.TempDir()
	foreignRule := filepath.Join(proj, ".windsurf", "rules", "user.md")
	if err := os.MkdirAll(filepath.Dir(foreignRule), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignRule, []byte("user rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projC := source.Canonical{
		Memory:   source.Memory{Body: "mem\n"},
		Commands: []source.Command{{Name: "wf", Body: "step 1\n"}},
	}
	c := projC
	c.Project = &projC
	a := windsurf.New(windsurf.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		filepath.Join(proj, ".windsurf", "rules", "agentsync.md"),
		filepath.Join(proj, ".windsurf", "workflows", "wf.md"),
		foreignRule,
	} {
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("expected %s to exist: %v", want, err)
		}
	}
}
