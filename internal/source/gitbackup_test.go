package source

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestDestinationGitBackupConfig(t *testing.T) {
	t.Run("default mode is prompt", func(t *testing.T) {
		var g DestinationGitBackupConfig
		if got := g.EffectiveMode(); got != GitBackupModePrompt {
			t.Fatalf("EffectiveMode() = %q, want %q", got, GitBackupModePrompt)
		}
	})

	t.Run("parses the table", func(t *testing.T) {
		var c Config
		const in = `[destination_directory_git_backup]
mode = "on"
author_name = "agentsync"
author_email = "bot@localhost"
`
		if err := toml.Unmarshal([]byte(in), &c); err != nil {
			t.Fatal(err)
		}
		g := c.DestinationGitBackup
		if g.Mode != GitBackupModeOn {
			t.Errorf("Mode = %q, want on", g.Mode)
		}
		if g.AuthorName != "agentsync" || g.AuthorEmail != "bot@localhost" {
			t.Errorf("author = %q <%q>, want agentsync <bot@localhost>", g.AuthorName, g.AuthorEmail)
		}
		if g.EffectiveMode() != GitBackupModeOn {
			t.Errorf("EffectiveMode() = %q, want on", g.EffectiveMode())
		}
	})
}
