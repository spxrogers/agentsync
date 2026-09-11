package source_test

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestSpliceTOMLTable anchors the rewriter to BYTES, not to a parsed model.
//
// The whole reason this function exists instead of a marshal/unmarshal round
// trip is that agentsync.toml is a file a human wrote and keeps: its comments,
// its blank lines, its section order and its key formatting are content. A test
// that asserted "the result parses and the table is right" would pass on a
// rewriter that reflowed the entire file, which is exactly the behaviour being
// avoided — so every case below states the expected output string in full.
//
// The fixtures are layouts a real agentsync.toml takes: the table mid-file
// between two others, absent, last with no trailing newline, in the
// `[agents.<name>]` sub-table spelling, and CRLF.
func TestSpliceTOMLTable(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		table string
		block string
		opts  source.SpliceOptions
		want  string
	}{
		{
			// Comments and blank lines OUTSIDE the table survive verbatim; the
			// one INSIDE its run does not. That asymmetry is the documented
			// best-effort caveat, pinned here so it is a known property rather
			// than a surprise.
			name: "mid-file between two other tables",
			raw: "# top comment\n[updates]\ndefault_mode = \"track\"\n\n" +
				"[agents]\n# inner comment\nclaude = { enabled = true }\n\n" +
				"[secrets]\nbackend = \"age\"\n",
			table: "agents",
			block: "[agents]\nclaude = { enabled = true }\nopencode = { enabled = true }",
			opts:  source.SpliceOptions{IncludeSubtables: true},
			want: "# top comment\n[updates]\ndefault_mode = \"track\"\n\n" +
				"[agents]\nclaude = { enabled = true }\nopencode = { enabled = true }\n\n" +
				"[secrets]\nbackend = \"age\"\n",
		},
		{
			name:  "absent — appended after a blank-line separator",
			raw:   "# top\n[secrets]\nbackend = \"age\"\n",
			table: "agents",
			block: "[agents]\nclaude = { enabled = true }",
			opts:  source.SpliceOptions{IncludeSubtables: true},
			want:  "# top\n[secrets]\nbackend = \"age\"\n\n[agents]\nclaude = { enabled = true }",
		},
		{
			// The input's last line has no newline. The splice must not invent
			// one, and must not swallow the line before it.
			name:  "last table, no trailing newline",
			raw:   "[secrets]\nbackend = \"age\"\n\n[agents]\nclaude = { enabled = true }",
			table: "agents",
			block: "[agents]\nclaude = { enabled = true }\nopencode = { enabled = true }",
			opts:  source.SpliceOptions{IncludeSubtables: true},
			want:  "[secrets]\nbackend = \"age\"\n\n[agents]\nclaude = { enabled = true }\nopencode = { enabled = true }",
		},
		{
			// The regression IncludeSubtables exists for: without it the
			// sub-tables stay and the regenerated [agents] block is appended
			// beside them, defining agents.claude twice — which go-toml rejects
			// on the next load, bricking the config.
			name:  "sub-table spelling is claimed when opted in",
			raw:   "# my config\n[agents.claude]\nenabled = true\nscope = \"user\"\n",
			table: "agents",
			block: "[agents]\nclaude = { enabled = true, scope = \"user\" }\nopencode = { enabled = true }",
			opts:  source.SpliceOptions{IncludeSubtables: true},
			want:  "# my config\n[agents]\nclaude = { enabled = true, scope = \"user\" }\nopencode = { enabled = true }",
		},
		{
			// The git-backup caller does NOT opt in. A hand-written sub-table is
			// content this rewriter has never owned, so it survives untouched
			// where the main table is replaced in place.
			name:  "sub-table spelling is left alone when not opted in",
			raw:   "[destination_directory_git_backup.extra]\nk = 1\n\n[destination_directory_git_backup]\nmode = \"off\"\n",
			table: "destination_directory_git_backup",
			block: "[destination_directory_git_backup]\nmode = \"on\"",
			opts:  source.SpliceOptions{},
			want:  "[destination_directory_git_backup.extra]\nk = 1\n\n[destination_directory_git_backup]\nmode = \"on\"",
		},
		{
			// CHARACTERIZATION, not an endorsement: the splicer splits on "\n",
			// so a CRLF file keeps CRLF on the lines it copies and gets LF on the
			// lines it writes. The result is mixed-ending but still valid TOML
			// (the \r rides at the end of the copied lines). Recorded so a future
			// change to line handling is a deliberate decision with a failing
			// test, not an accident.
			name:  "CRLF input keeps CRLF outside the run and gets LF inside it",
			raw:   "# top\r\n[agents]\r\nclaude = { enabled = true }\r\n\r\n[secrets]\r\nbackend = \"age\"\r\n",
			table: "agents",
			block: "[agents]\nclaude = { enabled = true }",
			opts:  source.SpliceOptions{IncludeSubtables: true},
			want:  "# top\r\n[agents]\nclaude = { enabled = true }\n\n[secrets]\r\nbackend = \"age\"\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := source.SpliceTOMLTable(tc.raw, tc.table, tc.block, tc.opts)
			if got != tc.want {
				t.Errorf("SpliceTOMLTable bytes differ\n got: %q\nwant: %q", got, tc.want)
			}
			// Whatever the splice produced must still be TOML. This is weaker
			// than the byte assertion above and is here for the cases it cannot
			// cover on its own: a result that matches `want` but no longer
			// parses would mean `want` itself was wrong.
			var doc map[string]any
			if err := toml.Unmarshal([]byte(got), &doc); err != nil {
				t.Errorf("spliced result does not parse as TOML: %v\n%s", err, got)
			}
		})
	}
}

// TestTableOutsideUnchanged pins the fail-closed backstop itself — the check
// both callers run before persisting a splice, and the only reason a line-based
// rewriter is safe to point at a user's config.
//
// Weakening it is the subtle failure this whole change could hide: a backstop
// that answered true for a mangled neighbour, or that treated an unparseable
// result as "unchanged", would leave the splice looking correct while silently
// corrupting a table the rewriter does not own.
func TestTableOutsideUnchanged(t *testing.T) {
	const base = "[agents]\nclaude = { enabled = true }\n\n[updates]\ndefault_mode = \"track\"\n"

	t.Run("a changed OWN table is ignored", func(t *testing.T) {
		after := "[agents]\nclaude = { enabled = false }\nopencode = { enabled = true }\n\n[updates]\ndefault_mode = \"track\"\n"
		same, err := source.TableOutsideUnchanged([]byte(base), []byte(after), "agents")
		if err != nil || !same {
			t.Fatalf("same, err = %v, %v; the named table is the one the caller is rewriting", same, err)
		}
	})

	t.Run("comments and whitespace outside are free to differ", func(t *testing.T) {
		after := "# a new comment\n[agents]\nclaude = { enabled = true }\n\n\n[updates]\ndefault_mode   =   \"track\"\n"
		same, err := source.TableOutsideUnchanged([]byte(base), []byte(after), "agents")
		if err != nil || !same {
			t.Fatalf("same, err = %v, %v; the check is over VALUES, not bytes", same, err)
		}
	})

	t.Run("a changed neighbour table is caught", func(t *testing.T) {
		after := "[agents]\nclaude = { enabled = true }\n\n[updates]\ndefault_mode = \"pin\"\n"
		same, err := source.TableOutsideUnchanged([]byte(base), []byte(after), "agents")
		if err != nil {
			t.Fatal(err)
		}
		if same {
			t.Fatal("a neighbour table's value changed and the backstop said nothing changed; " +
				"this is the silent-corruption case the check exists for")
		}
	})

	t.Run("a dropped neighbour table is caught", func(t *testing.T) {
		after := "[agents]\nclaude = { enabled = true }\n"
		same, err := source.TableOutsideUnchanged([]byte(base), []byte(after), "agents")
		if err != nil {
			t.Fatal(err)
		}
		if same {
			t.Fatal("a neighbour table vanished and the backstop said nothing changed")
		}
	})

	t.Run("an unparseable result is an ERROR, not a quiet false", func(t *testing.T) {
		same, err := source.TableOutsideUnchanged([]byte(base), []byte("[agents]\nbroken = \n"), "agents")
		if err == nil {
			t.Fatal("want an error naming the re-parse; the callers word 'does not parse' and " +
				"'would alter content outside' differently to the user")
		}
		if same {
			t.Fatal("same must be false alongside the error")
		}
	})

	t.Run("an unparseable ORIGINAL is an error too", func(t *testing.T) {
		if _, err := source.TableOutsideUnchanged([]byte("[oops\n"), []byte(base), "agents"); err == nil {
			t.Fatal("want an error naming the original re-parse")
		}
	})
}
