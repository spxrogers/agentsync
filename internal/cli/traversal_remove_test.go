package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
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

// TestWriteBackFileItem_RejectsTraversal is the symmetric guard for the reverse
// (dest→source) write boundary: writeBackFileItem joined op.SourceID onto home
// and AtomicWrite'd it with no containment check. SourceID derives from a
// component Name, so a "../" segment would let the [w]rite-back action clobber
// an arbitrary file outside ~/.agentsync. The forward import boundary
// (source.Write*) was fenced with validateComponentID; this one was not.
func TestWriteBackFileItem_RejectsTraversal(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}

	victim := filepath.Join(base, "victim.txt")
	if err := os.WriteFile(victim, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A drifted dest file whose SourceID escapes the source tree.
	srcEdit := filepath.Join(base, "edited.txt")
	if err := os.WriteFile(srcEdit, []byte("attacker payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	it := reconcileItem{op: adapter.FileOp{Path: srcEdit, SourceID: "../victim.txt"}}

	if err := writeBackFileItem(home, it); err == nil {
		t.Fatal("expected writeBackFileItem to reject a traversal SourceID")
	}
	if data, _ := os.ReadFile(victim); string(data) != "precious" {
		t.Fatalf("victim file outside home was overwritten via traversal: %q", data)
	}
}
