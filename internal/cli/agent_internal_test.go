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
		{
			name:  "user-scope keys entry remainder keeps the path:ptr tail",
			key:   "claude:user::${HOME}/.claude.json:/mcpServers/x",
			agent: "claude", sc: adapter.ScopeUser,
			wantOK: true, wantRest: "${HOME}/.claude.json:/mcpServers/x",
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

// TestOtherFilesKeyPath pins the shared-file guard's path extraction: keys
// under the purge's OWN project root are stripped colon-safely (they are the
// candidates for a co-owned dest), everything else falls back to the 4th colon
// field. Both sides of the guard must extract a same-root dest identically, or
// a co-owned file under a colon-bearing root would lose its protection and be
// deleted without backup.
func TestOtherFilesKeyPath(t *testing.T) {
	testenv.RequireContainer(t)
	cases := []struct {
		name            string
		key             string
		portableProject string
		want            string
	}{
		{
			name:            "same project root, colon-bearing, extracts the exact path",
			key:             "opencode:project:/mnt/we:ird/proj:/mnt/we:ird/proj/x/SKILL.md",
			portableProject: "/mnt/we:ird/proj",
			want:            "/mnt/we:ird/proj/x/SKILL.md",
		},
		{
			name:            "same project root, plain, extracts the path",
			key:             "opencode:project:${HOME}/proj:${HOME}/proj/.mcp.json",
			portableProject: "${HOME}/proj",
			want:            "${HOME}/proj/.mcp.json",
		},
		{
			name:            "user-scope key falls back to the exact 4th field",
			key:             "opencode:user::${HOME}/.claude.json",
			portableProject: "${HOME}/proj",
			want:            "${HOME}/.claude.json",
		},
		{
			name:            "malformed short key yields nothing",
			key:             "opencode:user",
			portableProject: "${HOME}/proj",
			want:            "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := otherFilesKeyPath(tc.key, tc.portableProject); got != tc.want {
				t.Fatalf("otherFilesKeyPath(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}
