package marketplace

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestNamespaceProjected covers the projection-time rename that resolves
// cross-plugin name collisions (issue #211): a plugin-provided component's Name —
// the thing every adapter derives its destination path and identity from — is
// rewritten to "<plugin>-<name>", with the upstream name kept in BaseName.
func TestNamespaceProjected(t *testing.T) {
	t.Run("renames every name-keyed component and stamps provenance", func(t *testing.T) {
		pr := ProjectionResult{
			Skills:    []source.Skill{{Name: "code-review", Frontmatter: map[string]any{"name": "code-review"}}},
			Subagents: []source.Subagent{{Name: "code-reviewer", Frontmatter: map[string]any{"name": "code-reviewer"}}},
			Commands:  []source.Command{{Name: "review", Frontmatter: map[string]any{"description": "d"}}},
		}
		if err := namespaceProjected(&pr, "feature-dev"); err != nil {
			t.Fatalf("namespaceProjected: %v", err)
		}
		for _, tc := range []struct{ got, want, field string }{
			{pr.Skills[0].Name, "feature-dev-code-review", "skill Name"},
			{pr.Skills[0].BaseName, "code-review", "skill BaseName"},
			{pr.Skills[0].Plugin, "feature-dev", "skill Plugin"},
			{pr.Subagents[0].Name, "feature-dev-code-reviewer", "subagent Name"},
			{pr.Subagents[0].BaseName, "code-reviewer", "subagent BaseName"},
			{pr.Subagents[0].Plugin, "feature-dev", "subagent Plugin"},
			{pr.Commands[0].Name, "feature-dev-review", "command Name"},
			{pr.Commands[0].BaseName, "review", "command BaseName"},
			{pr.Commands[0].Plugin, "feature-dev", "command Plugin"},
		} {
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
			}
		}
	})

	// Renaming only the struct field would leave the components still colliding:
	// Codex prefers the frontmatter `name` over the file stem when deriving its
	// TOML `name` (which IS the agent's identity), and Claude's Agent Skills
	// require the frontmatter name to match the skill directory.
	t.Run("rewrites a present frontmatter name", func(t *testing.T) {
		pr := ProjectionResult{Subagents: []source.Subagent{
			{Name: "reviewer", Frontmatter: map[string]any{"name": "reviewer", "model": "opus"}},
		}}
		if err := namespaceProjected(&pr, "pkg"); err != nil {
			t.Fatalf("namespaceProjected: %v", err)
		}
		if got := pr.Subagents[0].Frontmatter["name"]; got != "pkg-reviewer" {
			t.Errorf("frontmatter name = %v, want pkg-reviewer", got)
		}
		if got := pr.Subagents[0].Frontmatter["model"]; got != "opus" {
			t.Errorf("other frontmatter keys must survive; model = %v", got)
		}
	})

	// An absent name stays absent: inventing one would assert an identity the
	// upstream artifact never declared, and for Claude a subagent's name is
	// user-visible invocation surface.
	t.Run("leaves an absent frontmatter name absent", func(t *testing.T) {
		pr := ProjectionResult{Subagents: []source.Subagent{
			{Name: "reviewer", Frontmatter: map[string]any{"description": "d"}},
		}}
		if err := namespaceProjected(&pr, "pkg"); err != nil {
			t.Fatalf("namespaceProjected: %v", err)
		}
		if _, ok := pr.Subagents[0].Frontmatter["name"]; ok {
			t.Errorf("namespacing must not invent a frontmatter name; got %v", pr.Subagents[0].Frontmatter)
		}
	})

	// The frontmatter map can be shared with the caller's parsed artifact, so the
	// rename copies rather than mutating in place.
	t.Run("does not mutate the caller's frontmatter map", func(t *testing.T) {
		fm := map[string]any{"name": "reviewer"}
		pr := ProjectionResult{Subagents: []source.Subagent{{Name: "reviewer", Frontmatter: fm}}}
		if err := namespaceProjected(&pr, "pkg"); err != nil {
			t.Fatalf("namespaceProjected: %v", err)
		}
		if fm["name"] != "reviewer" {
			t.Errorf("the caller's map was mutated in place; got %v", fm)
		}
	})

	// Hooks have no name key; MCP/LSP are id-keyed and guarded by
	// checkProjectedConflicts, whose hard failure on a same-id divergence is a
	// deliberate security property (a silent endpoint hijack is a case to refuse,
	// not to rename apart).
	t.Run("leaves mcp, lsp, and hooks alone", func(t *testing.T) {
		pr := ProjectionResult{
			MCPServers: []source.MCPServer{{ID: "srv"}},
			LSPServers: []source.LSPServer{{ID: "gopls"}},
		}
		if err := namespaceProjected(&pr, "pkg"); err != nil {
			t.Fatalf("namespaceProjected: %v", err)
		}
		if pr.MCPServers[0].ID != "srv" || pr.LSPServers[0].ID != "gopls" {
			t.Errorf("id-keyed components must not be namespaced: %+v %+v", pr.MCPServers, pr.LSPServers)
		}
	})

	// A plugin id originates from a marketplace, outside agentsync's trust
	// boundary. A derived name that would escape its destination directory or
	// smuggle a terminal escape into a diagnostic is refused, not written.
	t.Run("refuses a derived name the write boundary would reject", func(t *testing.T) {
		for _, plugin := range []string{"../evil", "a:b", "esc\x1b[31m"} {
			pr := ProjectionResult{Subagents: []source.Subagent{{Name: "reviewer"}}}
			if err := namespaceProjected(&pr, plugin); err == nil {
				t.Errorf("plugin id %q should not derive a writable component name", plugin)
			}
		}
	})

	// An empty plugin id means "not plugin-provided" — hand-authored components
	// flow through the same code path untouched.
	t.Run("empty plugin is a no-op", func(t *testing.T) {
		pr := ProjectionResult{Subagents: []source.Subagent{{Name: "reviewer"}}}
		if err := namespaceProjected(&pr, ""); err != nil {
			t.Fatalf("namespaceProjected: %v", err)
		}
		if pr.Subagents[0].Name != "reviewer" || pr.Subagents[0].Plugin != "" {
			t.Errorf("an empty plugin id must change nothing; got %+v", pr.Subagents[0])
		}
	})
}
