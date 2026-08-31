package cli

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestPurgeMatches pins the purge state-key matcher. Under the v1 string key
// this needed a literal-prefix match plus a colon-split fallback, and a
// colon-bearing project root mangled the extracted path — an accepted residual
// that made `--purge` under-delete while reporting success (issue #227). The
// typed key compares fields, so a colon-bearing root is now ordinary.
func TestPurgeMatches(t *testing.T) {
	testenv.RequireContainer(t)
	cases := []struct {
		name            string
		key             state.Key
		agent           string
		sc              adapter.Scope
		portableProject string
		want            bool
	}{
		{
			name:  "user scope matches every scope and project of the agent",
			key:   state.Key{Agent: "claude", Scope: "project", Project: "${HOME}/proj", Path: "${HOME}/proj/.mcp.json"},
			agent: "claude", sc: adapter.ScopeUser,
			want: true,
		},
		{
			name:  "user scope binds the agent name exactly, not as a sub-prefix",
			key:   state.Key{Agent: "claude2", Scope: "user", Path: "${HOME}/.claude.json"},
			agent: "claude", sc: adapter.ScopeUser,
			want: false,
		},
		{
			name:  "project scope matches only that project root",
			key:   state.Key{Agent: "claude", Scope: "project", Project: "${HOME}/proj", Path: "${HOME}/proj/.mcp.json"},
			agent: "claude", sc: adapter.ScopeProject, portableProject: "${HOME}/proj",
			want: true,
		},
		{
			name:  "project scope rejects another project's keys",
			key:   state.Key{Agent: "claude", Scope: "project", Project: "${HOME}/other", Path: "${HOME}/other/.mcp.json"},
			agent: "claude", sc: adapter.ScopeProject, portableProject: "${HOME}/proj",
			want: false,
		},
		{
			name:  "project scope rejects the agent's user-scope keys",
			key:   state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude.json"},
			agent: "claude", sc: adapter.ScopeProject, portableProject: "${HOME}/proj",
			want: false,
		},
		{
			name:  "project scope handles a colon-bearing project root",
			key:   state.Key{Agent: "claude", Scope: "project", Project: "/mnt/we:ird/proj", Path: "/mnt/we:ird/proj/.mcp.json"},
			agent: "claude", sc: adapter.ScopeProject, portableProject: "/mnt/we:ird/proj",
			want: true,
		},
		{
			name: "project scope rejects a SIBLING root that merely shares a prefix",
			key: state.Key{
				Agent: "claude", Scope: "project",
				Project: "${HOME}/work/app:staging", Path: "${HOME}/work/app:staging/.mcp.json",
			},
			agent: "claude", sc: adapter.ScopeProject, portableProject: "${HOME}/work/app",
			want: false,
		},
		{
			name:  "pointer keys match on the same legs",
			key:   state.Key{Agent: "claude", Scope: "project", Project: "${HOME}/proj", Path: "${HOME}/proj/.mcp.json", Pointer: "/mcpServers/x"},
			agent: "claude", sc: adapter.ScopeProject, portableProject: "${HOME}/proj",
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := purgeMatches(tc.key, tc.agent, tc.sc, tc.portableProject); got != tc.want {
				t.Fatalf("purgeMatches(%+v) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}
