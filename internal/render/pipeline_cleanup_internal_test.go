package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/noop"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// keyMergeNoop is a noop adapter that claims a key-merge strategy, so
// orphanCleanupOps has a destination format to synthesize against.
type keyMergeNoop struct{ *noop.Adapter }

func (keyMergeNoop) KeyMergeStrategy() string { return "merge-json-keys" }

// TestOrphanCleanupOps_StampsOpCleanup pins that the cleanup op the pipeline
// synthesizes for an emptied key-merge section is built by adapter.CleanupOp
// and so carries Kind == adapter.OpCleanup: `apply` labels and counts the op
// as a removal by reading that kind, not by sniffing the "{}"+OwnedKeys shape,
// so a missing stamp would silently relabel every key removal as a write. The
// purge path (`agent disable --purge`) calls the same constructor.
func TestOrphanCleanupOps_StampsOpCleanup(t *testing.T) {
	testenv.RequireContainer(t)
	userHome := t.TempDir()
	dest := filepath.Join(userHome, ".claude.json")
	// The dest must exist: orphanCleanupOps skips a dest that is already gone
	// rather than creating an empty "{}" file.
	if err := os.WriteFile(dest, []byte(`{"mcpServers":{"srv":{"command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := state.New()
	owned := state.Key{Agent: "claude", Scope: "user", Path: paths.HomeRelative(userHome, dest), Pointer: "/mcpServers/srv"}
	s.Keys[owned] = state.KeyEntry{SHA256: "deadbeef"}

	// The adapter rendered NO op for the mcpServers section this run — the
	// source section emptied — so the owned pointer is orphaned.
	got := orphanCleanupOps(s, keyMergeNoop{noop.New("claude")}, "claude", adapter.ScopeUser, "", userHome, nil)
	if len(got) != 1 {
		t.Fatalf("orphanCleanupOps = %+v, want exactly one cleanup op", got)
	}
	op := got[0]
	if op.Kind != adapter.OpCleanup {
		t.Errorf("Kind = %v, want OpCleanup — apply reads this kind to label and count the removal", op.Kind)
	}
	if op.Action != adapter.ActionWrite {
		t.Errorf("Action = %v, want ActionWrite (the merge path performs the removal)", op.Action)
	}
	if op.Path != dest {
		t.Errorf("Path = %q, want %q", op.Path, dest)
	}
	if string(op.Content) != "{}" {
		t.Errorf("Content = %q, want \"{}\"", op.Content)
	}
	if op.MergeStrategy != "merge-json-keys" {
		t.Errorf("MergeStrategy = %q, want the adapter's KeyMergeStrategy", op.MergeStrategy)
	}
	if len(op.OwnedKeys) != 1 || op.OwnedKeys[0] != "/mcpServers/srv" {
		t.Errorf("OwnedKeys = %v, want [/mcpServers/srv]", op.OwnedKeys)
	}
}
