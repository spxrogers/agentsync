package claude_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestIngest_RoundTripsMCPAndSkills(t *testing.T) {
	tmp := t.TempDir()
	enabled := true
	in := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "github",
			Server: source.MCPServerSpec{
				Type: "stdio", Command: "npx", Args: []string{"-y", "x"},
				Env: map[string]string{"K": "V"}, Agents: []string{"*"},
				Enabled: &enabled,
			},
		}},
		Skills: []source.Skill{{
			Name:        "review",
			Frontmatter: map[string]any{"name": "review", "description": "x"},
			Body:        "body\n",
		}},
	}
	a := claude.New(claude.Options{TargetRoot: tmp})
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
	if len(out.Skills) != 1 || out.Skills[0].Name != "review" {
		t.Fatalf("Skill roundtrip lost: %+v", out.Skills)
	}
}

// TestIngest_RoundTripsMCPExtra proves unmodeled native MCP fields survive the
// apply→ingest round-trip via the passthrough Extra map, and that a modeled
// field is NOT duplicated into Extra.
func TestIngest_RoundTripsMCPExtra(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "github",
			Server: source.MCPServerSpec{
				Type:    "stdio",
				Command: "npx",
				Extra: map[string]any{
					"timeout":  float64(30), // JSON numbers round-trip as float64
					"disabled": true,
					"cwd":      "/work",
				},
			},
		}},
	}
	a := claude.New(claude.Options{TargetRoot: tmp})
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
		t.Fatalf("mcp = %d", len(out.MCPServers))
	}
	srv := out.MCPServers[0].Server
	if srv.Command != "npx" {
		t.Fatalf("modeled command lost: %q", srv.Command)
	}
	ex := srv.Extra
	if ex["timeout"] != float64(30) || ex["disabled"] != true || ex["cwd"] != "/work" {
		t.Fatalf("unmodeled fields not preserved via Extra: %+v", ex)
	}
	for _, k := range []string{"type", "command"} {
		if _, dup := ex[k]; dup {
			t.Fatalf("modeled key %q duplicated into Extra: %+v", k, ex)
		}
	}
}

func TestIngest_RoundTripsSubagentsAndCommands(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		Subagents: []source.Subagent{{
			Name:        "reviewer",
			Frontmatter: map[string]any{"description": "code review agent"},
			Body:        "You are a code reviewer.\n",
		}},
		Commands: []source.Command{{
			Name:        "format",
			Frontmatter: map[string]any{"description": "Format code"},
			Body:        "Run gofmt on the repo.\n",
		}},
	}
	a := claude.New(claude.Options{TargetRoot: tmp})
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
	if len(out.Subagents) != 1 || out.Subagents[0].Name != "reviewer" {
		t.Fatalf("Subagent roundtrip lost: %+v", out.Subagents)
	}
	if len(out.Commands) != 1 || out.Commands[0].Name != "format" {
		t.Fatalf("Command roundtrip lost: %+v", out.Commands)
	}
}

func TestIngest_RoundTripsHooksAndLSP(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		Hooks: []source.Hook{{
			Event:   "PreToolUse",
			Matcher: "Bash",
			Type:    "command",
			Command: "echo before",
		}},
		LSPServers: []source.LSPServer{{
			ID: "gopls",
			Spec: source.LSPServerSpec{
				Command: "gopls",
				Args:    []string{"serve"},
			},
		}},
	}
	a := claude.New(claude.Options{TargetRoot: tmp})
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
	if len(out.Hooks) != 1 || out.Hooks[0].Event != "PreToolUse" {
		t.Fatalf("Hook roundtrip lost: %+v", out.Hooks)
	}
	if len(out.LSPServers) != 1 || out.LSPServers[0].ID != "gopls" {
		t.Fatalf("LSP roundtrip lost: %+v", out.LSPServers)
	}
}

func TestIngest_RoundTripsMemory(t *testing.T) {
	tmp := t.TempDir()
	in := source.Canonical{
		Memory: source.Memory{Body: "# Agent Memory\n\nRemember: always be helpful.\n"},
	}
	a := claude.New(claude.Options{TargetRoot: tmp})
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

// TestIngest_LenientSkillNotSilentlyDropped is the regression for the bug
// `agentsync import claude` exhibited: a SKILL.md whose description carries an
// unquoted "Triggers on: X, Y" colon-space sequence broke sigs.k8s.io/yaml and
// the silent `continue` in Ingest dropped the whole skill. Now: it loads via
// TestSetStderr_NilResetsToDefault pins the adapter.WarnSink contract: a
// later SetStderr(nil) must not panic and must reset warnings away from the
// previously-configured buffer (back to the os.Stderr default — verified
// indirectly by asserting the previously-set buffer no longer receives the
// warning a known-lenient skill triggers). Without this, the doc claim
// in adapter.WarnSink is a paper promise: today's implementations satisfy
// the contract by coincidence of the stderr() accessor's nil fallback,
// not by the setter doing anything special, so the only thing keeping the
// contract from silently regressing is this test.
func TestSetStderr_NilResetsToDefault(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, ".claude", "skills", "bad-yaml")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same lenient-trigger fixture the LenientSkillNotSilentlyDropped test
	// uses: a colon-space in the description.
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: bad-yaml\ndescription: Triggers on: rebase\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	a := claude.New(claude.Options{TargetRoot: tmp, Stderr: &warn})

	// Reset: a panic here is itself a contract failure.
	a.SetStderr(nil)

	if _, err := a.Ingest(adapter.ScopeUser, ""); err != nil {
		t.Fatalf("Ingest after SetStderr(nil): %v", err)
	}
	if warn.Len() > 0 {
		t.Fatalf("SetStderr(nil) did not detach the previously-set buffer; got:\n%s", warn.String())
	}
}

// the lenient fallback AND emits a warning to the configured Stderr so the
// user is notified.
func TestIngest_LenientSkillNotSilentlyDropped(t *testing.T) {
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, ".claude", "skills")

	// Three skills covering each path:
	//   ok       — strict YAML, no warning
	//   bad-yaml — colon-space in description, lenient succeeds → warning
	//   broken   — actually malformed (unterminated fence), both parsers fail
	//              → warning + dropped
	if err := os.MkdirAll(filepath.Join(skillsDir, "ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillsDir, "bad-yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skillsDir, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "ok", "SKILL.md"),
		[]byte("---\nname: ok\ndescription: simple\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "bad-yaml", "SKILL.md"),
		[]byte("---\nname: bad-yaml\ndescription: Does the thing. Triggers on: foo, bar, baz.\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "broken", "SKILL.md"),
		[]byte("---\nname: broken\nno closing fence\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	a := claude.New(claude.Options{TargetRoot: tmp, Stderr: &warn})
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	names := map[string]bool{}
	for _, s := range out.Skills {
		names[s.Name] = true
	}
	if !names["ok"] {
		t.Errorf("strict-YAML skill 'ok' missing from Ingest: %v", names)
	}
	if !names["bad-yaml"] {
		t.Errorf("bad-yaml skill silently dropped — lenient fallback didn't run: %v", names)
	}
	if names["broken"] {
		t.Errorf("a structurally-broken skill should NOT load: %v", names)
	}

	got := warn.String()
	if !strings.Contains(got, "bad-yaml") {
		t.Errorf("Stderr missing warning for lenient skill 'bad-yaml':\n%s", got)
	}
	if !strings.Contains(got, "broken") {
		t.Errorf("Stderr missing warning for dropped skill 'broken':\n%s", got)
	}
	// The 'ok' skill must NOT trigger a warning — strict YAML is silent.
	if strings.Contains(got, "skill ok") || strings.Contains(got, "skill \"ok\"") {
		t.Errorf("strict-YAML skill incorrectly produced a warning:\n%s", got)
	}
}
