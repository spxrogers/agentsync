package cli

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestPurgeKeyRest pins the purge state-key matcher, in particular the
// colon-safety of the project-scope match: a portable project root is not
// guaranteed colon-free (a root outside $HOME stays an absolute path), so the
// matcher compares a full literal prefix instead of colon-split fields.
func TestPurgeKeyRest(t *testing.T) {
	testenv.RequireContainer(t)
	cases := []struct {
		name            string
		key             string
		agent           string
		sc              adapter.Scope
		portableProject string
		wantOK          bool
		wantRest        string
	}{
		{
			name:  "user scope matches every scope and project of the agent",
			key:   "claude:project:${HOME}/proj:${HOME}/proj/.mcp.json",
			agent: "claude", sc: adapter.ScopeUser,
			wantOK: true, wantRest: "${HOME}/proj/.mcp.json",
		},
		{
			name:  "user scope binds the agent name exactly, not as a sub-prefix",
			key:   "claude2:user::${HOME}/.claude.json",
			agent: "claude", sc: adapter.ScopeUser,
			wantOK: false,
		},
		{
			name:  "project scope matches only that project root",
			key:   "claude:project:${HOME}/proj:${HOME}/proj/.mcp.json",
			agent: "claude", sc: adapter.ScopeProject, portableProject: "${HOME}/proj",
			wantOK: true, wantRest: "${HOME}/proj/.mcp.json",
		},
		{
			name:  "project scope rejects another project's keys",
			key:   "claude:project:${HOME}/other:${HOME}/other/.mcp.json",
			agent: "claude", sc: adapter.ScopeProject, portableProject: "${HOME}/proj",
			wantOK: false,
		},
		{
			name:  "project scope rejects the agent's user-scope keys",
			key:   "claude:user::${HOME}/.claude.json",
			agent: "claude", sc: adapter.ScopeProject, portableProject: "${HOME}/proj",
			wantOK: false,
		},
		{
			name:  "project scope survives a colon-bearing project root",
			key:   "claude:project:/mnt/we:ird/proj:/mnt/we:ird/proj/.mcp.json",
			agent: "claude", sc: adapter.ScopeProject, portableProject: "/mnt/we:ird/proj",
			wantOK: true, wantRest: "/mnt/we:ird/proj/.mcp.json",
		},
		{
			name:  "keys entry remainder keeps the path:ptr tail",
			key:   "claude:project:${HOME}/proj:${HOME}/proj/.mcp.json:/mcpServers/x",
			agent: "claude", sc: adapter.ScopeProject, portableProject: "${HOME}/proj",
			wantOK: true, wantRest: "${HOME}/proj/.mcp.json:/mcpServers/x",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rest, ok := purgeKeyRest(tc.key, tc.agent, tc.sc, tc.portableProject)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && rest != tc.wantRest {
				t.Fatalf("rest = %q, want %q", rest, tc.wantRest)
			}
		})
	}
}
