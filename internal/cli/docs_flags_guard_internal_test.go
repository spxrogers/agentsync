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
// it, in both directions — an undocumented flag and a documented-but-absent flag
// are both failures.
func TestDocsListAgentsFlagWhereItExists(t *testing.T) {
	repoRoot := repoRootFromCaller(t)

	// Commands that really accept --agents, by full path.
	actual := map[string]bool{}
	walkCommands(NewRoot(), func(path string, c *cobra.Command) {
		if c.Flags().Lookup("agents") != nil {
			actual[path] = true
		}
	})
	if len(actual) == 0 {
		t.Fatal("no command declares --agents; this guard would vacuously pass")
	}

	for _, rel := range docCommandTables {
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		for _, row := range tableRows(string(data)) {
			cmd, flags, ok := rowCommandAndFlags(row)
			if !ok || !actual[cmd] {
				continue
			}
			if !strings.Contains(flags, "--agents") {
				t.Errorf("%s: the `%s` row does not list `--agents`, but the command accepts it "+
					"(#200 F10 gave apply/status/diff/reconcile/revert one shared --agents grammar).\n    row: %s",
					rel, cmd, strings.TrimSpace(row))
			}
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

// rowCommandAndFlags pulls the leading command name and the trailing flags cell
// out of a table row. It reads only the FIRST backticked token of column one, so
// a row like "| `apply` | … |" yields "apply"; rows whose first cell is not a
// simple command (alternation like `agent add|remove`, prose) yield ok=false and
// are skipped rather than guessed at.
func rowCommandAndFlags(row string) (cmd, flags string, ok bool) {
	cells := strings.Split(strings.Trim(row, "|"), "|")
	if len(cells) < 2 {
		return "", "", false
	}
	first := strings.TrimSpace(cells[0])
	if !strings.HasPrefix(first, "`") {
		return "", "", false
	}
	end := strings.Index(first[1:], "`")
	if end < 0 {
		return "", "", false
	}
	name := first[1 : 1+end]
	// Only single-word commands (optionally with a `<arg>` after) are matched;
	// alternation rows are ambiguous about which verb carries which flag.
	if strings.ContainsAny(name, "|\\") {
		return "", "", false
	}
	name = strings.TrimSpace(strings.Split(name, " ")[0])
	if name == "" {
		return "", "", false
	}
	return name, cells[len(cells)-1], true
}
