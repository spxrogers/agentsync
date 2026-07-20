package secrets_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestComponentIDs_EnumeratesAllKindsIncludingProjectOverlay pins that
// ComponentIDs surfaces every id an adapter joins into a destination path —
// subagent/command/skill Names AND MCP/LSP server ids — for BOTH the top-level
// model and the project overlay. render.Plan validates each returned id against
// source.ValidateComponentID, so a kind or a scope missing here is a
// path-traversal id that would reach an adapter unchecked. The project-overlay
// recursion (the `c.Project != nil` branch) is the specific gap this closes: it
// was previously exercised only indirectly through Plan, and never for a
// component that lives ONLY in the project overlay.
func TestComponentIDs_EnumeratesAllKindsIncludingProjectOverlay(t *testing.T) {
	proj := source.Canonical{
		Subagents:  []source.Subagent{{Name: "proj-sub"}},
		Commands:   []source.Command{{Name: "proj-cmd"}},
		Skills:     []source.Skill{{Name: "proj-skill"}},
		MCPServers: []source.MCPServer{{ID: "proj-mcp"}},
		LSPServers: []source.LSPServer{{ID: "proj-lsp"}},
	}
	c := source.Canonical{
		Subagents:  []source.Subagent{{Name: "user-sub"}},
		Commands:   []source.Command{{Name: "user-cmd"}},
		Skills:     []source.Skill{{Name: "user-skill"}},
		MCPServers: []source.MCPServer{{ID: "user-mcp"}},
		LSPServers: []source.LSPServer{{ID: "user-lsp"}},
		Project:    &proj,
	}

	got := map[string]bool{}
	for _, id := range secrets.ForRender(c).ComponentIDs() {
		got[id.Kind+"/"+id.Name] = true
	}

	want := []string{
		// top-level model
		"subagent/user-sub", "command/user-cmd", "skill/user-skill",
		"mcp/user-mcp", "lsp/user-lsp",
		// project overlay — the recursion branch under test
		"subagent/proj-sub", "command/proj-cmd", "skill/proj-skill",
		"mcp/proj-mcp", "lsp/proj-lsp",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("ComponentIDs missing %q; got %v", w, got)
		}
	}
}

// TestComponentIDs_NoProjectOverlayIsUserScopeOnly proves the recursion is
// conditional: a user-scope model (nil Project) yields only its own ids — the
// accessor must not fabricate a project set, or a user-scope apply could be
// rejected for a name only a project overlay would render.
func TestComponentIDs_NoProjectOverlayIsUserScopeOnly(t *testing.T) {
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "user-mcp"}},
	}
	ids := secrets.ForRender(c).ComponentIDs()
	if len(ids) != 1 || ids[0].Kind != "mcp" || ids[0].Name != "user-mcp" {
		t.Fatalf("want exactly [mcp/user-mcp], got %v", ids)
	}
}
