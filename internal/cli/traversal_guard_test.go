package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestEveryAdapterRejectsTraversalComponentName is the registry-wide guard for
// the Render-time path-traversal fix (issue #156, theme C8): the same central
// check must protect EVERY registered adapter, not just the Gemini/Windsurf
// sites the review flagged. It mirrors TestEveryAdapterClassifiesSkips —
// render every adapter apply uses (via registryFactory) at both scopes — but
// drives the plan through the dispatch waist (render.Plan), the single place the
// guard lives.
//
// For each adapter and each traversal-bearing component Name (path separators,
// "..", absolute, bare ".", all-whitespace, and a control/deceptive rune) the
// plan must be REFUSED (non-nil error, no escaping FileOp emitted). For a
// legitimate control name the plan must succeed and every emitted FileOp.Path
// must stay under the agent's destination root. A vacuity guard (mirroring the
// skip-kind test's totalSkips==0 check) asserts at least one adapter actually
// produced a FileOp for the control name, so the happy path can't pass by
// rendering nothing.
//
// FS-touching: render.Plan drives each adapter's Render (path resolution, some
// adapters stat the destination), so it must run in the container.
func TestEveryAdapterRejectsTraversalComponentName(t *testing.T) {
	testenv.RequireContainer(t)

	// The adapters' TargetRoot is paths.HomeDir(paths.OSEnv{}) (see
	// registryFactory); resolve it the same way so the user-scope containment
	// assertion knows the expected root.
	userHome := paths.HomeDir(paths.OSEnv{})

	// Malicious names — mirror internal/source/writer_test.go's degenerate and
	// control/deceptive-rune tables. A raw rune would smuggle a terminal escape
	// into this file, so the two rune cases use explicit \u escapes.
	malicious := []struct {
		label string
		name  string
	}{
		{"parent-traversal", "../../../tmp/x"},
		{"slash", "a/b"},
		{"backslash", `a\b`},
		{"bare-dot", "."},
		{"whitespace", "  "},
		{"absolute", "/etc/passwd"},
		{"bidi-override", "name\u202egnp"}, // RLO — Trojan-Source reorder
		{"esc-recolor", "good\x1b[31m"},    // ESC/CSI — terminal recolor
	}

	scopes := []struct {
		label   string
		scope   adapter.Scope
		project string
	}{
		{"user", adapter.ScopeUser, ""},
		{"project", adapter.ScopeProject, t.TempDir()},
	}

	reg := registryFactory()

	sawControlOp := false

	for _, name := range reg.Names() {
		for _, sc := range scopes {
			// Malicious: every traversal-bearing name is refused for this adapter.
			for _, m := range malicious {
				t.Run(name+"/"+m.label+"/"+sc.label, func(t *testing.T) {
					model := traversalModel(m.name, sc.scope == adapter.ScopeProject)
					_, err := render.Plan(secrets.ForRender(model), reg, []string{name}, sc.scope, sc.project, nil, userHome)
					if err == nil {
						t.Fatalf("Plan(%s, name=%q, scope=%s) = nil error; want the plan refused (write-anywhere on apply)",
							name, m.name, sc.scope)
					}
				})
			}

			// Control: a legitimate name still renders, and every emitted path
			// stays under the destination root (user → HOME; project → projDir).
			t.Run(name+"/legitimate/"+sc.label, func(t *testing.T) {
				model := traversalModel("review", sc.scope == adapter.ScopeProject)
				plan, err := render.Plan(secrets.ForRender(model), reg, []string{name}, sc.scope, sc.project, nil, userHome)
				if err != nil {
					t.Fatalf("Plan(%s, legitimate, scope=%s) = %v; want nil (happy path unchanged)", name, sc.scope, err)
				}
				for _, op := range plan.PerAgent[name].Ops {
					if op.Action != adapter.ActionWrite {
						continue
					}
					sawControlOp = true
					// The guard's positive property: no emitted write escapes.
					// user scope roots at HOME; project scope roots at projDir,
					// but a scope-aware adapter may still emit a user-level merge
					// file at project scope, so accept either — the point is that
					// nothing escapes to an arbitrary location like /tmp or /etc.
					contained := underRoot(op.Path, userHome) ||
						(sc.project != "" && underRoot(op.Path, sc.project))
					if !contained {
						t.Errorf("%s: FileOp path %q escapes the destination root (HOME=%q, project=%q)",
							name, op.Path, userHome, sc.project)
					}
				}
			})
		}
	}

	if !sawControlOp {
		t.Fatal("no adapter produced a FileOp for the legitimate control name — the guard's happy path is untested (vacuous)")
	}
}

// traversalModel builds a canonical whose subagent, command, and skill all carry
// the same Name — each of which an adapter joins into a destination filename. At
// project scope it duplicates the set into the Project overlay (the trick the
// skip-kind guard uses) so the project-scope render path, which renders from
// Project, exercises the guard too.
func traversalModel(name string, projectScope bool) source.Canonical {
	c := source.Canonical{
		Skills:    []source.Skill{{Name: name, Frontmatter: map[string]any{"name": name}, Body: "body\n"}},
		Subagents: []source.Subagent{{Name: name, Frontmatter: map[string]any{"description": "Review"}, Body: "body\n"}},
		Commands:  []source.Command{{Name: name, Frontmatter: map[string]any{"description": "Summarize"}, Body: "body\n"}},
	}
	if projectScope {
		proj := c
		proj.Project = nil
		withProject := c
		withProject.Project = &proj
		return withProject
	}
	return c
}

// underRoot reports whether p is root or lives beneath it (no ".." escape).
func underRoot(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
