package marketplace_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/marketplace"
)

func TestProject_StrictPluginJSON(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(cache, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"x","mcpServers":{"foo":{"command":"${CLAUDE_PLUGIN_ROOT}/run.sh"}}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "x"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.MCPServers) != 1 {
		t.Fatalf("mcp = %d, want 1", len(pr.MCPServers))
	}
	cmd := pr.MCPServers[0].Server.Command
	if !strings.HasPrefix(cmd, cache) {
		t.Fatalf("CLAUDE_PLUGIN_ROOT not resolved: %s", cmd)
	}
	if pr.MCPServers[0].ID != "foo" {
		t.Errorf("mcp id = %q, want foo", pr.MCPServers[0].ID)
	}
}

// TestProject_RejectsEscapingComponentPath is the regression for the
// manifest-path traversal: the fetchers are hardened, but a hostile
// plugin.json could list a skill/command/agent path that resolves outside
// the plugin cache, exfiltrating a host file into a projected component.
func TestProject_RejectsEscapingComponentPath(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(secret, []byte("---\nname: leak\n---\nhost secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, mal := range []string{secret, "../../../../etc/passwd", "../escape/SKILL.md"} {
		t.Run(mal, func(t *testing.T) {
			cache := t.TempDir()
			if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
				t.Fatal(err)
			}
			manifest := `{"name":"x","skills":["` + mal + `"]}`
			if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := marketplace.Project(marketplace.PluginEntry{Name: "x"}, cache)
			if err == nil {
				t.Fatalf("expected escape error for skills path %q", mal)
			}
			if !strings.Contains(err.Error(), "escapes plugin cache") {
				t.Fatalf("error should explain the escape; got: %v", err)
			}
		})
	}
}

func TestProject_StrictPluginJSON_MultipleComponents(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"name": "multi",
		"mcpServers": {
			"srv-a": {"type": "stdio", "command": "${CLAUDE_PLUGIN_ROOT}/a"},
			"srv-b": {"type": "http", "url": "http://localhost:9000"}
		},
		"lspServers": {
			"lsp-x": {"command": "${CLAUDE_PLUGIN_ROOT}/lsp"}
		},
		"skills": ["skill-one", "skill-two"],
		"commands": "my-cmd.md",
		"agents": ["agent-alpha.md"]
	}`
	if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create the skill directories and SKILL.md files so projection finds them.
	for _, sk := range []string{"skill-one", "skill-two"} {
		if err := os.MkdirAll(filepath.Join(cache, sk), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + sk + "\n---\nSkill body.\n"
		if err := os.WriteFile(filepath.Join(cache, sk, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Create the command and agent markdown files.
	if err := os.WriteFile(filepath.Join(cache, "my-cmd.md"), []byte("---\nname: my-cmd\n---\nDo things.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "agent-alpha.md"), []byte("---\nname: agent-alpha\n---\nAgent body.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "multi"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.MCPServers) != 2 {
		t.Errorf("mcp count = %d, want 2", len(pr.MCPServers))
	}
	if len(pr.LSPServers) != 1 {
		t.Errorf("lsp count = %d, want 1", len(pr.LSPServers))
	}
	if len(pr.Skills) != 2 {
		t.Errorf("skills count = %d, want 2", len(pr.Skills))
	}
	if len(pr.Commands) != 1 {
		t.Errorf("commands count = %d, want 1", len(pr.Commands))
	}
	if len(pr.Subagents) != 1 {
		t.Errorf("agents count = %d, want 1", len(pr.Subagents))
	}

	// Verify CLAUDE_PLUGIN_ROOT substitution in LSP.
	lspCmd := pr.LSPServers[0].Spec.Command
	if !strings.HasPrefix(lspCmd, cache) {
		t.Errorf("LSP command not resolved: %s", lspCmd)
	}
}

// TestProject_SkillBundledFiles proves a plugin-bundled skill is projected as a
// DIRECTORY: scripts/, references/, and nested files come along (with the
// script's executable bit preserved), not just SKILL.md. This is the plugin/
// apply-path guard against the "only SKILL.md survives" lossiness bug.
func TestProject_SkillBundledFiles(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"x","skills":["pdf"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(cache, "pdf")
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: pdf\ndescription: pdfs\n---\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "scripts", "extract.py"), []byte("print('hi')\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "references", "REF.md"), []byte("# ref\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "x"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Skills) != 1 {
		t.Fatalf("skills = %d, want 1", len(pr.Skills))
	}
	files := pr.Skills[0].Files
	if len(files) != 2 {
		t.Fatalf("bundled files = %d, want 2: %+v", len(files), files)
	}
	byPath := map[string]uint32{}
	for _, f := range files {
		byPath[f.Path] = f.Mode
	}
	if _, ok := byPath["references/REF.md"]; !ok {
		t.Fatalf("references/REF.md not projected: %+v", files)
	}
	if mode, ok := byPath["scripts/extract.py"]; !ok {
		t.Fatalf("scripts/extract.py not projected: %+v", files)
	} else if mode&0o100 == 0 {
		t.Fatalf("scripts/extract.py lost +x: %o", mode)
	}
}

// TestProject_NestedSkillDiscovery is the regression for the notion plugin: its
// skills are grouped a level deeper (skills/notion/<category>/SKILL.md). The old
// one-level convention scan returned the grouping dir (skills/notion), which has
// no SKILL.md, and loadSkillEntry then read a directory as a file and hard-failed
// the whole projection ("is a directory") — bricking apply/status/diff for any
// installed plugin shaped this way. Discovery must recurse to the leaf skills.
func TestProject_NestedSkillDiscovery(t *testing.T) {
	cache := t.TempDir()
	// No plugin.json skills field → convention discovery runs.
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"notion"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	leaves := []string{"knowledge-capture", "meeting-intelligence"}
	for _, leaf := range leaves {
		d := filepath.Join(cache, "skills", "notion", leaf)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + leaf + "\ndescription: d\n---\nBody.\n"
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "notion"}, cache)
	if err != nil {
		t.Fatalf("Project on nested skills should not fail: %v", err)
	}
	if len(pr.Skills) != 2 {
		t.Fatalf("skills = %d, want 2: %+v", len(pr.Skills), pr.Skills)
	}
	got := map[string]bool{}
	for _, s := range pr.Skills {
		got[s.Name] = true
	}
	for _, leaf := range leaves {
		if !got[leaf] {
			t.Errorf("nested skill %q not discovered: %v", leaf, got)
		}
	}
}

// TestProject_GroupingDirNoSkillMD verifies a skills/ subtree that contains no
// SKILL.md at all is simply skipped (no skills, no error) rather than crashing.
func TestProject_GroupingDirNoSkillMD(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"empty"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A grouping dir with only a stray non-SKILL file deeper down.
	d := filepath.Join(cache, "skills", "group", "sub")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "README.md"), []byte("# nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "empty"}, cache)
	if err != nil {
		t.Fatalf("Project should skip a SKILL.md-less subtree, not fail: %v", err)
	}
	if len(pr.Skills) != 0 {
		t.Fatalf("skills = %d, want 0: %+v", len(pr.Skills), pr.Skills)
	}
}

func TestProject_NonStrict_EntryComponents(t *testing.T) {
	cache := t.TempDir()
	f := false
	entry := marketplace.PluginEntry{
		Name:   "ns",
		Strict: &f,
		MCPServers: map[string]any{
			"inline-srv": map[string]any{
				"command": "${CLAUDE_PLUGIN_ROOT}/inline",
				"args":    []any{"--port", "8080"},
			},
		},
		LSPServers: map[string]any{
			"inline-lsp": map[string]any{
				"command": "${CLAUDE_PLUGIN_ROOT}/lsp-inline",
			},
		},
	}

	pr, err := marketplace.Project(entry, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.MCPServers) != 1 {
		t.Fatalf("mcp count = %d, want 1", len(pr.MCPServers))
	}
	cmd := pr.MCPServers[0].Server.Command
	if !strings.HasPrefix(cmd, cache) {
		t.Errorf("CLAUDE_PLUGIN_ROOT not resolved in non-strict entry: %s", cmd)
	}
	if len(pr.MCPServers[0].Server.Args) != 2 {
		t.Errorf("args count = %d, want 2", len(pr.MCPServers[0].Server.Args))
	}
	if len(pr.LSPServers) != 1 {
		t.Errorf("lsp count = %d, want 1", len(pr.LSPServers))
	}
}

// TestProject_NonStrict_UnionsPluginJSON is the regression for non-strict mode
// dropping the plugin's own plugin.json components. Non-strict must mean
// "plugin.json PLUS entry additions", not "entry replaces plugin.json", so a
// non-strict entry never silently loses the plugin's declared components — and
// an upstream strict:true→false flip can't drop them.
func TestProject_NonStrict_UnionsPluginJSON(t *testing.T) {
	cache := t.TempDir()
	d := filepath.Join(cache, ".claude-plugin")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "plugin.json"),
		[]byte(`{"name":"ns","mcpServers":{"base-srv":{"command":"b"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := false
	entry := marketplace.PluginEntry{
		Name:       "ns",
		Strict:     &f,
		MCPServers: map[string]any{"extra-srv": map[string]any{"command": "e"}},
	}
	pr, err := marketplace.Project(entry, cache)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, m := range pr.MCPServers {
		ids[m.ID] = true
	}
	if !ids["base-srv"] {
		t.Fatalf("non-strict dropped the plugin.json component base-srv: %v", ids)
	}
	if !ids["extra-srv"] {
		t.Fatalf("non-strict entry override extra-srv missing: %v", ids)
	}
}

// Under NON-strict mode, a same-named skill declared by both plugin.json and
// the entry collapses to ONE entry with the entry's body winning — otherwise
// two canonical Skills with the same Name render to the same dest path and the
// cross-agent divergence guard aborts the whole apply.
func TestProject_NonStrictConflict_Skill_EntryWins(t *testing.T) {
	cacheDir := "/cache"
	files := map[string][]byte{
		filepath.Join(cacheDir, ".claude-plugin", "plugin.json"):  []byte(`{"name":"p","skills":["./skills/base"]}`),
		filepath.Join(cacheDir, "skills", "base", "SKILL.md"):     []byte("---\nname: shared\n---\nFROM PLUGIN_JSON\n"),
		filepath.Join(cacheDir, "skills", "override", "SKILL.md"): []byte("---\nname: shared\n---\nFROM ENTRY\n"),
	}
	f := false
	entry := marketplace.PluginEntry{Name: "p", Strict: &f, Skills: "./skills/override"}
	pr, err := marketplace.ProjectWithReader(entry, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	var body string
	for _, s := range pr.Skills {
		if s.Name == "shared" {
			n++
			body = s.Body
		}
	}
	if n != 1 {
		t.Fatalf("skill \"shared\" count = %d, want 1 (non-strict must dedup same-name)", n)
	}
	if !strings.Contains(body, "FROM ENTRY") {
		t.Errorf("entry override did not win; body = %q", body)
	}
}

// Under NON-strict mode, a same-ID MCP server declared by both plugin.json and
// the entry collapses to one, entry winning.
func TestProject_NonStrictConflict_MCP_EntryWins(t *testing.T) {
	cacheDir := "/cache"
	files := map[string][]byte{
		filepath.Join(cacheDir, ".claude-plugin", "plugin.json"): []byte(`{"name":"p","mcpServers":{"srv":{"command":"base"}}}`),
	}
	f := false
	entry := marketplace.PluginEntry{Name: "p", Strict: &f, MCPServers: map[string]any{"srv": map[string]any{"command": "entry"}}}
	pr, err := marketplace.ProjectWithReader(entry, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.MCPServers) != 1 {
		t.Fatalf("mcp count = %d, want 1 (dedup same id)", len(pr.MCPServers))
	}
	if got := pr.MCPServers[0].Server.Command; got != "entry" {
		t.Errorf("entry override did not win; command = %q, want entry", got)
	}
}

// Under STRICT mode (the default), a same-name component with DIFFERING content
// in plugin.json vs the entry is an ambiguous packaging conflict and must be a
// hard error rather than a silent guess.
func TestProject_StrictConflict_Errors(t *testing.T) {
	cacheDir := "/cache"
	files := map[string][]byte{
		filepath.Join(cacheDir, ".claude-plugin", "plugin.json"): []byte(`{"name":"p","mcpServers":{"srv":{"command":"base"}}}`),
	}
	// Strict defaults to true (nil).
	entry := marketplace.PluginEntry{Name: "p", MCPServers: map[string]any{"srv": map[string]any{"command": "entry"}}}
	_, err := marketplace.ProjectWithReader(entry, cacheDir, fakeFS(files))
	if err == nil {
		t.Fatal("strict mode must error on a differing same-name conflict")
	}
	if !strings.Contains(err.Error(), "defined twice with different content") {
		t.Errorf("error should explain the conflict; got: %v", err)
	}
}

// Even under strict mode, an IDENTICAL same-name component in both places is not
// a conflict — it collapses to one without error.
func TestProject_StrictConflict_IdenticalDedups(t *testing.T) {
	cacheDir := "/cache"
	files := map[string][]byte{
		filepath.Join(cacheDir, ".claude-plugin", "plugin.json"): []byte(`{"name":"p","mcpServers":{"srv":{"command":"same"}}}`),
	}
	entry := marketplace.PluginEntry{Name: "p", MCPServers: map[string]any{"srv": map[string]any{"command": "same"}}}
	pr, err := marketplace.ProjectWithReader(entry, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatalf("identical same-name component must not error under strict: %v", err)
	}
	if len(pr.MCPServers) != 1 {
		t.Fatalf("mcp count = %d, want 1 (identical dedups)", len(pr.MCPServers))
	}
}

// A server defined in both plugin.json and the entry that is semantically
// identical but differs only by nil-vs-empty env/headers must NOT be a strict
// conflict. parseMCPSpec built nil when the key was absent and an empty map when
// present-but-empty; reflect.DeepEqual treated those as different, so an
// otherwise-identical server spuriously hard-errored under strict.
func TestProject_StrictConflict_NilVsEmptyMapNotAConflict(t *testing.T) {
	cacheDir := "/cache"
	files := map[string][]byte{
		filepath.Join(cacheDir, ".claude-plugin", "plugin.json"): []byte(`{"name":"p","mcpServers":{"srv":{"command":"x"}}}`),
	}
	// Entry specifies an explicit empty env (and headers); semantically the same
	// server as plugin.json's, which omits them.
	entry := marketplace.PluginEntry{Name: "p", MCPServers: map[string]any{
		"srv": map[string]any{"command": "x", "env": map[string]any{}, "headers": map[string]any{}},
	}}
	pr, err := marketplace.ProjectWithReader(entry, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatalf("nil-vs-empty env/headers must not be a strict conflict: %v", err)
	}
	if len(pr.MCPServers) != 1 {
		t.Fatalf("mcp count = %d, want 1", len(pr.MCPServers))
	}
}

// Identical hooks declared by both plugin.json and the entry must collapse to
// one, otherwise the hook is registered twice in settings.json and runs twice.
func TestProject_DedupsIdenticalHooks(t *testing.T) {
	cacheDir := "/cache"
	files := map[string][]byte{
		filepath.Join(cacheDir, ".claude-plugin", "plugin.json"): []byte(`{"name":"p","hooks":"run.sh"}`),
	}
	entry := marketplace.PluginEntry{Name: "p", Hooks: "run.sh"}
	pr, err := marketplace.ProjectWithReader(entry, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Hooks) != 1 {
		t.Fatalf("hooks = %d, want 1 (identical hooks must dedup)", len(pr.Hooks))
	}
}

// Hooks that differ in any field are genuinely distinct and must BOTH survive —
// dedup is exact-content only, never an event-keyed override that could silently
// drop a legitimate second hook.
func TestProject_DistinctHooksCoexist(t *testing.T) {
	cacheDir := "/cache"
	files := map[string][]byte{
		filepath.Join(cacheDir, ".claude-plugin", "plugin.json"): []byte(`{"name":"p","hooks":"a.sh"}`),
	}
	entry := marketplace.PluginEntry{Name: "p", Hooks: "b.sh"}
	pr, err := marketplace.ProjectWithReader(entry, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Hooks) != 2 {
		t.Fatalf("hooks = %d, want 2 (distinct commands must both survive)", len(pr.Hooks))
	}
}

func TestProject_Strict_MissingPluginJSON(t *testing.T) {
	// Strict mode but no plugin.json — should return empty result, no error.
	cache := t.TempDir()
	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "x"}, cache)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pr.MCPServers)+len(pr.Skills)+len(pr.Commands)+len(pr.LSPServers) != 0 {
		t.Errorf("expected empty projection, got %+v", pr)
	}
}

func TestProject_Hooks_StringCommand(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"h","hooks":"${CLAUDE_PLUGIN_ROOT}/hook.sh"}`
	if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "h"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Hooks) != 1 {
		t.Fatalf("hooks = %d, want 1", len(pr.Hooks))
	}
	if pr.Hooks[0].Event != "PreToolUse" {
		t.Errorf("event = %q, want PreToolUse", pr.Hooks[0].Event)
	}
	if !strings.HasPrefix(pr.Hooks[0].Command, cache) {
		t.Errorf("hook command not resolved: %s", pr.Hooks[0].Command)
	}
}

func TestProject_Hooks_MapWithEvent(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"h","hooks":{"PostToolUse":{"command":"${CLAUDE_PLUGIN_ROOT}/post.sh","matcher":"Bash"}}}`
	if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "h"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Hooks) != 1 {
		t.Fatalf("hooks = %d, want 1", len(pr.Hooks))
	}
	h := pr.Hooks[0]
	if h.Event != "PostToolUse" {
		t.Errorf("event = %q", h.Event)
	}
	if h.Matcher != "Bash" {
		t.Errorf("matcher = %q", h.Matcher)
	}
	if !strings.HasPrefix(h.Command, cache) {
		t.Errorf("command not resolved: %s", h.Command)
	}
}

func TestProject_MCPSpec_FullFields(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"name": "full",
		"mcpServers": {
			"full-srv": {
				"type": "stdio",
				"command": "${CLAUDE_PLUGIN_ROOT}/server",
				"args": ["${CLAUDE_PLUGIN_ROOT}/config.json", "--verbose"],
				"env": {"PLUGIN_DIR": "${CLAUDE_PLUGIN_ROOT}"},
				"agents": ["claude", "opencode"]
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	pr, err := marketplace.Project(marketplace.PluginEntry{Name: "full"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.MCPServers) != 1 {
		t.Fatalf("mcp = %d", len(pr.MCPServers))
	}
	spec := pr.MCPServers[0].Server
	if spec.Type != "stdio" {
		t.Errorf("type = %q", spec.Type)
	}
	if !strings.HasPrefix(spec.Command, cache) {
		t.Errorf("command not resolved: %s", spec.Command)
	}
	if len(spec.Args) != 2 || !strings.HasPrefix(spec.Args[0], cache) {
		t.Errorf("args not resolved: %v", spec.Args)
	}
	if v, ok := spec.Env["PLUGIN_DIR"]; !ok || !strings.HasPrefix(v, cache) {
		t.Errorf("env not resolved: %v", spec.Env)
	}
	if len(spec.Agents) != 2 {
		t.Errorf("agents = %v", spec.Agents)
	}
}

// ---------------------------------------------------------------------------
// Tests for full component projection (skills / commands / subagents).
// These use ProjectWithReader and an in-memory readFile to avoid real FS.
// ---------------------------------------------------------------------------

// fakeFS builds a readFile that maps absolute paths to content.
func fakeFS(files map[string][]byte) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if data, ok := files[path]; ok {
			return data, nil
		}
		return nil, os.ErrNotExist
	}
}

func TestProjectWithReader_SkillsFullyLoaded(t *testing.T) {
	const cacheDir = "/fake/cache"
	files := map[string][]byte{
		"/fake/cache/.claude-plugin/plugin.json": []byte(`{
			"name": "sp",
			"skills": ["./skills/tdd", "./skills/refactor"]
		}`),
		// Directory-based skill: <path>/SKILL.md
		"/fake/cache/skills/tdd/SKILL.md":      []byte("---\nname: test-driven-development\ndescription: TDD skill\n---\nWrite tests first.\n"),
		"/fake/cache/skills/refactor/SKILL.md": []byte("---\nname: refactor\ndescription: Refactoring skill\n---\nMake it clean.\n"),
	}
	pr, err := marketplace.ProjectWithReader(
		marketplace.PluginEntry{Name: "sp"},
		cacheDir,
		fakeFS(files),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pr.Skills) != 2 {
		t.Fatalf("skills count = %d, want 2", len(pr.Skills))
	}

	// Build a name set for order-independent assertions.
	byName := make(map[string]struct{})
	for _, sk := range pr.Skills {
		byName[sk.Name] = struct{}{}
	}
	for _, wantName := range []string{"test-driven-development", "refactor"} {
		if _, ok := byName[wantName]; !ok {
			names := make([]string, 0, len(pr.Skills))
			for _, s := range pr.Skills {
				names = append(names, s.Name)
			}
			t.Errorf("skill %q not found; got names %v", wantName, names)
		}
	}

	// All skills must have non-empty body and frontmatter with description.
	for _, sk := range pr.Skills {
		if sk.Body == "" {
			t.Errorf("skill %q has empty body", sk.Name)
		}
		if sk.Frontmatter == nil {
			t.Errorf("skill %q has nil frontmatter", sk.Name)
		}
		if _, ok := sk.Frontmatter["description"]; !ok {
			t.Errorf("skill %q missing description in frontmatter", sk.Name)
		}
	}
}

func TestProjectWithReader_SkillsFullyLoaded_NamesAndBodies(t *testing.T) {
	const cacheDir = "/plugin"
	files := map[string][]byte{
		"/plugin/.claude-plugin/plugin.json": []byte(`{"name":"p","skills":["./skills/alpha","./skills/beta","./skills/gamma"]}`),
		"/plugin/skills/alpha/SKILL.md":      []byte("---\nname: alpha-skill\ndescription: Alpha\n---\nAlpha body text.\n"),
		"/plugin/skills/beta/SKILL.md":       []byte("---\nname: beta-skill\ndescription: Beta\n---\nBeta body text.\n"),
		"/plugin/skills/gamma/SKILL.md":      []byte("---\nname: gamma-skill\ndescription: Gamma\n---\nGamma body text.\n"),
	}
	pr, err := marketplace.ProjectWithReader(marketplace.PluginEntry{Name: "p"}, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pr.Skills) != 3 {
		t.Fatalf("skills = %d, want 3", len(pr.Skills))
	}
	for _, sk := range pr.Skills {
		if sk.Name == "" {
			t.Errorf("empty skill name")
		}
		if sk.Body == "" {
			t.Errorf("skill %q has empty body", sk.Name)
		}
		if sk.Frontmatter["name"] == "" {
			t.Errorf("skill %q frontmatter name empty", sk.Name)
		}
	}
}

func TestProjectWithReader_CommandsFullyLoaded(t *testing.T) {
	const cacheDir = "/plugin"
	files := map[string][]byte{
		"/plugin/.claude-plugin/plugin.json": []byte(`{"name":"p","commands":["./commands/deploy.md","./commands/lint.md"]}`),
		// deploy.md has no name in frontmatter → fallback to filename sans .md
		"/plugin/commands/deploy.md": []byte("---\ndescription: Deploy the app\n---\nRun the deploy script.\n"),
		// lint.md has name in frontmatter
		"/plugin/commands/lint.md": []byte("---\nname: run-lint\ndescription: Lint the code\n---\nRun linter.\n"),
	}
	pr, err := marketplace.ProjectWithReader(marketplace.PluginEntry{Name: "p"}, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pr.Commands) != 2 {
		t.Fatalf("commands = %d, want 2", len(pr.Commands))
	}
	byName := make(map[string]struct{})
	for _, cmd := range pr.Commands {
		byName[cmd.Name] = struct{}{}
		if cmd.Body == "" {
			t.Errorf("command %q has empty body", cmd.Name)
		}
	}
	// deploy.md has no frontmatter name → derives from filename
	if _, ok := byName["deploy"]; !ok {
		names := make([]string, 0, len(pr.Commands))
		for _, c := range pr.Commands {
			names = append(names, c.Name)
		}
		t.Errorf("expected command named 'deploy' (from filename), got names: %v", names)
	}
	// lint.md has frontmatter name → "run-lint"
	if _, ok := byName["run-lint"]; !ok {
		names := make([]string, 0, len(pr.Commands))
		for _, c := range pr.Commands {
			names = append(names, c.Name)
		}
		t.Errorf("expected command named 'run-lint' (from frontmatter), got names: %v", names)
	}
}

func TestProjectWithReader_SubagentsFullyLoaded(t *testing.T) {
	const cacheDir = "/plugin"
	files := map[string][]byte{
		"/plugin/.claude-plugin/plugin.json": []byte(`{"name":"p","agents":["./agents/reviewer.md","./agents/coder.md"]}`),
		"/plugin/agents/reviewer.md":         []byte("---\nname: code-reviewer\ndescription: Reviews code\n---\nReview all the things.\n"),
		"/plugin/agents/coder.md":            []byte("---\ndescription: Writes code\n---\nWrite it.\n"),
	}
	pr, err := marketplace.ProjectWithReader(marketplace.PluginEntry{Name: "p"}, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pr.Subagents) != 2 {
		t.Fatalf("subagents = %d, want 2", len(pr.Subagents))
	}
	byName := make(map[string]struct{})
	for _, ag := range pr.Subagents {
		byName[ag.Name] = struct{}{}
		if ag.Body == "" {
			t.Errorf("subagent %q has empty body", ag.Name)
		}
	}
	// reviewer.md has frontmatter name → "code-reviewer"
	if _, ok := byName["code-reviewer"]; !ok {
		names := make([]string, 0, len(pr.Subagents))
		for _, a := range pr.Subagents {
			names = append(names, a.Name)
		}
		t.Errorf("expected subagent 'code-reviewer', got: %v", names)
	}
	// coder.md has no frontmatter name → filename fallback
	if _, ok := byName["coder"]; !ok {
		names := make([]string, 0, len(pr.Subagents))
		for _, a := range pr.Subagents {
			names = append(names, a.Name)
		}
		t.Errorf("expected subagent 'coder', got: %v", names)
	}
}

func TestProjectWithReader_MissingFile_Skipped(t *testing.T) {
	const cacheDir = "/plugin"
	files := map[string][]byte{
		"/plugin/.claude-plugin/plugin.json": []byte(`{"name":"p","skills":["./skills/present","./skills/missing"],"commands":["./cmds/existing.md","./cmds/gone.md"]}`),
		"/plugin/skills/present/SKILL.md":    []byte("---\nname: present\n---\nPresent.\n"),
		"/plugin/cmds/existing.md":           []byte("---\nname: existing\n---\nExists.\n"),
		// skills/missing and cmds/gone.md intentionally absent
	}
	pr, err := marketplace.ProjectWithReader(marketplace.PluginEntry{Name: "p"}, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatalf("missing file should be skipped, not error: %v", err)
	}
	if len(pr.Skills) != 1 {
		t.Errorf("skills = %d, want 1 (missing entry skipped)", len(pr.Skills))
	}
	if pr.Skills[0].Name != "present" {
		t.Errorf("skill name = %q, want present", pr.Skills[0].Name)
	}
	if len(pr.Commands) != 1 {
		t.Errorf("commands = %d, want 1 (missing entry skipped)", len(pr.Commands))
	}
}

func TestProjectWithReader_MalformedFrontmatter_Error(t *testing.T) {
	const cacheDir = "/plugin"
	files := map[string][]byte{
		"/plugin/.claude-plugin/plugin.json": []byte(`{"name":"p","skills":["./skills/bad"]}`),
		// Unterminated frontmatter — no closing ---
		"/plugin/skills/bad/SKILL.md": []byte("---\nname: bad\ndescription: broken\nno closing delimiter\n"),
	}
	_, err := marketplace.ProjectWithReader(marketplace.PluginEntry{Name: "p"}, cacheDir, fakeFS(files))
	if err == nil {
		t.Fatal("expected error for malformed frontmatter, got nil")
	}
	if !strings.Contains(err.Error(), "frontmatter") && !strings.Contains(err.Error(), "load skill") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestProjectWithReader_SkillNameFallbackToBasename(t *testing.T) {
	const cacheDir = "/plugin"
	files := map[string][]byte{
		"/plugin/.claude-plugin/plugin.json": []byte(`{"name":"p","skills":["./skills/my-skill"]}`),
		// No name in frontmatter → use dirname
		"/plugin/skills/my-skill/SKILL.md": []byte("---\ndescription: A skill\n---\nBody.\n"),
	}
	pr, err := marketplace.ProjectWithReader(marketplace.PluginEntry{Name: "p"}, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pr.Skills) != 1 {
		t.Fatalf("skills = %d, want 1", len(pr.Skills))
	}
	if pr.Skills[0].Name != "my-skill" {
		t.Errorf("skill name = %q, want my-skill (basename fallback)", pr.Skills[0].Name)
	}
}

func TestProjectWithReader_NonStrict_FullyLoaded(t *testing.T) {
	const cacheDir = "/plugin"
	f := false
	files := map[string][]byte{
		"/plugin/skills/inline-skill/SKILL.md": []byte("---\nname: inline-skill\n---\nInline body.\n"),
		"/plugin/agents/inline-agent.md":       []byte("---\nname: inline-agent\n---\nAgent body.\n"),
	}
	entry := marketplace.PluginEntry{
		Name:   "ns",
		Strict: &f,
		Skills: []interface{}{"./skills/inline-skill"},
		Agents: []interface{}{"./agents/inline-agent.md"},
	}
	pr, err := marketplace.ProjectWithReader(entry, cacheDir, fakeFS(files))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pr.Skills) != 1 {
		t.Errorf("skills = %d, want 1", len(pr.Skills))
	}
	if len(pr.Subagents) != 1 {
		t.Errorf("subagents = %d, want 1", len(pr.Subagents))
	}
	if len(pr.Skills) > 0 && pr.Skills[0].Body == "" {
		t.Errorf("non-strict skill has empty body")
	}
}

// errorFS returns a readFile that errors for all paths with the given error.
func errorFS(wrapped error) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		return nil, wrapped
	}
}

// _ is used to suppress unused import lint for errorFS.
var _ = errorFS

func TestProjectWithReader_IOError_Propagates(t *testing.T) {
	const cacheDir = "/plugin"
	ioErr := errors.New("disk failure")
	// plugin.json reads fine, but skill file returns a hard I/O error
	readFile := func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "plugin.json") {
			return []byte(`{"name":"p","skills":["./skills/bad"]}`), nil
		}
		return nil, ioErr
	}
	_, err := marketplace.ProjectWithReader(marketplace.PluginEntry{Name: "p"}, cacheDir, readFile)
	if err == nil {
		t.Fatal("expected I/O error to propagate, got nil")
	}
}
