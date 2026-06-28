package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
