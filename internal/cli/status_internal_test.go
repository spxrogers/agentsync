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
// TestRenderClassLegend pins --legend's output against a HARDCODED, literal
// expectation per class — deliberately NOT read from classMeaning/classLegend
// at test time. A round-2 version of this test compared the rendered line
// against classMeaning[cls]/classLegend[cls] dynamically; that is a tautology
// against the exact mutation it claims to guard (swap classLegend's "drift"
// and "conflict" action strings, or classMeaning's "orphan"/"orphan-drifted"
// meanings, in a throwaway clone): renderClassLegend reads the SAME map the
// assertion reads, so both shift together and the swap is invisible. Verified
// by mutation: the literal-oracle version below fails on that swap; the
// dynamic-oracle version did not.
func TestRenderClassLegend(t *testing.T) {
	var buf bytes.Buffer
	p := &ui.Printer{Out: &buf}
	renderClassLegend(p)
	out := buf.String()
	lines := strings.Split(out, "\n")
	// findLine returns the one line whose row label is this exact class (a
	// prefix match on "<glyph> <cls>" after the leading indent, so "orphan"
	// can't match "orphan-drifted"'s line by substring).
	findLine := func(t *testing.T, cls string) string {
		t.Helper()
		for _, l := range lines {
			trimmed := strings.TrimLeft(l, " ")
			// The glyph is one rune; the class word starts right after it and
			// a space, and is itself followed by whitespace padding.
			if i := strings.IndexByte(trimmed, ' '); i >= 0 {
				rest := strings.TrimLeft(trimmed[i+1:], " ")
				if rest == cls || strings.HasPrefix(rest, cls+" ") {
					return l
				}
			}
		}
		t.Fatalf("no --legend line found for class %q; got:\n%s", cls, out)
		return ""
	}
	tests := []struct{ cls, meaning, action string }{
		{"clean", "all agree", "apply does nothing"},
		{"pending", "you changed the source", "will be updated to match source"},
		{"drift", "the destination was edited", "will be overwritten (use reconcile to keep the dest edit)"},
		{"converged", "landed on the same value", "nothing left to reconcile"},
		{"conflict", "changed to different values", "will be overwritten (use reconcile to merge the dest edit)"},
		{"new", "brand-new item", "will be created"},
		{"foreign-collision", "agentsync never wrote", "will be backed up and overwritten"},
		{"orphan", "removed from source and untouched", "will be deleted"},
		{"orphan-drifted", "the destination was also edited", "if apply still reclaims it"},
	}
	for _, tc := range tests {
		line := findLine(t, tc.cls)
		meaningIdx := strings.Index(line, tc.meaning)
		actionIdx := strings.Index(line, tc.action)
		if meaningIdx < 0 {
			t.Errorf("expected %q's own line to contain %q; got:%s", tc.cls, tc.meaning, line)
		}
		if actionIdx < 0 {
			t.Errorf("expected %q's own line to contain %q; got:%s", tc.cls, tc.action, line)
		}
		// A whole-line Contains check can't tell "meaning — action" from
		// "action — meaning": both substrings are still present either way.
		// Pin the ORDER too, or a swapped composition passes silently.
		if meaningIdx >= 0 && actionIdx >= 0 && meaningIdx > actionIdx {
			t.Errorf(`expected %q's line to read "meaning — action", not the reverse; got:%s`, tc.cls, line)
		}
	}
	// The per-class checks above only prove each class's OWN line exists
	// somewhere and contains the right substrings in the right order — they
	// say nothing about the full row LIST: its length, its order, or whether
	// an extra/junk row snuck in. Mutation-verified gaps this closes: iterating
	// an unordered map instead of classOrder (rows print in random order every
	// run — classOrder's own doc comment claims it "doubles as this table's
	// row order", previously unverified by any test); a junk entry appended to
	// classOrder (prints an extra row with an empty meaning/action, unnoticed
	// since nothing asserts NO extra rows exist).
	var gotRows []string
	for _, l := range lines {
		if !strings.HasPrefix(l, "  ") {
			continue // the "Drift classification statuses" header, or a blank line
		}
		trimmed := strings.TrimLeft(l, " ")
		i := strings.IndexByte(trimmed, ' ')
		if i < 0 {
			continue
		}
		rest := strings.TrimLeft(trimmed[i+1:], " ")
		j := strings.IndexByte(rest, ' ')
		if j < 0 {
			continue
		}
		gotRows = append(gotRows, rest[:j])
	}
	if len(gotRows) != len(classOrder) {
		t.Fatalf("expected exactly %d legend rows (one per classOrder entry), got %d: %v", len(classOrder), len(gotRows), gotRows)
	}
	for i, cls := range classOrder {
		if gotRows[i] != cls {
			t.Errorf("legend row %d = %q, want %q — row order must match classOrder", i, gotRows[i], cls)
		}
	}
	// The two pairs most likely to get their text swapped by accident (they
	// share almost identical wording otherwise) must not cross-contaminate.
	driftLine := findLine(t, "drift")
	if strings.Contains(driftLine, "merge the dest edit") {
		t.Errorf("drift's line must say \"keep\", not \"merge\" (that's conflict's line); got:%s", driftLine)
	}
	conflictLine := findLine(t, "conflict")
	if strings.Contains(conflictLine, "keep the dest edit") {
		t.Errorf("conflict's line must say \"merge\", not \"keep\" (that's drift's line); got:%s", conflictLine)
	}
	orphanLine := findLine(t, "orphan")
	if strings.Contains(orphanLine, "also edited") {
		t.Errorf("orphan's line must not describe orphan-drifted's meaning; got:%s", orphanLine)
	}
	// "will be deleted" is a strict PREFIX of orphan-drifted's action text, so
	// a mutation widening orphan's own action to orphan-drifted's full string
	// would still satisfy the "will be deleted" containment check above and
	// pass silently — this is the asymmetric half of the swap-guard that
	// needs its own negative assertion (mutation-verified: without this check,
	// orphan's action silently absorbing orphan-drifted's text goes uncaught).
	if strings.Contains(orphanLine, "if apply still reclaims it") {
		t.Errorf("orphan's line must not describe orphan-drifted's action (orphan gets no backup); got:%s", orphanLine)
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
	visited := 0
	for c := drift.Clean; c.String() != "unknown"; c++ {
		visited++
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
	// Belt-and-suspenders: the loop's own bound (c.String() != "unknown")
	// stops silently if a class is ever appended WITHOUT a String() case —
	// classifier.go's default already falls back to "unknown" for anything
	// past OrphanDrifted, so that specific mistake surfaces loudly elsewhere
	// (every class after it prints as "unknown"), but pinning the expected
	// count here means a mismatch fails HERE too, next to the tables it's
	// meant to protect, rather than only in some other test's output.
	if visited != 9 {
		t.Errorf("expected exactly 9 drift classes, visited %d — drift.Class and this test's assumptions have diverged", visited)
	}
	// containsString above only proves presence, not uniqueness — a class
	// accidentally duplicated in classOrder/classSeverity would still "contain"
	// every name exactly once each and pass, while printing that class twice in
	// both `status`'s summary footer and `status --legend`.
	for name, list := range map[string][]string{"classOrder": classOrder, "classSeverity": classSeverity} {
		seen := map[string]bool{}
		for _, cls := range list {
			if seen[cls] {
				t.Errorf("%s contains %q more than once", name, cls)
			}
			seen[cls] = true
		}
		// The loop above only proves the enum's 9 classes are all PRESENT in
		// classOrder/classSeverity (⊇) and that none repeats — neither rules
		// out an extra, never-classified entry alongside the real 9 (⊆). A
		// junk entry appended to classOrder prints an extra row with an empty
		// meaning/action in `status --legend` (renderClassLegend has no way to
		// know it isn't real), and TestRenderClassLegend's own row-order check
		// can't catch it either: it reads its expected row list from this SAME
		// classOrder, so the two shift together — this length bound is the
		// only place that pins classOrder to the CLOSED nine-class universe.
		if len(list) != 9 {
			t.Errorf("%s has %d entries, want exactly 9 (the closed drift.Class universe)", name, len(list))
		}
	}
}
