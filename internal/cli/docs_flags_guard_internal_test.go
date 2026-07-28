package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// docCommandTables are the two hand-maintained "at a glance" command tables.
// They are the first thing a user reads and the last thing anyone updates.
var docCommandTables = []string{
	"docs/user-guide.md",
	"website/src/content/docs/reference/cli.mdx",
}

// TestDocsListAgentsFlagWhereItExists closes a doc-drift class that CLAUDE.md
// calls out by name: "a doc claim is not self-certifying."
//
// #200 F10 gave `apply`, `status`, `diff`, `reconcile` and `revert` ONE --agents
// grammar — the whole point being that the filter you use in the daily loop no
// longer vanishes at the step that writes. But the flag columns in both command
// tables were only updated for the two commands that already had it, so the
// docs asserted the opposite of the feature for three of the five.
//
// Nothing about that fails a build: the flag works, the table is prose. So this
// test derives the truth from the command tree and requires the tables to match
// it, in BOTH directions — a command that accepts `--agents` whose row omits it,
// and a row that advertises `--agents` on a command that would reject it. Rows
// that do not name a single concrete command (prose, or an alternation like
// `agent add|remove`) are skipped rather than guessed at.
func TestDocsListAgentsFlagWhereItExists(t *testing.T) {
	repoRoot := repoRootFromCaller(t)

	// Commands that really accept --agents, and the full set of command paths
	// (so a doc row naming a real command can be checked in BOTH directions
	// without mistaking a prose row for a command).
	actual := map[string]bool{}
	known := map[string]struct{}{}
	walkCommands(NewRoot(), func(path string, c *cobra.Command) {
		known[path] = struct{}{}
		if c.Flags().Lookup("agents") != nil {
			actual[path] = true
		}
	})
	if len(actual) == 0 {
		t.Fatal("no command declares --agents; this guard would vacuously pass")
	}

	var covered []string
	for _, rel := range docCommandTables {
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		for _, row := range tableRows(string(data)) {
			cmds, flags, ok := rowCommandAndFlags(row)
			if !ok {
				continue
			}
			// Keep only the members that are real commands; a row may mix a
			// documented verb with prose.
			var real []string
			for _, c := range cmds {
				if _, isCommand := known[c]; isCommand {
					real = append(real, c)
				}
			}
			if len(real) == 0 {
				continue
			}
			covered = append(covered, real...)

			// One row can document several commands (an alternation row), and
			// the flags cell describes the row as a whole — so the row must
			// list --agents when ANY member takes it, and must not when none
			// does.
			var takers []string
			for _, c := range real {
				if actual[c] {
					takers = append(takers, c)
				}
			}
			documented := strings.Contains(flags, "--agents")
			switch {
			case len(takers) > 0 && !documented:
				t.Errorf("%s: the `%s` row does not list `--agents`, but %s accepts it "+
					"(#200 F10 gave apply/status/diff/reconcile/revert one shared --agents grammar).\n    row: %s",
					rel, real[0], strings.Join(takers, ", "), strings.TrimSpace(row))
			case len(takers) == 0 && documented:
				// The other direction, which this guard previously CLAIMED to
				// check and did not: documenting a flag the command rejects
				// sends a user to an error.
				t.Errorf("%s: the `%s` row lists `--agents`, but no command it documents accepts it — "+
					"a reader following this doc gets an unknown-flag error.\n    row: %s",
					rel, real[0], strings.TrimSpace(row))
			}
		}
	}

	// A parser change that stops matching rows would make this guard pass while
	// checking nothing. Require every command that really takes --agents to have
	// been seen in at least one table.
	seen := map[string]bool{}
	for _, c := range covered {
		seen[c] = true
	}
	for cmd := range actual {
		if !seen[cmd] {
			t.Errorf("`%s` accepts --agents but appears in NEITHER command table — either document it, "+
				"or the row parser has stopped matching its row (which would silently vacate this guard)", cmd)
		}
	}
}

// tableRows returns the markdown table rows of src (lines starting with "|"),
// skipping header separators.
func tableRows(src string) []string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		l := strings.TrimSpace(line)
		if !strings.HasPrefix(l, "|") || strings.HasPrefix(l, "|---") || strings.HasPrefix(l, "| ---") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// rowCommandAndFlags pulls the command(s) a table row documents, plus its
// trailing flags cell.
//
// Two subtleties the first version got wrong, each of which silently dropped
// rows from the guard:
//
//   - Markdown escapes a literal pipe inside a cell as `\|`, so splitting the
//     row on "|" cuts THROUGH the first cell of an alternation row. Cells are
//     split on unescaped pipes only.
//   - Alternation rows ("mcp add|remove|list <name>") document several
//     commands at once. Returning ok=false for them skipped `mcp add` — one of
//     the six commands that actually take --agents — so dropping the flag from
//     that row was undetectable. They now expand to every member command.
//
// Rows whose first cell is not backticked (prose) still yield ok=false.
func rowCommandAndFlags(row string) (cmds []string, flags string, ok bool) {
	cells := splitTableCells(row)
	if len(cells) < 2 {
		return nil, "", false
	}
	first := strings.TrimSpace(cells[0])
	if !strings.HasPrefix(first, "`") {
		return nil, "", false
	}
	end := strings.Index(first[1:], "`")
	if end < 0 {
		return nil, "", false
	}
	spec := strings.TrimSpace(first[1 : 1+end])
	if spec == "" {
		return nil, "", false
	}
	flags = cells[len(cells)-1]

	// "mcp add|remove|list <name>" -> mcp add, mcp remove, mcp list
	// "apply"                      -> apply
	fields := strings.Fields(spec)
	verbs := strings.Split(fields[0], "|")
	if len(fields) > 1 {
		if alt := strings.Split(fields[1], "|"); len(alt) > 1 || len(verbs) == 1 {
			// second field carries the alternation: "mcp add|remove|list"
			for _, v := range alt {
				if v = strings.TrimSpace(v); v != "" && !strings.HasPrefix(v, "<") && !strings.HasPrefix(v, "[") {
					cmds = append(cmds, fields[0]+" "+v)
				}
			}
		}
	}
	if len(cmds) == 0 {
		for _, v := range verbs {
			if v = strings.TrimSpace(v); v != "" {
				cmds = append(cmds, v)
			}
		}
	}
	return cmds, flags, len(cmds) > 0
}

// splitTableCells splits a markdown table row on UNESCAPED pipes.
func splitTableCells(row string) []string {
	row = strings.Trim(strings.TrimSpace(row), "|")
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(row); i++ {
		if row[i] == '\\' && i+1 < len(row) && row[i+1] == '|' {
			cur.WriteByte('|') // escaped pipe: literal content, not a separator
			i++
			continue
		}
		if row[i] == '|' {
			cells = append(cells, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(row[i])
	}
	cells = append(cells, cur.String())
	return cells
}
