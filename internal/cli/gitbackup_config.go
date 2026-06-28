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

const gitBackupTableHeader = "[destination_directory_git_backup]"

// setDestinationGitBackupMode writes mode into the
// [destination_directory_git_backup] table of <home>/agentsync.toml, preserving
// everything OUTSIDE that table (comments, key order, other sections) via a
// line-splice — never a full marshal, which would strip the user's comments. Any
// existing author_name/author_email overrides in the table are carried across.
// Creates the table if absent.
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
	if err := iox.AtomicWrite(p, []byte(out), 0o644); err != nil {
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

// spliceTOMLTable replaces the simple TOML table named by header (e.g.
// "[destination_directory_git_backup]") with newBlock, preserving everything
// outside it. A table runs from its header line to the next line beginning with
// "[". If the table is absent, newBlock is appended after a blank-line separator.
// Mirrors writeAgents' approach so config edits never clobber hand-written content.
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
