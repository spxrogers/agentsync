package claude_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
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
	ops, _, err := a.Render(in, adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops); err != nil {
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
	ops, _, err := a.Render(in, adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops); err != nil {
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
	ops, _, err := a.Render(in, adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops); err != nil {
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
	ops, _, err := a.Render(in, adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops); err != nil {
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
