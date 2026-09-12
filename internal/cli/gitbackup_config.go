package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/source"
)

// gitBackupTable is the bare TOML table name; gitBackupTableHeader is its
// bracketed spelling, which only the generated block needs.
const (
	gitBackupTable       = "destination_directory_git_backup"
	gitBackupTableHeader = "[" + gitBackupTable + "]"
)

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
	// No IncludeSubtables: this table has no sub-table form, and claiming
	// `[destination_directory_git_backup.x]` would consume content this rewriter
	// has never owned.
	content := source.SpliceTOMLTable(raw, gitBackupTable, block, source.SpliceOptions{})

	// Fail-closed backstop (issue #171): SpliceTOMLTable is line-based, not a TOML
	// parser — it assumes the target table is a simple contiguous block and does not
	// distinguish `[x]` from an array-of-tables `[[x]]`. On an unusual-but-valid
	// layout (the table mid-file followed by an `[[array.of.tables]]`, interleaved
	// sections, or a multi-line string whose CONTENT contains a `[...]` line) the
	// splice can produce bytes that no longer parse, or that silently mangle ANOTHER
	// table. Mirror writeAgents' guard: re-parse the spliced result and require BOTH
	// that it parses as a full canonical config AND that everything outside the
	// git-backup table is unchanged; refuse the write (leaving agentsync.toml
	// byte-for-byte untouched) otherwise.
	var check source.Config
	if err := toml.Unmarshal(content, &check); err != nil {
		return fmt.Errorf("refusing to rewrite %s: the regenerated config no longer parses (%v); "+
			"the file likely uses a TOML construct the git-backup splicer cannot handle — "+
			"edit the [destination_directory_git_backup] table by hand (mode change aborted)", p, err)
	}
	same, err := source.TableOutsideUnchanged(raw, content, gitBackupTable)
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
