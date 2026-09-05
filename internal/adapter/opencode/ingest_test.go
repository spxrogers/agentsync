package opencode_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// seedOwnedState marks the given write ops as agentsync-owned by writing an
// apply-state file the way a real apply would (render.RecordOpsState), so
// Ingest's ownership filter recognizes them. The ops' files must already exist
// on disk (RecordOpsState hashes them). targetRoot is the adapter's TargetRoot,
// which — like the CLI registry's production wiring — is also the HomeRelative
// base for the state keys, so the keys seeded here match the keys Ingest builds.
func seedOwnedState(t *testing.T, targetRoot string, scope adapter.Scope, project string, ops []adapter.FileOp) {
	t.Helper()
	st := state.New()
	if err := render.RecordOpsState(st, targetRoot, "opencode", scope, project, ops); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	statePath := filepath.Join(targetRoot, ".agentsync", ".state", "targets.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := state.Save(statePath, st); err != nil {
		t.Fatalf("save state: %v", err)
	}
}

// ownFiles marks already-written destination files as agentsync-owned, for
// fixtures that place native files on disk directly (not via Apply).
func ownFiles(t *testing.T, targetRoot string, scope adapter.Scope, project string, files ...string) {
	t.Helper()
	ops := make([]adapter.FileOp, 0, len(files))
	for _, f := range files {
		ops = append(ops, adapter.FileOp{Action: adapter.ActionWrite, Path: f, Mode: 0o644})
	}
	seedOwnedState(t, targetRoot, scope, project, ops)
}

// writeFixtureFile writes content to path, creating parent dirs.
func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func subagentNames(subs []source.Subagent) []string {
	out := make([]string, len(subs))
	for i, s := range subs {
		out[i] = s.Name
	}
	return out
}

func commandNames(cmds []source.Command) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.Name
	}
	return out
}

func containsStr(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestIngest_ProjectScopeMCP_ReadsRootNotDotOpencode is artifact-anchored: it
// writes a real <project>/opencode.json (OpenCode's project config location)
// AND a <project>/.opencode/opencode.json trap, then ingests at project scope
// and asserts ONLY the root file's server is captured — OpenCode does not read
// .opencode/opencode.json, so neither does agentsync.
func TestIngest_ProjectScopeMCP_ReadsRootNotDotOpencode(t *testing.T) {
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "opencode.json"),
		[]byte(`{"mcp":{"projapi":{"type":"local","command":["node","s.js"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Trap: a server in .opencode/opencode.json must NOT be read.
	dotDir := filepath.Join(proj, ".opencode")
	if err := os.MkdirAll(dotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotDir, "opencode.json"),
		[]byte(`{"mcp":{"shouldNotImport":{"type":"local","command":["trap"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	out, err := a.Ingest(adapter.ScopeProject, proj)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.MCPServers) != 1 {
		t.Fatalf("want exactly 1 MCP server (from root opencode.json), got %d: %+v", len(out.MCPServers), out.MCPServers)
	}
	if out.MCPServers[0].ID != "projapi" {
		t.Fatalf("want %q from <root>/opencode.json, got %q (.opencode/opencode.json wrongly read?)", "projapi", out.MCPServers[0].ID)
	}
}

// TestIngest_RoundTripsMCP exercises: Render → Apply → Ingest for MCP servers.
func TestIngest_RoundTripsMCP(t *testing.T) {
	tmp := t.TempDir()
	enabled := true
	in := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "github",
			Server: source.MCPServerSpec{
				Type: "stdio", Command: "npx", Args: []string{"-y", "x"},
				Env: map[string]string{"TOKEN": "abc"}, Enabled: &enabled,
			},
		}},
	}
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.MCPServers) != 1 || out.MCPServers[0].ID != "github" {
		t.Fatalf("MCP roundtrip lost: %+v", out.MCPServers)
	}
	srv := out.MCPServers[0].Server
	if srv.Command != "npx" {
		t.Fatalf("command = %q, want npx", srv.Command)
	}
	// The command array [npx -y x] must split back into command + args so a
	// reconcile write-back doesn't fold the flags into the command string.
	if len(srv.Args) != 2 || srv.Args[0] != "-y" || srv.Args[1] != "x" {
		t.Fatalf("args = %v, want [-y x]", srv.Args)
	}
	if srv.Type != "stdio" {
		t.Fatalf("type = %q, want stdio (from OpenCode \"local\")", srv.Type)
	}
	if srv.Env["TOKEN"] != "abc" {
		t.Fatalf("env token missing: %+v", srv.Env)
	}
}

// TestRender_MCP_SSEMapsToRemote proves the canonical "sse" transport renders as
// OpenCode "remote" (OpenCode has no separate sse transport).
func TestRender_MCP_SSEMapsToRemote(t *testing.T) {
	c := source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "stream",
		Server: source.MCPServerSpec{Type: "sse", URL: "https://sse.example.com"},
	}}}
	a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.MCPServers) != 1 || out.MCPServers[0].Server.URL != "https://sse.example.com" {
		t.Fatalf("sse->remote round-trip lost the server: %+v", out.MCPServers)
	}
}

// TestIngest_RoundTripsMCPHeaders is the regression for OpenCode ingest
// silently dropping a remote MCP server's auth headers. Render writes
// spec["headers"]; ingest must read them back, or an `import`/reconcile
// round-trip produces a server config missing its Authorization header,
// which then 401s on the next apply.
func TestIngest_RoundTripsMCPHeaders(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "remote",
			Server: source.MCPServerSpec{
				Type:    "http",
				URL:     "https://mcp.example.com",
				Headers: map[string]string{"Authorization": "Bearer xyz"},
			},
		}},
	}
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.MCPServers) != 1 {
		t.Fatalf("MCP roundtrip lost: %+v", out.MCPServers)
	}
	if got := out.MCPServers[0].Server.Headers["Authorization"]; got != "Bearer xyz" {
		t.Fatalf("auth header dropped on round-trip: got %q, headers=%+v", got, out.MCPServers[0].Server.Headers)
	}
}

// TestIngest_RoundTripsMemory exercises: Render → Apply → Ingest for AGENTS.md.
func TestIngest_RoundTripsMemory(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		Memory: source.Memory{Body: "# Memory\n\nAlways be helpful.\n"},
	}
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if out.Memory.Body != in.Memory.Body {
		t.Fatalf("Memory roundtrip: got %q, want %q", out.Memory.Body, in.Memory.Body)
	}
}

// TestIngest_RoundTripsSubagentsAndCommands exercises roundtrip for agents + commands.
// Fields dropped on render (tools, color) are not expected back; description+model
// survive; the render-synthesized `mode: subagent` is now PRESERVED on ingest
// (no longer discarded). Ingest captures only agentsync-owned files, so state is
// seeded exactly as a real apply would.
func TestIngest_RoundTripsSubagentsAndCommands(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		Subagents: []source.Subagent{{
			Name: "reviewer",
			Frontmatter: map[string]any{
				"description": "code review agent",
				"model":       "claude-opus-4-5",
			},
			Body: "You are a code reviewer.\n",
		}},
		Commands: []source.Command{{
			Name:        "format",
			Frontmatter: map[string]any{"description": "Format code"},
			Body:        "Run gofmt on the repo.\n",
		}},
	}
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Ownership: apply records the files it wrote in state; seed it so Ingest
	// recognizes these as owned (unmanaged files are filtered out — see
	// TestIngest_OwnershipFilter_SkipsUnmanagedAgentsAndCommands).
	seedOwnedState(t, tmp, adapter.ScopeUser, "", ops)

	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if len(out.Subagents) != 1 || out.Subagents[0].Name != "reviewer" {
		t.Fatalf("Subagent roundtrip lost: %+v", out.Subagents)
	}
	// `mode` is now PRESERVED: render synthesized `mode: subagent`, so ingest
	// must carry it back (dropping it demoted native primary/all agents — #148).
	if got := out.Subagents[0].Frontmatter["mode"]; got != "subagent" {
		t.Fatalf("subagent `mode` = %v, want \"subagent\" (preserved on ingest): %+v", got, out.Subagents[0].Frontmatter)
	}
	if out.Subagents[0].Frontmatter["description"] != "code review agent" {
		t.Fatalf("description missing from subagent: %+v", out.Subagents[0].Frontmatter)
	}
	if out.Subagents[0].Frontmatter["model"] != "claude-opus-4-5" {
		t.Fatalf("model missing from subagent: %+v", out.Subagents[0].Frontmatter)
	}

	if len(out.Commands) != 1 || out.Commands[0].Name != "format" {
		t.Fatalf("Command roundtrip lost: %+v", out.Commands)
	}
}

// TestIngest_JSONC_WithComments verifies that opencode.json with JSONC comments
// is ingested correctly via hujson.
func TestIngest_JSONC_WithComments(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID:     "github",
			Server: source.MCPServerSpec{Command: "npx"},
		}},
	}
	a := opencode.New(opencode.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Manually inject JSONC comments into the written file, simulating a user
	// who hand-edited the file with comments.
	commentedContent := `{
  // managed by agentsync
  "mcp": {
    // github MCP server
    "github": {"command":"npx"}
  }
}
`
	_ = os.WriteFile(ops[0].Path, []byte(commentedContent), 0o644)

	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest JSONC with comments: %v", err)
	}
	if len(out.MCPServers) != 1 || out.MCPServers[0].ID != "github" {
		t.Fatalf("JSONC ingest lost MCP: %+v", out.MCPServers)
	}
}

// TestIngest_PreservesAgentMode is artifact-anchored: it seeds a spec-complete
// on-disk agents/ directory with real name.md files declaring mode: primary /
// all / subagent (each with a description, model, and body), then Ingest →
// Render → write, and asserts the ON-DISK rendered frontmatter still declares
// the original mode for each. A Claude-shaped agent with no mode is added to the
// ingested model to prove render still defaults it to mode: subagent. The oracle
// is the written bytes, not the intermediate parsed model.
func TestIngest_PreservesAgentMode(t *testing.T) {
	targetRoot := t.TempDir()
	agentsDir := filepath.Join(targetRoot, ".config", "opencode", "agents")

	fixtures := []struct{ name, mode string }{
		{"primary-agent", "primary"},
		{"all-agent", "all"},
		{"sub-agent", "subagent"},
	}
	var files []string
	for _, f := range fixtures {
		content := "---\n" +
			"description: " + f.name + " description\n" +
			"mode: " + f.mode + "\n" +
			"model: anthropic/claude-sonnet-4\n" +
			"---\n" +
			"Body for " + f.name + ".\n"
		p := filepath.Join(agentsDir, f.name+".md")
		writeFixtureFile(t, p, content)
		files = append(files, p)
	}
	ownFiles(t, targetRoot, adapter.ScopeUser, "", files...)

	a := opencode.New(opencode.Options{TargetRoot: targetRoot})
	ingested, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(ingested.Subagents) != len(fixtures) {
		t.Fatalf("want %d ingested subagents, got %d: %v", len(fixtures), len(ingested.Subagents), subagentNames(ingested.Subagents))
	}
	// Every ingested subagent must carry its `mode` in canonical frontmatter.
	for _, s := range ingested.Subagents {
		if _, ok := s.Frontmatter["mode"]; !ok {
			t.Errorf("ingested subagent %q lost its `mode`: %+v", s.Name, s.Frontmatter)
		}
	}

	// A Claude-shaped subagent (no mode) proves the render default is preserved.
	ingested.Subagents = append(ingested.Subagents, source.Subagent{
		Name:        "claude-shaped",
		Frontmatter: map[string]any{"description": "no mode declared"},
		Body:        "Claude-shaped body.\n",
	})

	ops, _, err := a.Render(secrets.ForRender(ingested), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	wantMode := map[string]string{
		"primary-agent": "primary",
		"all-agent":     "all",
		"sub-agent":     "subagent",
		"claude-shaped": "subagent", // default preserved when no mode present
	}
	seen := map[string]bool{}
	for _, op := range ops {
		name := strings.TrimSuffix(filepath.Base(op.Path), ".md")
		want, ok := wantMode[name]
		if !ok {
			continue
		}
		// Write the rendered op to disk and re-read, so the assertion is over the
		// on-disk bytes rather than the in-memory model.
		if err := os.WriteFile(op.Path, op.Content, 0o644); err != nil {
			t.Fatalf("write rendered %s: %v", name, err)
		}
		data, err := os.ReadFile(op.Path)
		if err != nil {
			t.Fatalf("read rendered %s: %v", name, err)
		}
		fm, _, _, perr := claude.ParseFrontmatterWithReport(data)
		if perr != nil {
			t.Fatalf("parse rendered %s: %v", name, perr)
		}
		if got, _ := fm["mode"].(string); got != want {
			t.Errorf("rendered %s: mode = %q, want %q\n%s", name, got, want, data)
		}
		seen[name] = true
	}
	for name := range wantMode {
		if !seen[name] {
			t.Errorf("no rendered agent op for %q", name)
		}
	}
}

// TestIngest_OwnershipFilter_SkipsUnmanagedAgentsAndCommands proves Ingest
// captures ONLY agentsync-owned files from the shared agents/ and commands/
// directories, never a user's hand-authored files. Managed files are written
// through the real Render→Apply path and their ownership is seeded exactly as a
// real apply records it; foreign files are dropped in alongside with no state.
// TestIngest_WarnsWhenApplyStateAbsent proves the missing-apply-state case is not silent
// (issue #148 round-2 hardening). With a native OpenCode agent on disk but no
// targets.json (a relocated AGENTSYNC_HOME, or `import` run before any `apply`), Ingest
// owns nothing — the safe direction — but WARNS so the user understands why nothing was
// captured, rather than silently dropping their agents/commands.
func TestIngest_WarnsWhenApplyStateAbsent(t *testing.T) {
	targetRoot := t.TempDir()
	a := opencode.New(opencode.Options{TargetRoot: targetRoot})
	var warn bytes.Buffer
	a.SetStderr(&warn)

	agentsDir := filepath.Join(targetRoot, ".config", "opencode", "agents")
	writeFixtureFile(t, filepath.Join(agentsDir, "some-agent.md"),
		"---\ndescription: an agent\nmode: primary\n---\nBody.\n")

	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !strings.Contains(warn.String(), "found no apply state") {
		t.Fatalf("expected a missing-apply-state warning; stderr=%q", warn.String())
	}
	// Safe direction: nothing captured without ownership state.
	if len(got.Subagents) != 0 {
		t.Fatalf("expected no agents captured without apply state, got %d", len(got.Subagents))
	}
}

func TestIngest_OwnershipFilter_SkipsUnmanagedAgentsAndCommands(t *testing.T) {
	targetRoot := t.TempDir()
	a := opencode.New(opencode.Options{TargetRoot: targetRoot})

	managed := source.Canonical{
		Subagents: []source.Subagent{{
			Name:        "managed-agent",
			Frontmatter: map[string]any{"description": "managed agent", "mode": "primary"},
			Body:        "Managed agent body.\n",
		}},
		Commands: []source.Command{{
			Name:        "managed-cmd",
			Frontmatter: map[string]any{"description": "managed command"},
			Body:        "Managed command body.\n",
		}},
	}
	ops, _, err := a.Render(secrets.ForRender(managed), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	seedOwnedState(t, targetRoot, adapter.ScopeUser, "", ops)

	// Foreign hand-authored files sitting alongside the managed ones.
	agentsDir := filepath.Join(targetRoot, ".config", "opencode", "agents")
	commandsDir := filepath.Join(targetRoot, ".config", "opencode", "commands")
	writeFixtureFile(t, filepath.Join(agentsDir, "foreign-agent.md"),
		"---\ndescription: hand-authored agent\nmode: primary\n---\nForeign agent.\n")
	writeFixtureFile(t, filepath.Join(commandsDir, "foreign-cmd.md"),
		"---\ndescription: hand-authored command\n---\nForeign command.\n")

	got, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	cases := []struct {
		name     string
		gotNames []string
		managed  string
		foreign  string
	}{
		{"agents", subagentNames(got.Subagents), "managed-agent", "foreign-agent"},
		{"commands", commandNames(got.Commands), "managed-cmd", "foreign-cmd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !containsStr(tc.gotNames, tc.managed) {
				t.Errorf("owned %q not captured; got %v", tc.managed, tc.gotNames)
			}
			if containsStr(tc.gotNames, tc.foreign) {
				t.Errorf("foreign %q wrongly captured; got %v", tc.foreign, tc.gotNames)
			}
			if len(tc.gotNames) != 1 {
				t.Errorf("want exactly 1 owned %s, got %v", tc.name, tc.gotNames)
			}
		})
	}
}

// TestRoundTrip_OpenCode_SpecComplete anchors to a spec-complete on-disk tree
// containing a managed primary agent, a foreign agent, a managed command, and a
// foreign command. After Ingest → Render it asserts (a) the managed primary
// agent's mode survives, and (b) the foreign files are neither captured into the
// canonical model nor targeted by any render op (so a subsequent apply leaves
// them untouched). The oracle is the on-disk file set + rendered frontmatter.
func TestRoundTrip_OpenCode_SpecComplete(t *testing.T) {
	targetRoot := t.TempDir()
	a := opencode.New(opencode.Options{TargetRoot: targetRoot})

	// Managed items via the real render/apply path.
	managed := source.Canonical{
		Subagents: []source.Subagent{{
			Name:        "reviewer",
			Frontmatter: map[string]any{"description": "reviewer", "mode": "primary"},
			Body:        "Reviewer body.\n",
		}},
		Commands: []source.Command{{
			Name:        "deploy",
			Frontmatter: map[string]any{"description": "deploy"},
			Body:        "Deploy body.\n",
		}},
	}
	ops, _, err := a.Render(secrets.ForRender(managed), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render managed: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply managed: %v", err)
	}
	seedOwnedState(t, targetRoot, adapter.ScopeUser, "", ops)

	// Foreign files alongside.
	agentsDir := filepath.Join(targetRoot, ".config", "opencode", "agents")
	commandsDir := filepath.Join(targetRoot, ".config", "opencode", "commands")
	foreignAgent := filepath.Join(agentsDir, "handwritten.md")
	foreignCmd := filepath.Join(commandsDir, "handcmd.md")
	writeFixtureFile(t, foreignAgent, "---\ndescription: hand-authored\nmode: all\n---\nForeign.\n")
	writeFixtureFile(t, foreignCmd, "---\ndescription: hand-authored\n---\nForeign.\n")

	ingested, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	// Foreign files are not captured.
	if containsStr(subagentNames(ingested.Subagents), "handwritten") {
		t.Fatalf("foreign agent captured: %v", subagentNames(ingested.Subagents))
	}
	if containsStr(commandNames(ingested.Commands), "handcmd") {
		t.Fatalf("foreign command captured: %v", commandNames(ingested.Commands))
	}

	reOps, _, err := a.Render(secrets.ForRender(ingested), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render ingested: %v", err)
	}
	var sawReviewerMode bool
	for _, op := range reOps {
		// No render op may target a foreign file (which would rewrite/clobber it).
		if op.Path == foreignAgent || op.Path == foreignCmd {
			t.Fatalf("render op targets foreign file %s", op.Path)
		}
		if strings.HasSuffix(op.Path, "/agents/reviewer.md") {
			fm, _, _, perr := claude.ParseFrontmatterWithReport(op.Content)
			if perr != nil {
				t.Fatalf("parse reviewer: %v", perr)
			}
			if got, _ := fm["mode"].(string); got != "primary" {
				t.Fatalf("managed primary mode lost across round-trip: mode = %q\n%s", got, op.Content)
			}
			sawReviewerMode = true
		}
	}
	if !sawReviewerMode {
		t.Fatal("reviewer agent not re-rendered")
	}
	// The foreign files must still exist on disk unchanged (never rewritten).
	if _, err := os.Stat(foreignAgent); err != nil {
		t.Fatalf("foreign agent disappeared: %v", err)
	}
	if _, err := os.Stat(foreignCmd); err != nil {
		t.Fatalf("foreign command disappeared: %v", err)
	}
}
