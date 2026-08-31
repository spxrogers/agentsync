package cli

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/state"
)

// TestPruneStateFilesForPath_MatchesThePathExactly pins that the orphan prune
// compares the typed key's Path FIELD for equality rather than suffix-matching
// the encoded key, which is what the v1 string key forced
// (strings.HasSuffix(key, ":"+portable)).
//
// The two destinations below are both real and both reachable on one machine:
// paths.HomeRelative rewrites a path INSIDE $HOME to "${HOME}/…" but returns one
// OUTSIDE it verbatim, so a project at /opt/cfg and a project at $HOME/opt/cfg
// are stored as "/opt/cfg/.mcp.json" and "${HOME}/opt/cfg/.mcp.json" — and the
// first is a proper SUFFIX of the second. Pruning the orphan at /opt/cfg must
// not take the other project's ownership with it: a state entry deleted while
// its file survives makes the next apply treat that file as a foreign collision
// and back it up before overwriting.
func TestPruneStateFilesForPath_MatchesThePathExactly(t *testing.T) {
	const userHome = "/home/alice"
	orphan := state.Key{
		Agent: "claude", Scope: "project",
		Project: "/opt/cfg", Path: "/opt/cfg/.mcp.json",
	}
	sibling := state.Key{
		Agent: "claude", Scope: "project",
		Project: "${HOME}/opt/cfg", Path: "${HOME}/opt/cfg/.mcp.json",
	}
	s := state.New()
	s.Files[orphan] = state.FileEntry{SHA256: "a"}
	s.Files[sibling] = state.FileEntry{SHA256: "b"}

	pruneStateFilesForPath(s, userHome, "/opt/cfg/.mcp.json")

	if _, ok := s.Files[orphan]; ok {
		t.Fatal("the removed orphan's own state entry must be pruned")
	}
	if _, ok := s.Files[sibling]; !ok {
		t.Fatalf("pruning %q also disowned %q — the match must be on the whole Path field, not a suffix",
			orphan.Path, sibling.Path)
	}
}
