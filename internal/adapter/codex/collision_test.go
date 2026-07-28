package codex

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestRenderSubagents_CollisionErrorNamesOrigins pins the message half of issue
// #211. The collision detector itself is correct and stays — Codex's `name` IS
// the agent's identity, so two TOMLs cannot claim one, and silently overwriting
// would be the bug. What was broken was what the error TOLD the user.
//
// It printed two file stems, and colliding subagents almost always SHARE a stem,
// so it rendered as `"code-reviewer" and "code-reviewer"` — the same string
// twice, with no origin. The half of the message meant to identify the conflict
// was structurally uninformative precisely when it fired.
func TestRenderSubagents_CollisionErrorNamesOrigins(t *testing.T) {
	a := &Adapter{}
	p := Paths{AgentsDir: "/codex/agents"}

	t.Run("names each side's plugin", func(t *testing.T) {
		// The pathological residue namespacing cannot resolve: plugin "a" ships
		// "b-c" while plugin "a-b" ships "c", so both derive the same name.
		c := source.Canonical{Subagents: []source.Subagent{
			{Name: "a-b-c", BaseName: "b-c", Plugin: "a", Frontmatter: map[string]any{}},
			{Name: "a-b-c", BaseName: "c", Plugin: "a-b", Frontmatter: map[string]any{}},
		}}
		_, _, err := a.renderSubagents(c, p)
		if err == nil {
			t.Fatal("two subagents resolving to one Codex name must be an error")
		}
		for _, want := range []string{`plugin "a"`, `plugin "a-b"`, `as "b-c"`, `as "c"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should carry %s so the user can tell the sides apart; got: %v", want, err)
			}
		}
	})

	t.Run("names the canonical file for a hand-authored subagent", func(t *testing.T) {
		// Two hand-authored components whose frontmatter names collide: the
		// genuinely user-resolvable case, where "rename one" IS actionable — so
		// the message must point at the files the user can actually edit.
		c := source.Canonical{Subagents: []source.Subagent{
			{Name: "reviewer", Frontmatter: map[string]any{"name": "shared"}},
			{Name: "auditor", Frontmatter: map[string]any{"name": "shared"}},
		}}
		_, _, err := a.renderSubagents(c, p)
		if err == nil {
			t.Fatal("two frontmatter names resolving to one Codex name must be an error")
		}
		for _, want := range []string{"subagents/reviewer.md", "subagents/auditor.md", `"shared"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should carry %s; got: %v", want, err)
			}
		}
	})

	t.Run("distinct names render both", func(t *testing.T) {
		c := source.Canonical{Subagents: []source.Subagent{
			{Name: "a-reviewer", BaseName: "reviewer", Plugin: "a", Frontmatter: map[string]any{"name": "a-reviewer"}},
			{Name: "b-reviewer", BaseName: "reviewer", Plugin: "b", Frontmatter: map[string]any{"name": "b-reviewer"}},
		}}
		ops, _, err := a.renderSubagents(c, p)
		if err != nil {
			t.Fatalf("namespaced subagents must not collide: %v", err)
		}
		if len(ops) != 2 || ops[0].Path == ops[1].Path {
			t.Fatalf("want two ops at distinct paths; got %d: %+v", len(ops), ops)
		}
	})
}
