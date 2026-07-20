package generic

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// TestVersionRoots_AllSpecs pins the exact user-scope version roots for EVERY
// breadth-tier spec, so a wrong `versionRootOf` mapping (too broad, too deep, or
// uncovered) for any current or future spec fails loudly rather than shipping
// silently. Each entry lists the dirs (relative to the target root) the spec must
// version at user scope; a project-scope-only agent maps to the empty set.
func TestVersionRoots_AllSpecs(t *testing.T) {
	const root = "/home/u"
	want := map[string][]string{
		"qwen":        {".qwen"},
		"warp":        {".warp", ".agents/skills"},
		"junie":       {".junie"},
		"kiro":        {".kiro"},
		"kilocode":    {".kilo"},
		"amazonq":     {".aws/amazonq"},
		"factory":     {".factory"},
		"pi":          {".pi/agent", ".agents/skills"},
		"zed":         {".config/zed", ".agents/skills"},
		"firebase":    {},
		"copilot":     {".copilot"},
		"copilot-cli": {".copilot", ".agents/skills"},
		"crush":       {".config/crush"},
		"amp":         {".config/amp", ".config/agents"},
		"antigravity": {".gemini/config"},
		"goose":       {".agents/skills"},
		"jules":       {},
		"openhands":   {},
		"trae":        {},
		"jetbrains":   {},
		"augmentcode": {".augment", ".agents/skills"},
		"mistral":     {".vibe"},
	}

	specs := Specs()
	seen := map[string]bool{}
	for _, spec := range specs {
		seen[spec.Name] = true
		exp, ok := want[spec.Name]
		if !ok {
			t.Errorf("spec %q has no expected-roots row; add one (forces deliberate review of new specs)", spec.Name)
			continue
		}
		a := New(spec, Options{TargetRoot: root})

		// Exact set (order-independent).
		got := a.VersionRoots(adapter.ScopeUser, "")
		var wantAbs []string
		for _, rel := range exp {
			wantAbs = append(wantAbs, filepath.Join(root, filepath.FromSlash(rel)))
		}
		if !sameSet(got, wantAbs) {
			t.Errorf("%s.VersionRoots(user) = %v, want %v", spec.Name, got, wantAbs)
		}

		// Invariant: every user-scope target the spec declares is under exactly one
		// returned root, and no root is $HOME or escapes it.
		for _, target := range []string{spec.Memory.User, spec.MCP.User, spec.Skills.User} {
			if target == "" {
				continue
			}
			abs := filepath.Join(root, filepath.FromSlash(target))
			n := 0
			for _, r := range got {
				if r == root {
					t.Errorf("%s: root %q is $HOME — must never init a repo at $HOME", spec.Name, r)
				}
				if under(abs, r) {
					n++
					// Root-tightness (issue #154): the root must not be OVER-BROAD. The
					// deepest a target sits below its (correct) root across all current
					// specs is 2 segments — a file in a subdir of a single-product base
					// (junie's .junie/mcp/mcp.json, augmentcode's .augment/rules/...).
					// A target 3+ segments deep signals a collapsed, over-broad root:
					// e.g. a future spec under an unlisted multi-product base
					// (.local/share/foo/mcp.json wrongly collapsing to ~/.local) would
					// fail here.
					if rel, err := filepath.Rel(r, abs); err == nil && rel != "." {
						if depth := len(strings.Split(filepath.ToSlash(rel), "/")); depth > 2 {
							t.Errorf("%s: version root %q is over-broad for target %q (%d segments deep, want <= 2)", spec.Name, r, target, depth)
						}
					}
				}
			}
			if n != 1 {
				t.Errorf("%s: user target %q is under %d roots, want exactly 1 (roots=%v)", spec.Name, target, n, got)
			}
		}

		// Project scope abstains.
		if p := a.VersionRoots(adapter.ScopeProject, "/proj"); len(p) != 0 {
			t.Errorf("%s.VersionRoots(project) = %v, want nil", spec.Name, p)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("expected-roots row %q has no matching spec (stale row?)", name)
		}
	}
}

func TestVersionRootOf_RejectsEscapes(t *testing.T) {
	const root = "/home/u"
	for _, bad := range []string{"../.ssh/id", "../../etc/passwd", "AGENTS.md", "", "."} {
		if got := versionRootOf(root, bad); got != "" {
			t.Errorf("versionRootOf(%q) = %q, want \"\" (must not escape $HOME or version a bare file)", bad, got)
		}
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

func under(child, parent string) bool {
	if child == parent {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// TestVersionRootOf_Tightness pins that versionRootOf returns a root at most one
// segment shallower than the target for representative listed bases, and documents
// (in code) what an UNLISTED multi-product base currently produces, so the
// over-broad-collapse hazard stays visible (issue #154).
func TestVersionRootOf_Tightness(t *testing.T) {
	const root = "/home/u"
	cases := []struct {
		name, target, wantRoot string
	}{
		{"config-base", ".config/zed/AGENTS.md", ".config/zed"},
		{"aws-base", ".aws/amazonq/mcp.json", ".aws/amazonq"},
		{"agents-skills", ".agents/skills", ".agents/skills"},
		{"single-dotted", ".qwen/settings.json", ".qwen"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := versionRootOf(root, c.target)
			want := filepath.Join(root, filepath.FromSlash(c.wantRoot))
			if got != want {
				t.Fatalf("versionRootOf(%q) = %q, want %q", c.target, got, want)
			}
			rel, err := filepath.Rel(got, filepath.Join(root, filepath.FromSlash(c.target)))
			if err != nil {
				t.Fatal(err)
			}
			if rel != "." && len(strings.Split(filepath.ToSlash(rel), "/")) > 1 {
				t.Errorf("root %q is over-broad for target %q (rel=%q, want the target <= 1 segment below)", got, c.target, rel)
			}
		})
	}
	// KNOWN-BROAD: a target under an UNLISTED multi-product base collapses to a
	// single over-broad segment today. Pinned here so the hazard is visible; if such
	// a spec is ever added, versionRootOf's allowlist (and this row) must be updated.
	if broad := versionRootOf(root, ".local/share/foo/mcp.json"); broad != filepath.Join(root, ".local") {
		t.Errorf("expected the documented over-broad collapse to ~/.local for an unlisted base; got %q", broad)
	}
}
