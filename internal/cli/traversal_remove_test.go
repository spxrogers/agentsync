package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func newDiscardCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd
}

// TestPluginRemove_RejectsTraversal is the regression for an arbitrary-file
// delete: `plugin remove` built filepath.Join(home, "plugins", id+".toml")
// from the raw id and os.Remove'd it with no validation, so a traversal id
// ("../../victim") deleted a file outside ~/.agentsync and exited 0. Only
// `plugin install` validated the id.
func TestPluginRemove_RejectsTraversal(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(filepath.Join(home, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTSYNC_HOME", home)

	victim := filepath.Join(base, "victim.toml")
	if err := os.WriteFile(victim, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := pluginRemoveRun(newDiscardCmd(), []string{"../../victim"}); err == nil {
		t.Fatal("expected pluginRemoveRun to reject a traversal id")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim file outside home was deleted via traversal: %v", err)
	}
}

// TestMarketplaceRemove_RejectsTraversal is the same arbitrary-delete hole in
// `marketplace remove`.
func TestMarketplaceRemove_RejectsTraversal(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(filepath.Join(home, "marketplaces"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTSYNC_HOME", home)

	victim := filepath.Join(base, "victim.toml")
	if err := os.WriteFile(victim, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := marketplaceRemoveRun(newDiscardCmd(), []string{"../../victim"}); err == nil {
		t.Fatal("expected marketplaceRemoveRun to reject a traversal name")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim file outside home was deleted via traversal: %v", err)
	}
}
