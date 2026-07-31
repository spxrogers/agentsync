package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/ui"
)

// abs builds an absolute, OS-correct path from slash-separated segments so these
// table tests read the same on any platform.
func abs(segs ...string) string {
	return string(filepath.Separator) + filepath.Join(segs...)
}

// TestSkillRoots_AnchorsOnSkillMD is the unit guard for the adversarial finding:
// grouping must key off an actual `…/skills/<name>/SKILL.md`, never a bare
// `skills` path SEGMENT — otherwise an ancestor directory literally named
// `skills` (e.g. $HOME=/home/skills/user) would sweep unrelated files into a
// bogus group and hide their drift.
func TestSkillRoots_AnchorsOnSkillMD(t *testing.T) {
	greet := abs("u", ".claude", "skills", "greet")
	build := abs("home", "skills", "user", ".claude", "skills", "build")
	tests := []struct {
		name  string
		items []statusItem
		want  map[string]bool
	}{
		{
			name: "normal skill dir is a root",
			items: []statusItem{
				{Path: filepath.Join(greet, "SKILL.md")},
				{Path: filepath.Join(greet, "references", "notes.md")},
			},
			want: map[string]bool{greet: true},
		},
		{
			name: "ancestor named skills does not create a root for non-skill files",
			items: []statusItem{
				{Path: abs("home", "skills", "user", ".claude", "CLAUDE.md")},
				{Path: abs("home", "skills", "user", ".claude", "agents", "x.md")},
			},
			want: map[string]bool{},
		},
		{
			name: "real skill under a skills-named ancestor is still detected",
			items: []statusItem{
				{Path: abs("home", "skills", "user", ".claude", "CLAUDE.md")},
				{Path: filepath.Join(build, "SKILL.md")},
				{Path: filepath.Join(build, "references", "x.md")},
			},
			want: map[string]bool{build: true},
		},
		{
			name: "nested references/SKILL.md is NOT a root (grandparent is the skill name, not skills)",
			items: []statusItem{
				{Path: filepath.Join(greet, "SKILL.md")},
				{Path: filepath.Join(greet, "references", "SKILL.md")},
			},
			want: map[string]bool{greet: true},
		},
		{
			// A skill bundling its own `skills/<sub>/SKILL.md` must NOT spawn a
			// second inner root (which the inner SKILL.md would match alongside
			// the outer one — ambiguous under map iteration). Only the outermost
			// root survives, so the whole skill collapses onto one row.
			name: "a bundled skills/<sub>/SKILL.md does not create a nested root",
			items: []statusItem{
				{Path: filepath.Join(greet, "SKILL.md")},
				{Path: filepath.Join(greet, "skills", "sub", "SKILL.md")},
				{Path: filepath.Join(greet, "references", "x.md")},
			},
			want: map[string]bool{greet: true},
		},
		{
			// Sibling skills whose names share a prefix are INDEPENDENT roots —
			// the nesting filter must not drop `greet` just because `greetzilla`
			// HasPrefix("…/greet…"). Guards hasAncestorIn's `+sep` boundary at the
			// skillRoots level (skillRootOf's boundary is covered separately).
			name: "sibling skills with a shared name prefix are both roots",
			items: []statusItem{
				{Path: filepath.Join(greet, "SKILL.md")},
				{Path: filepath.Join(abs("u", ".claude", "skills", "greetzilla"), "SKILL.md")},
			},
			want: map[string]bool{greet: true, abs("u", ".claude", "skills", "greetzilla"): true},
		},
		{
			// A skill literally named `skills` still anchors correctly: the dir is
			// `…/skills/skills` whose grandparent base is `skills`.
			name: "a skill named skills is a valid root",
			items: []statusItem{
				{Path: abs("u", ".claude", "skills", "skills", "SKILL.md")},
			},
			want: map[string]bool{abs("u", ".claude", "skills", "skills"): true},
		},
		{
			name: "a key-merge item that happens to end in SKILL.md is ignored",
			items: []statusItem{
				{Path: filepath.Join(greet, "SKILL.md"), Pointer: "/mcpServers/x"},
			},
			want: map[string]bool{},
		},
		{
			name:  "no SKILL.md anywhere yields no roots (orphan bundles list individually)",
			items: []statusItem{{Path: filepath.Join(greet, "references", "notes.md")}},
			want:  map[string]bool{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := skillRoots(tc.items)
			if len(got) != len(tc.want) {
				t.Fatalf("roots = %v, want %v", got, tc.want)
			}
			for r := range tc.want {
				if !got[r] {
					t.Errorf("missing root %q in %v", r, got)
				}
			}
		})
	}
}

func TestSkillRootOf(t *testing.T) {
	greet := abs("u", ".claude", "skills", "greet")
	other := abs("u", ".claude", "skills", "other")
	roots := map[string]bool{greet: true}
	tests := []struct {
		path string
		want string
	}{
		{filepath.Join(greet, "SKILL.md"), greet},
		{filepath.Join(greet, "references", "notes.md"), greet},
		{filepath.Join(greet, "skills", "sub", "SKILL.md"), greet}, // nested bundle maps to the outer root
		{filepath.Join(other, "SKILL.md"), ""},                     // not a known root
		{abs("u", ".claude", "skills", "greetzilla"), ""},          // prefix-but-not-a-child
	}
	for _, tc := range tests {
		if got := skillRootOf(tc.path, roots); got != tc.want {
			t.Errorf("skillRootOf(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestMostSevereClass(t *testing.T) {
	tests := []struct {
		name  string
		items []statusItem
		want  string
	}{
		{"mixed picks the most severe", []statusItem{{Class: "clean"}, {Class: "drift"}, {Class: "new"}}, "drift"},
		{"conflict outranks orphan and pending", []statusItem{{Class: "orphan"}, {Class: "conflict"}, {Class: "pending"}}, "conflict"},
		{"uniform clean stays clean", []statusItem{{Class: "clean"}, {Class: "clean"}}, "clean"},
		{"unknown class falls back to first item", []statusItem{{Class: "weird"}}, "weird"},
		{"empty yields empty", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mostSevereClass(tc.items); got != tc.want {
				t.Errorf("mostSevereClass = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSkillSummary(t *testing.T) {
	skillMD := statusItem{Path: abs("u", "skills", "greet", "SKILL.md"), Class: "clean"}
	ref := func(name, cls string) statusItem {
		return statusItem{Path: abs("u", "skills", "greet", "references", name), Class: cls}
	}
	tests := []struct {
		name  string
		items []statusItem
		want  string
	}{
		{"uniform with SKILL.md", []statusItem{skillMD, ref("a.md", "clean")}, "(SKILL.md + 1 file)"},
		{"plural files", []statusItem{skillMD, ref("a.md", "clean"), ref("b.md", "clean")}, "(SKILL.md + 2 files)"},
		{
			"mixed appends a class breakdown in classOrder",
			[]statusItem{{Path: skillMD.Path, Class: "drift"}, ref("a.md", "clean")},
			"(SKILL.md + 1 file; 1 clean, 1 drift)",
		},
		{
			"no SKILL.md uses bare file count",
			[]statusItem{ref("a.md", "clean"), ref("b.md", "clean")},
			"(2 files)",
		},
		{
			// converged folds into clean (displayClass), so a skill mixing the two
			// must read as uniformly clean — no breakdown, and never the word
			// "converged" anywhere in the summary.
			"converged folds into clean with no breakdown",
			[]statusItem{{Path: skillMD.Path, Class: "converged"}, ref("a.md", "clean")},
			"(SKILL.md + 1 file)",
		},
		{
			"converged still counts toward the clean bucket in a real breakdown",
			[]statusItem{{Path: skillMD.Path, Class: "converged"}, ref("a.md", "drift")},
			"(SKILL.md + 1 file; 1 clean, 1 drift)",
		},
		{
			"uniform converged reads identically to uniform clean",
			[]statusItem{{Path: skillMD.Path, Class: "converged"}, ref("a.md", "converged")},
			"(SKILL.md + 1 file)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := skillSummary(tc.items); got != tc.want {
				t.Errorf("skillSummary = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDisplayClass locks in the one fold the formatted dashboard applies:
// converged reads as clean, everything else (including classes outside the
// known nine, defensively) passes through unchanged.
func TestDisplayClass(t *testing.T) {
	tests := []struct{ cls, want string }{
		{"clean", "clean"},
		{"converged", "clean"},
		{"pending", "pending"},
		{"drift", "drift"},
		{"conflict", "conflict"},
		{"new", "new"},
		{"foreign-collision", "foreign-collision"},
		{"orphan", "orphan"},
		{"orphan-drifted", "orphan-drifted"},
		{"weird", "weird"},
	}
	for _, tc := range tests {
		if got := displayClass(tc.cls); got != tc.want {
			t.Errorf("displayClass(%q) = %q, want %q", tc.cls, got, tc.want)
		}
	}
}

// TestRenderStatusText_ConvergedFoldsIntoClean is the render-layer guard for
// the UI decision: converged is a real, distinct internal classification
// (see internal/drift), but the formatted `status` dashboard shows it as
// clean — merged into the same summary count, the same per-item word, and
// never named "converged" anywhere a human reads it. `status --json` (built
// straight from statusModel, untouched by this rendering path) keeps the
// distinction for machine consumers.
func TestRenderStatusText_ConvergedFoldsIntoClean(t *testing.T) {
	model := statusModel{
		Agents: []statusAgent{{
			Agent: "claude",
			Items: []statusItem{
				{Path: "/u/.claude/CLAUDE.md", Class: "clean"},
				{Path: "/u/.claude/settings.json", Class: "converged"},
			},
		}},
		Summary: map[string]int{"clean": 1, "converged": 1},
	}
	var buf bytes.Buffer
	p := &ui.Printer{Out: &buf}
	renderStatusText(p, model, false)
	out := buf.String()
	if strings.Contains(out, "converged") {
		t.Errorf("formatted status must never print the word \"converged\"; got:\n%s", out)
	}
	if !strings.Contains(out, "2 clean") {
		t.Errorf("expected the converged item folded into the clean tally (\"2 clean\"); got:\n%s", out)
	}
	if got := strings.Count(out, "clean"); got != 3 {
		// One per item row (2) + one in the summary footer segment = 3.
		t.Errorf("expected exactly 3 occurrences of \"clean\" (two item rows + one summary segment); got %d in:\n%s", got, out)
	}
}

// TestRenderClassLegend covers `status --legend`: the standalone glossary must
// list all nine drift classes, including clean and converged spelled out on
// their own (unlike the inline per-run legend, which omits both).
func TestRenderClassLegend(t *testing.T) {
	var buf bytes.Buffer
	p := &ui.Printer{Out: &buf}
	renderClassLegend(p)
	out := buf.String()
	for _, cls := range []string{
		"clean", "pending", "drift", "converged", "conflict",
		"new", "foreign-collision", "orphan", "orphan-drifted",
	} {
		if !strings.Contains(out, cls) {
			t.Errorf("expected --legend to mention class %q; got:\n%s", cls, out)
		}
	}
	// The class WORD alone survives a broken "meaning — action" composition
	// (it's printed unconditionally as part of the row label), so pin the
	// actual line content too: every class's classMeaning text must appear
	// verbatim, and every class classLegend covers must reuse ITS action text
	// verbatim — the exact coupling round 2 introduced to stop the inline
	// legend and --legend from silently disagreeing again.
	for cls, meaning := range classMeaning {
		if !strings.Contains(out, meaning) {
			t.Errorf("expected --legend's %q line to contain its meaning %q; got:\n%s", cls, meaning, out)
		}
	}
	for cls, action := range classLegend {
		if !strings.Contains(out, action) {
			t.Errorf("expected --legend's %q line to reuse classLegend's action text %q verbatim; got:\n%s", cls, action, out)
		}
	}
}

// TestRenderStatusText_SkillGroupConvergedFoldsIntoClean is the collapsed-
// skill-group counterpart to TestRenderStatusText_ConvergedFoldsIntoClean.
// mostSevereClass ranks converged above clean (classSeverity), so a skill
// directory whose most-severe member is converged is exactly the path where a
// forgotten displayClass call would leak the raw word into the group's
// headline — confirmed by mutation: reverting renderSkillGroup's
// `displayClass(cls)` back to the bare `cls` it replaced leaves the rest of
// this package's tests green.
func TestRenderStatusText_SkillGroupConvergedFoldsIntoClean(t *testing.T) {
	root := abs("u", ".claude", "skills", "greet")
	model := statusModel{
		Agents: []statusAgent{{
			Agent: "claude",
			Items: []statusItem{
				{Path: filepath.Join(root, "SKILL.md"), Class: "converged"},
				{Path: filepath.Join(root, "references", "notes.md"), Class: "clean"},
			},
		}},
		Summary: map[string]int{"clean": 1, "converged": 1},
	}
	var buf bytes.Buffer
	p := &ui.Printer{Out: &buf}
	renderStatusText(p, model, false)
	out := buf.String()
	if strings.Contains(out, "converged") {
		t.Errorf("collapsed skill-group headline must never print \"converged\"; got:\n%s", out)
	}
	if !strings.Contains(out, "clean") {
		t.Errorf("expected the collapsed skill-group headline to read clean; got:\n%s", out)
	}
}

// TestClassTablesCoverAllDriftClasses is a reflective guard over the hand-
// maintained per-class tables in this file (classOrder, classSeverity,
// classMeaning, and — for every class but clean/converged, which it
// intentionally omits — classLegend). drift.Class is a closed enum whose
// String() falls back to "unknown" past its last defined value (see
// classifier.go); nothing here would fail if a tenth value were added and
// silently left out of one of these tables, so this test walks the enum
// itself by that "unknown" sentinel — NOT by drift.OrphanDrifted, which
// would only catch a class inserted mid-list, not one appended after it
// (the way a new class is actually added) — rather than trusting any of the
// tables' own key sets, per the "models must stay faithful to their
// artifacts" rule in CLAUDE.md.
func TestClassTablesCoverAllDriftClasses(t *testing.T) {
	for c := drift.Clean; c.String() != "unknown"; c++ {
		name := c.String()
		if !containsString(classOrder, name) {
			t.Errorf("classOrder is missing drift class %q", name)
		}
		if !containsString(classSeverity, name) {
			t.Errorf("classSeverity is missing drift class %q", name)
		}
		if _, ok := classMeaning[name]; !ok {
			t.Errorf("classMeaning is missing drift class %q", name)
		}
		if name == "clean" || name == "converged" {
			continue // classLegend deliberately omits these two
		}
		if _, ok := classLegend[name]; !ok {
			t.Errorf("classLegend is missing drift class %q", name)
		}
	}
}
