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

func TestFromHomeRelative(t *testing.T) {
	cases := []struct {
		name     string
		userHome string
		stored   string
		want     string
	}{
		{"expands ${HOME}/ prefix", "/home/alice", "${HOME}/.claude.json", filepath.Join("/home/alice", ".claude.json")},
		{"nested ${HOME}/ prefix", "/home/alice", "${HOME}/.config/opencode/opencode.json", filepath.Join("/home/alice", ".config/opencode/opencode.json")},
		{"bare ${HOME}", "/home/alice", "${HOME}", "/home/alice"},
		{"absolute stored path unchanged", "/home/alice", "/etc/agentsync/global.json", "/etc/agentsync/global.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := paths.FromHomeRelative(tc.userHome, tc.stored)
			if got != tc.want {
				t.Fatalf("FromHomeRelative(%q,%q) = %q, want %q", tc.userHome, tc.stored, got, tc.want)
			}
		})
	}
}

// TestHomeRelative_RoundTrips proves HomeRelative and FromHomeRelative are
// inverses for dest paths under home — the invariant `agent disable --purge`
// relies on to turn a stored key back into a real path.
func TestHomeRelative_RoundTrips(t *testing.T) {
	home := "/home/alice"
	abs := filepath.Join(home, ".config", "opencode", "opencode.json")
	stored := paths.HomeRelative(home, abs)
	if stored != "${HOME}/.config/opencode/opencode.json" {
		t.Fatalf("HomeRelative = %q", stored)
	}
	back := paths.FromHomeRelative(home, stored)
	if back != abs {
		t.Fatalf("round-trip mismatch: %q -> %q -> %q", abs, stored, back)
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
