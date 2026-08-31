package state

import (
	"errors"
	"testing"
)

// TestParseLegacyKey covers every v1 key shape that appears in this repo's own
// test corpus plus the adversarial ones. The v1 format joined fields with ':'
// and both the project root and the destination path may legally contain one,
// so the parser enumerates readings and accepts one only when the choice is
// forced.
func TestParseLegacyKey(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		pointered bool
		want      Key
		wantErr   bool
		ambiguous bool
	}{
		{
			name: "user scope, whole file",
			in:   "claude:user::${HOME}/CLAUDE.md",
			want: Key{Agent: "claude", Scope: "user", Path: "${HOME}/CLAUDE.md"},
		},
		{
			name: "user scope, empty path",
			in:   "claude:user::",
			want: Key{Agent: "claude", Scope: "user"},
		},
		{
			name: "user scope, colon-bearing path in a file key is unambiguous",
			in:   "claude:user::a:b:c",
			want: Key{Agent: "claude", Scope: "user", Path: "a:b:c"},
		},
		{
			name: "agent name is bound exactly",
			in:   "claude2:user::${HOME}/.claude.json",
			want: Key{Agent: "claude2", Scope: "user", Path: "${HOME}/.claude.json"},
		},
		{
			name: "project scope, plain",
			in:   "claude:project:${HOME}/proj:${HOME}/proj/.mcp.json",
			want: Key{Agent: "claude", Scope: "project", Project: "${HOME}/proj", Path: "${HOME}/proj/.mcp.json"},
		},
		{
			name: "project scope, colon-bearing root resolves by containment",
			in:   "claude:project:/mnt/we:ird/proj:/mnt/we:ird/proj/.mcp.json",
			want: Key{Agent: "claude", Scope: "project", Project: "/mnt/we:ird/proj", Path: "/mnt/we:ird/proj/.mcp.json"},
		},
		{
			name: "project scope, the #227 sibling-collision root",
			in:   "claude:project:${HOME}/work/app:staging:${HOME}/work/app:staging/.mcp.json",
			want: Key{
				Agent: "claude", Scope: "project",
				Project: "${HOME}/work/app:staging", Path: "${HOME}/work/app:staging/.mcp.json",
			},
		},
		{
			name: "project scope, single candidate wins even without containment",
			in:   "claude:project:${HOME}/proj:${HOME}/other/x.md",
			want: Key{Agent: "claude", Scope: "project", Project: "${HOME}/proj", Path: "${HOME}/other/x.md"},
		},
		{
			name:      "pointer key, user scope",
			in:        "claude:user::${HOME}/.claude.json:/mcpServers/github",
			pointered: true,
			want:      Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude.json", Pointer: "/mcpServers/github"},
		},
		{
			name:      "pointer key, colon-bearing path anchors on \":/\"",
			in:        "claude:user::a:b:/realptr",
			pointered: true,
			want:      Key{Agent: "claude", Scope: "user", Path: "a:b", Pointer: "/realptr"},
		},
		{
			name:      "pointer key, project scope",
			in:        "claude:project:${HOME}/proj:${HOME}/proj/.claude/settings.json:/hooks/PreToolUse",
			pointered: true,
			want: Key{
				Agent: "claude", Scope: "project", Project: "${HOME}/proj",
				Path: "${HOME}/proj/.claude/settings.json", Pointer: "/hooks/PreToolUse",
			},
		},
		{
			name:    "no scope field",
			in:      "opencode:user",
			wantErr: true,
		},
		{
			name:    "second field is not a scope",
			in:      "claude:nope::x",
			wantErr: true,
		},
		{
			name:    "no agent field",
			in:      ":user::x",
			wantErr: true,
		},
		{
			name:      "pointer key with no pointer",
			in:        "claude:user::${HOME}/.claude.json",
			pointered: true,
			wantErr:   true,
		},
		{
			name:      "ambiguous project split",
			in:        "claude:project:${HOME}/a:${HOME}/b:${HOME}/c",
			wantErr:   true,
			ambiguous: true,
		},
		{
			name:      "ambiguous pointer split",
			in:        "claude:user::a:/b:/c",
			pointered: true,
			wantErr:   true,
			ambiguous: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parse := parseLegacyFileKey
			if tc.pointered {
				parse = parseLegacyPointerKey
			}
			got, err := parse(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parse(%q) should fail; got %+v", tc.in, got)
				}
				if tc.ambiguous && !errors.Is(err, errAmbiguousLegacyKey) {
					t.Fatalf("parse(%q) error should be errAmbiguousLegacyKey; got %v", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parse(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
