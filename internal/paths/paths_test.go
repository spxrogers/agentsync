package paths_test

import (
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/paths"
)

func TestHomeDir(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "AGENTSYNC_TARGET_ROOT overrides everything",
			env:  map[string]string{"AGENTSYNC_TARGET_ROOT": "/tmp/redirect", "HOME": "/Users/real"},
			want: "/tmp/redirect",
		},
		{
			name: "falls back to HOME when no override",
			env:  map[string]string{"HOME": "/Users/real"},
			want: "/Users/real",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := paths.HomeDir(paths.MapEnv(tc.env))
			if got != tc.want {
				t.Fatalf("HomeDir = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHomeRelative(t *testing.T) {
	cases := []struct {
		name string
		home string
		in   string
		want string
	}{
		{
			name: "dest under home is normalized",
			home: "/Users/alice",
			in:   "/Users/alice/.claude.json",
			want: "${HOME}/.claude.json",
		},
		{
			name: "nested dest under home",
			home: "/Users/alice",
			in:   "/Users/alice/.config/opencode/opencode.json",
			want: "${HOME}/.config/opencode/opencode.json",
		},
		{
			name: "dest outside home is left absolute",
			home: "/Users/alice",
			in:   "/etc/agentsync/global.json",
			want: "/etc/agentsync/global.json",
		},
		{
			name: "empty home is no-op",
			home: "",
			in:   "/anywhere",
			want: "/anywhere",
		},
		{
			name: "exact home returns ${HOME}/.",
			home: "/Users/alice",
			in:   "/Users/alice",
			want: "${HOME}/.",
		},
		{
			name: "parent of home stays absolute (no escape)",
			home: "/Users/alice/.agentsync",
			in:   "/Users/alice/.claude.json",
			want: "/Users/alice/.claude.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := paths.HomeRelative(tc.home, tc.in)
			if got != tc.want {
				t.Fatalf("HomeRelative(%q,%q) = %q, want %q", tc.home, tc.in, got, tc.want)
			}
		})
	}
}

func TestAgentsyncHome(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "AGENTSYNC_HOME explicit override",
			env:  map[string]string{"AGENTSYNC_HOME": "/explicit/path", "HOME": "/Users/real"},
			want: "/explicit/path",
		},
		{
			name: "default ~/.agentsync under HOME",
			env:  map[string]string{"HOME": "/Users/real"},
			want: filepath.Join("/Users/real", ".agentsync"),
		},
		{
			name: "AGENTSYNC_TARGET_ROOT shifts default",
			env:  map[string]string{"AGENTSYNC_TARGET_ROOT": "/tmp/x", "HOME": "/Users/real"},
			want: filepath.Join("/tmp/x", ".agentsync"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := paths.AgentsyncHome(paths.MapEnv(tc.env))
			if got != tc.want {
				t.Fatalf("AgentsyncHome = %q, want %q", got, tc.want)
			}
		})
	}
}
