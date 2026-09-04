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
// synthesizes for an emptied key-merge section is built by adapter.NewCleanupOp
// and so carries Kind == adapter.OpCleanup: `apply` labels and counts the op
// as a removal by reading that kind, not by sniffing the "{}"+OwnedKeys shape,
// so a missing stamp would silently relabel every key removal as a write. The
// purge path (`agent disable --purge`) calls the same constructor. The second
// case pins the dest-exists gate: an already-deleted dest gets no op (rather
// than a freshly created empty "{}" file) — PruneStaleState drops its entry.
func TestOrphanCleanupOps_StampsOpCleanup(t *testing.T) {
	testenv.RequireContainer(t)
	// orphaned returns state that owns /mcpServers/srv at <userHome>/.claude.json
	// while the adapter rendered NO op for that section this run — the source
	// section emptied, so the owned pointer is orphaned.
	orphaned := func(t *testing.T) (userHome, dest string, s *state.Targets) {
		t.Helper()
		userHome = t.TempDir()
		dest = filepath.Join(userHome, ".claude.json")
		s = state.New()
		owned := state.Key{Agent: "claude", Scope: "user", Path: paths.HomeRelative(userHome, dest), Pointer: "/mcpServers/srv"}
		s.Keys[owned] = state.KeyEntry{SHA256: "deadbeef"}
		return userHome, dest, s
	}

	t.Run("dest present: one op, built by NewCleanupOp", func(t *testing.T) {
		userHome, dest, s := orphaned(t)
		if err := os.WriteFile(dest, []byte(`{"mcpServers":{"srv":{"command":"x"}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
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
	})

	t.Run("dest already gone: nothing to prune", func(t *testing.T) {
		userHome, _, s := orphaned(t)
		// No file at dest. Synthesizing "{}" here would CREATE an empty file
		// where the user deleted one; the pipeline skips it instead.
		got := orphanCleanupOps(s, keyMergeNoop{noop.New("claude")}, "claude", adapter.ScopeUser, "", userHome, nil)
		if len(got) != 0 {
			t.Fatalf("orphanCleanupOps = %+v, want no op for an absent dest", got)
		}
	})
}
