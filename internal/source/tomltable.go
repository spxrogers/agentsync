package source

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// SpliceTOMLTable replaces one TOML table's run in raw with block, preserving
// the bytes — comments, blank lines, key order, formatting — of every OTHER
// table. It is what lets `agent add/remove/enable/disable` and the git-backup
// mode setter rewrite ONE table of the user's agentsync.toml without
// re-marshalling the file and flattening everything they wrote in it.
//
// table is the bare table name ("agents"), not the bracketed header. A table's
// "run" starts at its header line and ends at the next line beginning with "[",
// so a trailing comment or blank line between the table's last key and the
// following header belongs to THIS table's run and is replaced along with it.
// Comment fidelity is therefore best-effort for lines ADJACENT to the spliced
// table; put a section's leading comment below its own header. What is NOT
// best-effort is that no data outside the table changes — and that is
// guaranteed by the caller's fail-closed re-parse (TableOutsideUnchanged), not
// by this line splice.
//
// When the table is absent, block is appended after a blank-line separator.
// When it is present, block goes where the FIRST matching header was and a
// blank line is kept before whatever section follows, so repeated edits do not
// collapse the file's spacing (the loop drops blank lines inside the run, so
// one separator is re-added each time — no accumulation).
//
// This is deliberately NOT a TOML parser. It cannot see that an "[agents]" line
// is the CONTENT of a multi-line string, and it does not distinguish `[x]` from
// an array-of-tables `[[x]]`. Both are handled the same way: by refusing to
// write. Every caller MUST re-parse the result and run TableOutsideUnchanged
// before persisting it — see the two callers in internal/cli.
func SpliceTOMLTable(raw, table, block string, opts SpliceOptions) string {
	header := "[" + table + "]"
	subPrefix := "[" + table + "."
	newLines := strings.Split(block, "\n")
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines)+len(newLines))
	insertAt := -1
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			// A new table header: ours iff it matches, and — when the caller
			// opted in — iff it is one of our sub-tables.
			inTable = trimmed == header ||
				(opts.IncludeSubtables && strings.HasPrefix(trimmed, subPrefix))
			if inTable {
				if insertAt < 0 {
					insertAt = len(out) // first matching section → reinsert here
				}
				continue // drop the old header
			}
		}
		if inTable {
			continue // drop lines inside the old table
		}
		out = append(out, line)
	}
	if insertAt < 0 {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, newLines...)
	} else {
		tail := append([]string(nil), out[insertAt:]...)
		out = append(out[:insertAt], newLines...)
		if len(tail) > 0 && strings.TrimSpace(tail[0]) != "" {
			out = append(out, "")
		}
		out = append(out, tail...)
	}
	return strings.Join(out, "\n")
}

// SpliceOptions selects which headers SpliceTOMLTable treats as the target
// table's own.
//
// IncludeSubtables is what tells `[agents]` apart from
// `[destination_directory_git_backup]`, and it is NOT cosmetic. The agents
// registry has two spellings — the inline-table form under a single `[agents]`
// header, and the idiomatic `[agents.<name>]` sub-table per agent — and a
// rewriter that dropped only the former appended a second `[agents]` block while
// leaving the sub-tables in place, defining agents.<name> twice and bricking the
// config (go-toml rejects the duplicate on the next load). The git-backup table
// has no sub-table form and never had one spliced, so it does not opt in: a
// hand-written `[destination_directory_git_backup.x]` is content this rewriter
// has never owned, and silently consuming it now would be a new way to lose a
// user's data rather than a fix.
type SpliceOptions struct {
	// IncludeSubtables also claims `[<table>.<name>]` headers as part of the
	// table's run.
	IncludeSubtables bool
}

// TableOutsideUnchanged reports whether every table EXCEPT the named one decodes
// to the same value in the original and the spliced config. Comments and
// whitespace are free to differ; data outside the section the rewriter owns is
// not — a splice that still parses but changed another table's values is silent
// corruption and must be refused.
//
// It is the fail-closed backstop that makes SpliceTOMLTable safe to run on a
// file the splicer cannot fully understand (issue #171). Both callers run it and
// abandon the write on false, leaving agentsync.toml byte-for-byte untouched.
// An unparseable ORIGINAL or RESULT is an error, not a false: the caller words
// those two facts differently to the user.
func TableOutsideUnchanged(oldRaw, newRaw []byte, table string) (bool, error) {
	var oldDoc, newDoc map[string]any
	if err := toml.Unmarshal(oldRaw, &oldDoc); err != nil {
		return false, fmt.Errorf("re-parse original config: %w", err)
	}
	if err := toml.Unmarshal(newRaw, &newDoc); err != nil {
		return false, fmt.Errorf("re-parse regenerated config: %w", err)
	}
	delete(oldDoc, table)
	delete(newDoc, table)
	return reflect.DeepEqual(oldDoc, newDoc), nil
}
