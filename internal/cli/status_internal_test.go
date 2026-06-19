package cli

import (
	"path/filepath"
	"testing"
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := skillSummary(tc.items); got != tc.want {
				t.Errorf("skillSummary = %q, want %q", got, tc.want)
			}
		})
	}
}
