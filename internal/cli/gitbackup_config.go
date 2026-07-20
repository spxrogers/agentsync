package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/source"
)

const gitBackupTableHeader = "[destination_directory_git_backup]"

// setDestinationGitBackupMode writes mode into the
// [destination_directory_git_backup] table of <home>/agentsync.toml via a
// line-splice — never a full marshal, which would strip the user's comments. It
// preserves the keys, section order, and comments of every OTHER table. Any
// existing author_name/author_email overrides in the table are carried across;
// the table is created if absent.
//
// Fidelity caveat: the splice's "run" for this table extends to the next
// `[header]`, so a comment or blank line sitting between this table's last key
// and the following section header is treated as part of this table and is NOT
// preserved (put a section's leading comment below its own header). The
// fail-closed backstop below guarantees no *semantic* content outside the table
// changes — that is the load-bearing invariant; trailing-comment fidelity for
// this table's own run is best-effort, not guaranteed.
func setDestinationGitBackupMode(home, mode string) error {
	p := filepath.Join(home, "agentsync.toml")
	raw, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("read %s: %w", p, err)
	}
	// Read just the table (not the whole source tree) to preserve author overrides.
	var cfg struct {
		Table source.DestinationGitBackupConfig `toml:"destination_directory_git_backup"`
	}
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse %s: %w", p, err)
	}
	block := buildGitBackupSection(mode, cfg.Table.AuthorName, cfg.Table.AuthorEmail)
	out := spliceTOMLTable(string(raw), gitBackupTableHeader, block)

	// Fail-closed backstop (issue #171): spliceTOMLTable is line-based, not a TOML
	// parser — it assumes the target table is a simple contiguous block and does not
	// distinguish `[x]` from an array-of-tables `[[x]]`. On an unusual-but-valid
	// layout (the table mid-file followed by an `[[array.of.tables]]`, interleaved
	// sections, or a multi-line string whose CONTENT contains a `[...]` line) the
	// splice can produce bytes that no longer parse, or that silently mangle ANOTHER
	// table. Mirror writeAgents' guard: re-parse the spliced result and require BOTH
	// that it parses as a full canonical config AND that everything outside the
	// git-backup table is unchanged; refuse the write (leaving agentsync.toml
	// byte-for-byte untouched) otherwise.
	content := []byte(out)
	var check source.Config
	if err := toml.Unmarshal(content, &check); err != nil {
		return fmt.Errorf("refusing to rewrite %s: the regenerated config no longer parses (%v); "+
			"the file likely uses a TOML construct the git-backup splicer cannot handle — "+
			"edit the [destination_directory_git_backup] table by hand (mode change aborted)", p, err)
	}
	same, err := nonGitBackupUnchanged(raw, content)
	if err != nil {
		return fmt.Errorf("refusing to rewrite %s: %w (mode change aborted)", p, err)
	}
	if !same {
		return fmt.Errorf("refusing to rewrite %s: the rewrite would alter content outside the "+
			"[destination_directory_git_backup] table — edit it by hand (mode change aborted)", p)
	}
	if err := iox.AtomicWrite(p, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}

// nonGitBackupUnchanged reports whether everything OUTSIDE the git-backup table is
// semantically identical between the original and spliced config — the backstop's
// oracle that the splice touched only its own table (issue #171).
func nonGitBackupUnchanged(oldRaw, newRaw []byte) (bool, error) {
	var oldDoc, newDoc map[string]any
	if err := toml.Unmarshal(oldRaw, &oldDoc); err != nil {
		return false, fmt.Errorf("re-parse original config: %w", err)
	}
	if err := toml.Unmarshal(newRaw, &newDoc); err != nil {
		return false, fmt.Errorf("re-parse regenerated config: %w", err)
	}
	delete(oldDoc, "destination_directory_git_backup")
	delete(newDoc, "destination_directory_git_backup")
	return reflect.DeepEqual(oldDoc, newDoc), nil
}

// buildGitBackupSection renders the [destination_directory_git_backup] block,
// emitting author lines only when set so we never write empty overrides.
func buildGitBackupSection(mode, authorName, authorEmail string) string {
	var sb strings.Builder
	sb.WriteString(gitBackupTableHeader + "\n")
	fmt.Fprintf(&sb, "mode = %q\n", mode)
	if authorName != "" {
		fmt.Fprintf(&sb, "author_name = %q\n", authorName)
	}
	if authorEmail != "" {
		fmt.Fprintf(&sb, "author_email = %q\n", authorEmail)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// spliceTOMLTable replaces the simple TOML table named by header (e.g.
// "[destination_directory_git_backup]") with newBlock, preserving the content of
// every OTHER table. A table's "run" is from its header line to the next line
// beginning with "[", so a trailing comment or blank line between the table's
// keys and the following "[header]" is part of THIS table's run and is replaced
// along with it — comment fidelity is therefore best-effort for lines adjacent
// to the spliced table (the caller's fail-closed re-parse backstop, not this
// line-splice, is what guarantees no data outside the table is lost). If the
// table is absent, newBlock is appended after a blank-line separator. Mirrors
// writeAgents' approach so config edits never clobber hand-written content.
func spliceTOMLTable(raw, header, newBlock string) string {
	newLines := strings.Split(newBlock, "\n")
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines)+len(newLines))
	insertAt := -1
	inSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			// A new table header: ours iff it matches header exactly.
			inSection = trimmed == header
			if inSection {
				if insertAt < 0 {
					insertAt = len(out)
				}
				continue // drop the old header
			}
		}
		if inSection {
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
		// Keep a blank line before whatever section follows so repeated edits
		// don't collapse the file's spacing (the loop drops blanks inside the
		// table, so one separator is re-added each run — no accumulation).
		if len(tail) > 0 && strings.TrimSpace(tail[0]) != "" {
			out = append(out, "")
		}
		out = append(out, tail...)
	}
	return strings.Join(out, "\n")
}
