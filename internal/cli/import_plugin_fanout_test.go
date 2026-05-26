package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeSkillMarketplace builds a local marketplace whose single plugin ships both
// a skill (convention-discovered from skills/<name>/SKILL.md) and an MCP server,
// returning the marketplace root. It exists to prove a Claude marketplace
// projects its components out to OTHER agents on apply.
func makeSkillMarketplace(t *testing.T, dir string) string {
	t.Helper()
	mpDir := filepath.Join(dir, "skill-marketplace")

	write := func(rel, body string) {
		p := filepath.Join(mpDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(".claude-plugin/marketplace.json", `{
		"name": "toolkit-mp",
		"owner": {"name": "tester"},
		"plugins": [{"name": "toolkit", "source": "./plugins/toolkit"}]
	}`)
	write("plugins/toolkit/.claude-plugin/plugin.json", `{
		"name": "toolkit",
		"version": "1.0.0",
		"mcpServers": {"tk-mcp": {"command": "tk-server"}}
	}`)
	// Convention-discovered skill (plugin.json lists none).
	write("plugins/toolkit/skills/greeter/SKILL.md",
		"---\nname: greeter\ndescription: say hi\n---\nGreet the user.\n")
	return mpDir
}

// TestImport_PluginComponentsFanOutToOpenCode is the end-to-end guarantee behind
// "a marketplace is just a bag of entities fanned out via supported mechanisms":
// importing a Claude marketplace plugin, then `apply` with OpenCode also enabled,
// projects the plugin's skill and MCP server out to OpenCode — the skill to the
// shared .claude/skills path OpenCode reads, the MCP into opencode.json.
func TestImport_PluginComponentsFanOutToOpenCode(t *testing.T) {
	tmp, env := importTestEnv(t) // inits + enables claude
	if _, err := runCLI(t, env, "agent", "add", "opencode"); err != nil {
		t.Fatalf("agent add opencode: %v", err)
	}
	mpDir := makeSkillMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("toolkit-mp", mpDir, "toolkit"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	// The skill landed at the shared .claude/skills path OpenCode reads natively.
	skill := filepath.Join(tmp, ".claude", "skills", "greeter", "SKILL.md")
	data, err := os.ReadFile(skill)
	if err != nil {
		t.Fatalf("plugin skill did not project to the shared skills path: %v", err)
	}
	if !strings.Contains(string(data), "Greet the user") {
		t.Fatalf("projected skill missing body; got:\n%s", data)
	}

	// The plugin's MCP server fanned out into OpenCode's own config — a genuinely
	// OpenCode-specific render target, proving cross-agent fan-out (not just a
	// shared path).
	ocConfig := filepath.Join(tmp, ".config", "opencode", "opencode.json")
	oc, err := os.ReadFile(ocConfig)
	if err != nil {
		t.Fatalf("opencode.json not written: %v", err)
	}
	if !strings.Contains(string(oc), "tk-mcp") {
		t.Fatalf("plugin MCP server did not fan out to opencode.json; got:\n%s", oc)
	}
}
