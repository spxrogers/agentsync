package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

func TestSetDestinationGitBackupMode(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	const initial = `# my agentsync config
# keep these comments

[agents]
claude = { enabled = true, scope = "user" }

[updates]
default_mode = "track"
`
	p := filepath.Join(home, "agentsync.toml")
	if err := os.WriteFile(p, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := setDestinationGitBackupMode(home, source.GitBackupModeOn); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	// Comments + the other section survive.
	if !strings.Contains(s, "# keep these comments") {
		t.Errorf("comments were lost:\n%s", s)
	}
	if !strings.Contains(s, `default_mode = "track"`) {
		t.Errorf("[updates] table was lost:\n%s", s)
	}
	// The new table parses back to the persisted mode.
	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		t.Fatal(err)
	}
	if c.Config.DestinationGitBackup.EffectiveMode() != source.GitBackupModeOn {
		t.Fatalf("mode = %q, want on", c.Config.DestinationGitBackup.EffectiveMode())
	}

	// Idempotent: flipping to off twice leaves exactly one table, no dup headers.
	if err := setDestinationGitBackupMode(home, source.GitBackupModeOff); err != nil {
		t.Fatal(err)
	}
	if err := setDestinationGitBackupMode(home, source.GitBackupModeOff); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(p)
	if n := strings.Count(string(got2), gitBackupTableHeader); n != 1 {
		t.Fatalf("want exactly 1 %s table, got %d:\n%s", gitBackupTableHeader, n, got2)
	}
	c2, _ := source.Load(afero.NewOsFs(), home)
	if c2.Config.DestinationGitBackup.Mode != source.GitBackupModeOff {
		t.Fatalf("mode = %q, want off", c2.Config.DestinationGitBackup.Mode)
	}
}

func TestSetDestinationGitBackupMode_PreservesAuthors(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	const initial = `[destination_directory_git_backup]
mode = "prompt"
author_name = "Custom Bot"
author_email = "bot@example.com"
`
	p := filepath.Join(home, "agentsync.toml")
	if err := os.WriteFile(p, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setDestinationGitBackupMode(home, source.GitBackupModeOn); err != nil {
		t.Fatal(err)
	}
	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		t.Fatal(err)
	}
	g := c.Config.DestinationGitBackup
	if g.Mode != source.GitBackupModeOn || g.AuthorName != "Custom Bot" || g.AuthorEmail != "bot@example.com" {
		t.Fatalf("author overrides not preserved: %+v", g)
	}
}

// TestSetDestinationGitBackupMode_PreservesParseability drives the fail-closed
// re-parse backstop (issue #171): a mode change over an unusual-but-valid layout
// must still yield a config that parses, and a mode change that would corrupt the
// file must be refused with the original left byte-for-byte untouched.
func TestSetDestinationGitBackupMode_PreservesParseability(t *testing.T) {
	testenv.RequireContainer(t)

	ok := []struct {
		name, initial string
	}{
		{
			name: "array-of-tables-after-table",
			initial: "[destination_directory_git_backup]\nmode = \"off\"\n\n" +
				"[[servers]]\nhost = \"a\"\n\n[[servers]]\nhost = \"b\"\n",
		},
		{
			name:    "table-between-commented-sections",
			initial: "# top\n[updates]\ndefault_mode = \"track\"\n\n[destination_directory_git_backup]\nmode = \"prompt\"\n\n# tail\n[agents]\nclaude = { enabled = true, scope = \"user\" }\n",
		},
		{
			name:    "no-existing-table",
			initial: "[agents]\nclaude = { enabled = true, scope = \"user\" }\n",
		},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			p := filepath.Join(home, "agentsync.toml")
			if err := os.WriteFile(p, []byte(tc.initial), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := setDestinationGitBackupMode(home, source.GitBackupModeOn); err != nil {
				t.Fatalf("mode change should succeed on a valid layout: %v", err)
			}
			// Verify via a non-strict parse (matching the backstop) so an array-of-
			// tables or other agentsync-unmodeled-but-valid TOML doesn't trip the
			// strict source.Load; the concern here is TOML syntax survival + the mode.
			got, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var check struct {
				Table source.DestinationGitBackupConfig `toml:"destination_directory_git_backup"`
			}
			if err := toml.Unmarshal(got, &check); err != nil {
				t.Fatalf("result no longer parses as TOML: %v\n%s", err, got)
			}
			if check.Table.EffectiveMode() != source.GitBackupModeOn {
				t.Fatalf("mode = %q, want on\n%s", check.Table.EffectiveMode(), got)
			}
		})
	}

	// A multi-line string whose CONTENT contains the table header line makes the
	// line-based splicer mis-cut (dropping the string's closing """), corrupting the
	// file. The backstop must refuse and leave the original untouched.
	t.Run("refuses-corrupting-splice", func(t *testing.T) {
		home := t.TempDir()
		p := filepath.Join(home, "agentsync.toml")
		initial := "[other]\nnote = \"\"\"\nsome text\n[destination_directory_git_backup]\nmore text\n\"\"\"\n\n" +
			"[destination_directory_git_backup]\nmode = \"off\"\n"
		if err := os.WriteFile(p, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}
		err := setDestinationGitBackupMode(home, source.GitBackupModeOn)
		if err == nil {
			t.Fatal("a corrupting splice must be refused")
		}
		if !strings.Contains(err.Error(), "refusing to rewrite") {
			t.Fatalf("want a refusal error; got: %v", err)
		}
		got, _ := os.ReadFile(p)
		if string(got) != initial {
			t.Fatalf("file must be byte-for-byte unchanged on refusal:\n%s", got)
		}
	})
}
