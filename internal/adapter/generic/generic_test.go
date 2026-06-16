package generic_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/adapter/generic"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func findOp(ops []adapter.FileOp, suffix string) *adapter.FileOp {
	for i := range ops {
		if strings.HasSuffix(filepath.ToSlash(ops[i].Path), suffix) {
			return &ops[i]
		}
	}
	return nil
}

func hasSkip(skips []adapter.Skip, component, name string) bool {
	for _, s := range skips {
		if s.Component == component && s.Name == name {
			return true
		}
	}
	return false
}

// --- adapter-surface from spec ---

func TestCapabilitiesAndStrategyFromSpec(t *testing.T) {
	memOnly := generic.New(generic.Spec{Name: "mem", Memory: generic.FileTarget{Project: "AGENTS.md"}}, generic.Options{})
	if got := memOnly.Capabilities(); got&adapter.CapMemory == 0 || got&adapter.CapMCP != 0 {
		t.Fatalf("memory-only caps wrong: %v", got)
	}
	if memOnly.KeyMergeStrategy() != "" {
		t.Fatalf("memory-only must have no key-merge strategy")
	}
	withMCP := generic.New(generic.Spec{Name: "x", MCP: generic.MCPTarget{Project: ".x/mcp.json"}}, generic.Options{})
	if withMCP.Capabilities()&adapter.CapMCP == 0 {
		t.Fatal("MCP cap missing")
	}
	if withMCP.KeyMergeStrategy() != "merge-jsonc-keys" {
		t.Fatal("MCP spec must use the JSONC-tolerant merge")
	}
	withSkills := generic.New(generic.Spec{Name: "x", Skills: generic.FileTarget{Project: ".agents/skills"}}, generic.Options{})
	if withSkills.Capabilities()&adapter.CapSkill == 0 {
		t.Fatal("Skill cap missing when a Skills target is declared")
	}
	// A skills target is a directory write, not a JSON key-merge surface.
	if withSkills.KeyMergeStrategy() != "" {
		t.Fatal("a skills-only spec must have no key-merge strategy")
	}
}

func TestDetect(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".zed"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := generic.New(generic.Spec{Name: "zed", DetectDir: ".zed"}, generic.Options{TargetRoot: tmp, LookPath: func(string) (string, error) { return "", errors.New("no") }})
	if ok, _ := a.Detect(); !ok {
		t.Fatal("Detect by dir failed")
	}
	b := generic.New(generic.Spec{Name: "z", DetectBin: "zbin"}, generic.Options{TargetRoot: t.TempDir(), LookPath: func(f string) (string, error) {
		if f == "zbin" {
			return "/usr/bin/zbin", nil
		}
		return "", errors.New("no")
	}})
	if ok, _ := b.Detect(); !ok {
		t.Fatal("Detect by bin failed")
	}
}

func TestProjectScopeGuard(t *testing.T) {
	a := generic.New(generic.Spec{Name: "x", Memory: generic.FileTarget{Project: "AGENTS.md"}}, generic.Options{TargetRoot: t.TempDir()})
	if _, _, err := a.Render(secrets.ForRender(source.Canonical{Memory: source.Memory{Body: "x\n"}}), adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Render guard: %v", err)
	}
	if _, err := a.Ingest(adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatalf("Ingest guard: %v", err)
	}
}

// --- memory render: scope-aware ---

func TestRender_Memory_ScopeAware(t *testing.T) {
	tmp := t.TempDir()
	spec := generic.Spec{Name: "amp", Memory: generic.FileTarget{User: ".config/amp/AGENTS.md", Project: "AGENTS.md"}}
	a := generic.New(spec, generic.Options{TargetRoot: tmp})

	uops, _, _ := a.Render(secrets.ForRender(source.Canonical{Memory: source.Memory{Body: "be terse\n"}}), adapter.ScopeUser, "")
	if op := findOp(uops, ".config/amp/AGENTS.md"); op == nil || string(op.Content) != "be terse\n" {
		t.Fatalf("user memory wrong: %+v", uops)
	}

	proj := t.TempDir()
	pc := source.Canonical{Memory: source.Memory{Body: "be terse\n"}, Project: &source.Canonical{Memory: source.Memory{Body: "be terse\n"}}}
	pops, _, _ := a.Render(secrets.ForRender(pc), adapter.ScopeProject, proj)
	if op := findOp(pops, "AGENTS.md"); op == nil || op.Path != filepath.Join(proj, "AGENTS.md") {
		t.Fatalf("project memory wrong: %+v", pops)
	}
}

func TestRender_Memory_ScopeGapReported(t *testing.T) {
	// Project-only memory: user scope should report a skip.
	spec := generic.Spec{Name: "goose", Memory: generic.FileTarget{Project: ".goosehints"}}
	a := generic.New(spec, generic.Options{TargetRoot: t.TempDir()})
	ops, skips, _ := a.Render(secrets.ForRender(source.Canonical{Memory: source.Memory{Body: "x\n"}}), adapter.ScopeUser, "")
	if findOp(ops, ".goosehints") != nil {
		t.Fatal("project-only memory must not render at user scope")
	}
	if !hasSkip(skips, "memory", "goose") {
		t.Fatalf("expected memory scope-gap skip, got %+v", skips)
	}
}

// --- MCP dialects ---

func mcpServers(t *testing.T, content []byte, rootKey string) map[string]any {
	t.Helper()
	var top map[string]any
	if err := json.Unmarshal(content, &top); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, content)
	}
	m, ok := top[rootKey].(map[string]any)
	if !ok {
		t.Fatalf("root key %q missing: %v", rootKey, top)
	}
	return m
}

func TestRender_MCP_Dialects(t *testing.T) {
	stdio := source.MCPServer{ID: "s", Server: source.MCPServerSpec{Type: "stdio", Command: "npx", Args: []string{"-y", "p"}, Env: map[string]string{"K": "v"}}}
	remote := source.MCPServer{ID: "r", Server: source.MCPServerSpec{Type: "http", URL: "https://x/mcp", Headers: map[string]string{"A": "b"}}}
	c := source.Canonical{MCPServers: []source.MCPServer{stdio, remote}}

	cases := []struct {
		name        string
		mcp         generic.MCPTarget
		rootKey     string
		wantType    bool   // transport field present
		stdioType   string // expected transport value for stdio
		remoteURLK  string // expected remote url key
		remoteValue string
	}{
		{"default-inferred", generic.MCPTarget{Project: ".a/mcp.json"}, "mcpServers", false, "", "url", ""},
		{"claude-type", generic.MCPTarget{Project: ".b/mcp.json", TransportKey: "type"}, "mcpServers", true, "stdio", "url", "http"},
		{"copilot-servers", generic.MCPTarget{Project: ".vscode/mcp.json", RootKey: "servers", TransportKey: "type"}, "servers", true, "stdio", "url", "http"},
		{"copilot-cli-local", generic.MCPTarget{Project: ".c/mcp.json", TransportKey: "type", StdioValue: "local"}, "mcpServers", true, "local", "url", "http"},
		{"zed-context", generic.MCPTarget{Project: ".zed/settings.json", RootKey: "context_servers"}, "context_servers", false, "", "url", ""},
		{"qwen-httpurl", generic.MCPTarget{Project: ".qwen/settings.json", RemoteURLKey: "httpUrl"}, "mcpServers", false, "", "httpUrl", ""},
		{"crush-mcp", generic.MCPTarget{Project: ".x/crush.json", RootKey: "mcp", TransportKey: "type"}, "mcp", true, "stdio", "url", "http"},
	}
	proj := t.TempDir()
	pc := c
	pc.Project = &c
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := generic.New(generic.Spec{Name: "t", MCP: tc.mcp}, generic.Options{TargetRoot: t.TempDir()})
			ops, _, err := a.Render(secrets.ForRender(pc), adapter.ScopeProject, proj)
			if err != nil {
				t.Fatal(err)
			}
			op := findOp(ops, filepath.Base(tc.mcp.Project))
			if op == nil {
				t.Fatalf("mcp op missing: %+v", ops)
			}
			servers := mcpServers(t, op.Content, tc.rootKey)
			sm := servers["s"].(map[string]any)
			rm := servers["r"].(map[string]any)
			if sm["command"] != "npx" {
				t.Fatalf("stdio command wrong: %v", sm)
			}
			if tc.wantType {
				if sm["type"] != tc.stdioType {
					t.Fatalf("stdio transport value = %v, want %s", sm["type"], tc.stdioType)
				}
			} else if _, has := sm["type"]; has {
				t.Fatalf("inferred dialect must not write a type field: %v", sm)
			}
			if rm[tc.remoteURLK] != "https://x/mcp" {
				t.Fatalf("remote url key %q wrong: %v", tc.remoteURLK, rm)
			}
		})
	}
}

func TestRender_MCP_SkipWhenUnsupported(t *testing.T) {
	// Memory-only spec: an MCP server in canonical must be reported as a skip.
	a := generic.New(generic.Spec{Name: "goose", Memory: generic.FileTarget{Project: ".goosehints"}}, generic.Options{TargetRoot: t.TempDir()})
	proj := t.TempDir()
	c := source.Canonical{MCPServers: []source.MCPServer{{ID: "g", Server: source.MCPServerSpec{Command: "x"}}}}
	c.Project = &source.Canonical{MCPServers: c.MCPServers}
	ops, skips, _ := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if findOp(ops, "mcp") != nil {
		t.Fatal("memory-only spec must not write MCP")
	}
	if !hasSkip(skips, "mcp", "g") {
		t.Fatalf("expected mcp skip, got %+v", skips)
	}
}

func TestRender_UnsupportedComponentsSkipped(t *testing.T) {
	a := generic.New(generic.Spec{Name: "x", Memory: generic.FileTarget{Project: "AGENTS.md"}}, generic.Options{TargetRoot: t.TempDir()})
	proj := t.TempDir()
	inner := source.Canonical{
		Skills:     []source.Skill{{Name: "sk", Body: "x\n"}},
		Subagents:  []source.Subagent{{Name: "sa", Body: "y\n"}},
		Commands:   []source.Command{{Name: "cmd", Body: "z\n"}},
		Hooks:      []source.Hook{{Event: "PreToolUse", Command: "e"}},
		LSPServers: []source.LSPServer{{ID: "gopls"}},
	}
	c := inner
	c.Project = &inner
	_, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct{ comp, name string }{{"skill", "sk"}, {"subagent", "sa"}, {"command", "cmd"}, {"hook", "PreToolUse"}, {"lsp", "gopls"}} {
		if !hasSkip(skips, w.comp, w.name) {
			t.Errorf("expected %s skip for %q, got %+v", w.comp, w.name, skips)
		}
	}
}

// --- apply + round-trips ---

func TestApply_MCP_PreservesForeign_AndRoundTrips(t *testing.T) {
	tmp := t.TempDir()
	spec := generic.Spec{Name: "factory", MCP: generic.MCPTarget{User: ".factory/mcp.json", TransportKey: "type"}}
	a := generic.New(spec, generic.Options{TargetRoot: tmp})
	mcpPath := filepath.Join(tmp, ".factory", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{ "mcpServers": { "user-srv": { "command": "mine" } } }`), 0o644); err != nil {
		t.Fatal(err)
	}
	in := source.Canonical{MCPServers: []source.MCPServer{
		{ID: "stdio", Server: source.MCPServerSpec{Type: "stdio", Command: "npx", Env: map[string]string{"K": "v"}}},
		{ID: "remote", Server: source.MCPServerSpec{Type: "http", URL: "https://x/mcp", Headers: map[string]string{"A": "b"}}},
	}}
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	// Foreign server preserved.
	data, _ := os.ReadFile(mcpPath)
	if !strings.Contains(string(data), "user-srv") {
		t.Fatalf("foreign server clobbered: %s", data)
	}
	// Round-trip.
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]source.MCPServerSpec{}
	for _, m := range got.MCPServers {
		byID[m.ID] = m.Server
	}
	if s := byID["stdio"]; s.Type != "stdio" || s.Command != "npx" || s.Env["K"] != "v" {
		t.Fatalf("stdio round-trip: %+v", s)
	}
	if s := byID["remote"]; s.Type != "http" || s.URL != "https://x/mcp" || s.Headers["A"] != "b" {
		t.Fatalf("remote round-trip: %+v", s)
	}
}

// TestApply_JSONC_NamespacedKey_NoClobber is the regression for the JSONC-tolerant
// merge: a hand-edited settings file with COMMENTS and a foreign key must survive
// (not be clobbered), and a namespaced root key (Amp's `amp.mcpServers`) must work.
func TestApply_JSONC_NamespacedKey_NoClobber(t *testing.T) {
	tmp := t.TempDir()
	spec := generic.Spec{Name: "amp", MCP: generic.MCPTarget{User: ".config/amp/settings.json", RootKey: "amp.mcpServers"}}
	a := generic.New(spec, generic.Options{TargetRoot: tmp})

	settings := filepath.Join(tmp, ".config", "amp", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	// A realistic JSONC settings file: line comment + foreign key + trailing comma.
	if err := os.WriteFile(settings, []byte("{\n  // user prefs\n  \"amp.notifications.enabled\": true,\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	in := source.Canonical{MCPServers: []source.MCPServer{{ID: "github", Server: source.MCPServerSpec{Command: "npx", Args: []string{"-y", "x"}}}}}
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if ops[0].MergeStrategy != "merge-jsonc-keys" {
		t.Fatalf("breadth MCP op must use merge-jsonc-keys, got %q", ops[0].MergeStrategy)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}

	// Result must be valid strict JSON (comments stripped), with foreign key kept
	// and our namespaced server map added.
	data, _ := os.ReadFile(settings)
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("merged settings is not valid JSON: %v\n%s", err, data)
	}
	if got["amp.notifications.enabled"] != true {
		t.Fatalf("foreign key clobbered: %v", got)
	}
	ns, ok := got["amp.mcpServers"].(map[string]any)
	if !ok || ns["github"] == nil {
		t.Fatalf("namespaced amp.mcpServers.github missing: %v", got)
	}

	// Round-trip back through the JSONC-tolerant ingest.
	round, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(round.MCPServers) != 1 || round.MCPServers[0].ID != "github" || round.MCPServers[0].Server.Command != "npx" {
		t.Fatalf("namespaced-key round-trip lost data: %+v", round.MCPServers)
	}
}

func TestRoundTrip_Memory(t *testing.T) {
	tmp := t.TempDir()
	a := generic.New(generic.Spec{Name: "x", Memory: generic.FileTarget{User: ".x/RULES.md"}}, generic.Options{TargetRoot: tmp})
	ops, _, _ := a.Render(secrets.ForRender(source.Canonical{Memory: source.Memory{Body: "# Rules\n\nGo fast.\n"}}), adapter.ScopeUser, "")
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Memory.Body != "# Rules\n\nGo fast.\n" {
		t.Fatalf("memory round-trip: %q", got.Memory.Body)
	}
}

func TestRoundTrip_MCP_HttpUrlDialect(t *testing.T) {
	tmp := t.TempDir()
	spec := generic.Spec{Name: "qwen", MCP: generic.MCPTarget{User: ".qwen/settings.json", RemoteURLKey: "httpUrl"}}
	a := generic.New(spec, generic.Options{TargetRoot: tmp})
	in := source.Canonical{MCPServers: []source.MCPServer{{ID: "r", Server: source.MCPServerSpec{Type: "http", URL: "https://x/mcp"}}}}
	ops, _, _ := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	// On disk the remote URL is under httpUrl.
	data, _ := os.ReadFile(filepath.Join(tmp, ".qwen", "settings.json"))
	if !strings.Contains(string(data), `"httpUrl"`) {
		t.Fatalf("remote url should use httpUrl key: %s", data)
	}
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServers) != 1 || got.MCPServers[0].Server.URL != "https://x/mcp" || got.MCPServers[0].Server.Type != "http" {
		t.Fatalf("httpUrl round-trip lost data: %+v", got.MCPServers)
	}
}

// TestRoundTrip_Extra confirms unmodeled native MCP keys survive via Extra.
func TestRoundTrip_Extra(t *testing.T) {
	tmp := t.TempDir()
	mcpPath := filepath.Join(tmp, ".a", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{ "mcpServers": { "s": { "command": "x", "timeout": 9 } } }`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := generic.New(generic.Spec{Name: "a", MCP: generic.MCPTarget{User: ".a/mcp.json"}}, generic.Options{TargetRoot: tmp})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServers) != 1 || got.MCPServers[0].Server.Extra["timeout"] == nil {
		t.Fatalf("Extra not captured: %+v", got.MCPServers)
	}
}

// TestRoundTrip_MCP_DualURLDialect covers the Gemini-lineage dual-URL split
// (Qwen: httpUrl = streamable HTTP, url = SSE). A native SSE server must
// canonicalize as sse with its URL captured — not as a corrupted stdio entry
// with the url shunted into Extra — and re-render to the SAME native key.
func TestRoundTrip_MCP_DualURLDialect(t *testing.T) {
	tmp := t.TempDir()
	mcpPath := filepath.Join(tmp, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	native := `{ "mcpServers": {
  "sse-srv": { "url": "https://sse.example/mcp" },
  "http-srv": { "httpUrl": "https://http.example/mcp" }
} }`
	if err := os.WriteFile(mcpPath, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := generic.Spec{Name: "qwen", MCP: generic.MCPTarget{User: ".qwen/settings.json", RemoteURLKey: "httpUrl", SSEURLKey: "url"}}
	a := generic.New(spec, generic.Options{TargetRoot: tmp})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]source.MCPServerSpec{}
	for _, m := range got.MCPServers {
		byID[m.ID] = m.Server
	}
	if s := byID["sse-srv"]; s.Type != "sse" || s.URL != "https://sse.example/mcp" {
		t.Fatalf("native SSE server corrupted on ingest: %+v", s)
	}
	if _, leaked := byID["sse-srv"].Extra["url"]; leaked {
		t.Fatalf("sse url must be modeled, not Extra: %+v", byID["sse-srv"].Extra)
	}
	if s := byID["http-srv"]; s.Type != "http" || s.URL != "https://http.example/mcp" {
		t.Fatalf("httpUrl server corrupted on ingest: %+v", s)
	}

	// Re-render: each server goes back to its own native key.
	ops, _, err := a.Render(secrets.ForRender(got), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mcpPath)
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	servers := top["mcpServers"].(map[string]any)
	sse := servers["sse-srv"].(map[string]any)
	if sse["url"] != "https://sse.example/mcp" {
		t.Fatalf("sse server must re-render under url: %v", sse)
	}
	if _, wrong := sse["httpUrl"]; wrong {
		t.Fatalf("sse server must not gain httpUrl: %v", sse)
	}
	httpSrv := servers["http-srv"].(map[string]any)
	if httpSrv["httpUrl"] != "https://http.example/mcp" {
		t.Fatalf("http server must re-render under httpUrl: %v", httpSrv)
	}
}

// TestRoundTrip_MCP_TransportAliases guards the two transport-mapping fixes:
// a documented "stdio" alias in a dialect whose StdioValue differs (Copilot CLI
// recommends type: "stdio" for cross-client compat alongside its own "local"),
// and a native "sse" type surviving a capture→apply round trip instead of being
// flipped to the generic remote value.
func TestRoundTrip_MCP_TransportAliases(t *testing.T) {
	tmp := t.TempDir()
	mcpPath := filepath.Join(tmp, ".copilot", "mcp-config.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	native := `{ "mcpServers": {
  "alias-stdio": { "type": "stdio", "command": "npx", "args": ["-y", "x"] },
  "sse-srv": { "type": "sse", "url": "https://sse.example/mcp" }
} }`
	if err := os.WriteFile(mcpPath, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := generic.Spec{Name: "copilot-cli", MCP: generic.MCPTarget{User: ".copilot/mcp-config.json", TransportKey: "type", StdioValue: "local"}}
	a := generic.New(spec, generic.Options{TargetRoot: tmp})
	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]source.MCPServerSpec{}
	for _, m := range got.MCPServers {
		byID[m.ID] = m.Server
	}
	if s := byID["alias-stdio"]; s.Type != "stdio" || s.Command != "npx" {
		t.Fatalf(`documented "stdio" alias must canonicalize as stdio (not a command-less http husk): %+v`, s)
	}
	if s := byID["sse-srv"]; s.Type != "sse" {
		t.Fatalf("native sse type lost on ingest: %+v", s)
	}

	ops, _, err := a.Render(secrets.ForRender(got), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mcpPath)
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	servers := top["mcpServers"].(map[string]any)
	if s := servers["alias-stdio"].(map[string]any); s["type"] != "local" || s["command"] != "npx" {
		t.Fatalf("stdio server must re-render with the dialect's own stdio value and KEEP its command: %v", s)
	}
	if s := servers["sse-srv"].(map[string]any); s["type"] != "sse" {
		t.Fatalf("sse transport must survive the round trip, not flip to http: %v", s)
	}
}

// --- skills ---

// TestSkills_RoundTrip_Fidelity anchors to a spec-complete on-disk skill fixture
// (SKILL.md + a bundled script with +x, a references file, and a BINARY asset)
// and asserts the whole directory survives render→apply→ingest byte-for-byte —
// per the "fidelity tests anchor to the artifact, not the model" rule. It also
// pins the SKILL.md bytes to the shared claude.SkillFileOps projection, which is
// what lets a breadth agent's .agents/skills tree dedupe with Codex's identical
// ops rather than trip the pipeline's divergent-bytes guard.
func TestSkills_RoundTrip_Fidelity(t *testing.T) {
	proj := t.TempDir()
	// goose targets the shared cross-vendor .agents/skills at both scopes.
	spec := generic.Spec{Name: "goose", Skills: generic.FileTarget{User: ".agents/skills", Project: ".agents/skills"}}
	a := generic.New(spec, generic.Options{TargetRoot: t.TempDir()})

	bin := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x0a}
	skill := source.Skill{
		Name:        "pdf",
		Frontmatter: map[string]any{"name": "pdf", "description": "work with PDFs"},
		Body:        "# PDF\n\nDo the thing.\n",
		Files: []source.SkillFile{
			{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\necho hi\n"), Mode: 0o755},
			{Path: "references/REF.md", Content: []byte("# Ref\n"), Mode: 0o644},
			{Path: "assets/logo.bin", Content: bin, Mode: 0o644},
		},
	}
	inner := source.Canonical{Skills: []source.Skill{skill}}
	c := inner
	c.Project = &inner // project overlay carries the same skill

	ops, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if hasSkip(skips, "skill", "pdf") {
		t.Fatalf("a supported skills spec must not skip the skill: %+v", skips)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}

	skillDir := filepath.Join(proj, ".agents", "skills", "pdf")
	// SKILL.md is exactly the shared projection (so it dedupes with Codex).
	wantSkillMD, err := claude.EncodeFrontmatter(skill.Frontmatter, skill.Body)
	if err != nil {
		t.Fatal(err)
	}
	gotSkillMD, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSkillMD, wantSkillMD) {
		t.Fatalf("SKILL.md not byte-identical to the shared claude.SkillFileOps projection")
	}
	// Bundled binary asset survives byte-for-byte; executable bit preserved.
	gotBin, err := os.ReadFile(filepath.Join(skillDir, "assets", "logo.bin"))
	if err != nil || !bytes.Equal(gotBin, bin) {
		t.Fatalf("binary asset not preserved byte-for-byte: %v", err)
	}
	info, err := os.Stat(filepath.Join(skillDir, "scripts", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("executable bit dropped from bundled script: %v", info.Mode())
	}

	// Ingest back: the whole skill directory round-trips into canonical.
	got, err := a.Ingest(adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 1 || got.Skills[0].Name != "pdf" {
		t.Fatalf("ingest skills: %+v", got.Skills)
	}
	gs := got.Skills[0]
	if gs.Body != skill.Body {
		t.Fatalf("ingested body mismatch: %q", gs.Body)
	}
	want := map[string]source.SkillFile{}
	for _, f := range skill.Files {
		want[f.Path] = f
	}
	if len(gs.Files) != len(want) {
		t.Fatalf("ingest dropped bundled files: got %d want %d", len(gs.Files), len(want))
	}
	for _, f := range gs.Files {
		w, ok := want[f.Path]
		if !ok {
			t.Fatalf("unexpected bundled file %q", f.Path)
		}
		if !bytes.Equal(f.Content, w.Content) {
			t.Fatalf("bundled file %q content changed on round-trip", f.Path)
		}
		if f.Path == "scripts/run.sh" && f.Mode&0o100 == 0 {
			t.Fatalf("ingested script lost +x: mode %o", f.Mode)
		}
	}
}

// TestSkills_ScopeGapSkip: a skills spec supported only at project scope must
// report a skip (not a stray write) when applied at user scope.
func TestSkills_ScopeGapSkip(t *testing.T) {
	a := generic.New(generic.Spec{Name: "trae", Skills: generic.FileTarget{Project: ".agents/skills"}}, generic.Options{TargetRoot: t.TempDir()})
	c := source.Canonical{Skills: []source.Skill{{Name: "sk", Body: "x\n"}}}
	ops, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if findOp(ops, "SKILL.md") != nil {
		t.Fatal("project-only skills spec must not write at user scope")
	}
	if !hasSkip(skips, "skill", "sk") {
		t.Fatalf("expected user-scope skill skip, got %+v", skips)
	}
}

// TestSkills_UnsupportedSpecStillSkips: a spec with no Skills target must still
// report skills present in canonical as a skip (coverage stays honest).
func TestSkills_UnsupportedSpecStillSkips(t *testing.T) {
	a := generic.New(generic.Spec{Name: "jules", Memory: generic.FileTarget{Project: "AGENTS.md"}}, generic.Options{TargetRoot: t.TempDir()})
	proj := t.TempDir()
	inner := source.Canonical{Skills: []source.Skill{{Name: "sk", Body: "x\n"}}}
	c := inner
	c.Project = &inner
	ops, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if findOp(ops, "SKILL.md") != nil {
		t.Fatal("a spec with no Skills target must not write skills")
	}
	if !hasSkip(skips, "skill", "sk") {
		t.Fatalf("expected skill skip for a skills-less spec, got %+v", skips)
	}
}
