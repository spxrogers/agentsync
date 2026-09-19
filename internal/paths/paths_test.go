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
		{
			// Issue #270: the test-isolation redirect must outrank an ambient
			// AGENTSYNC_HOME, or every test resolving the canonical source lands
			// on the developer's real tree.
			name: "AGENTSYNC_TARGET_ROOT outranks AGENTSYNC_HOME",
			env: map[string]string{
				"AGENTSYNC_TARGET_ROOT": "/tmp/x",
				"AGENTSYNC_HOME":        "/Users/real/.agentsync",
				"HOME":                  "/Users/real",
			},
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

// TestContainsDir pins the lexical containment predicate (spelling only, no
// filesystem access) and, on paths that need not exist, its resolved sibling —
// the two agree whenever no symlink is involved. The symlink and case-folding
// behaviour of the resolved form is covered by the container-gated cli and grok
// tests, which can create real directories, and by TestNormalizeDir_CaseFold.
func TestContainsDir(t *testing.T) {
	cases := []struct {
		name          string
		parent, child string
		want          bool
	}{
		{name: "identical", parent: "/home/alice", child: "/home/alice", want: true},
		{name: "direct child", parent: "/home/alice", child: "/home/alice/.grok", want: true},
		{name: "deep descendant", parent: "/", child: "/home/alice/.grok/skills", want: true},
		{name: "unclean spellings normalize", parent: "/home/alice/", child: "/home/alice/x/../.grok", want: true},
		{name: "parent of parent is not contained", parent: "/home/alice", child: "/home", want: false},
		{name: "sibling with shared prefix is not contained", parent: "/home/alice", child: "/home/alice-evil/x", want: false},
		{name: "unrelated", parent: "/opt/grok", child: "/home/alice", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := paths.ContainsDir(tc.parent, tc.child); got != tc.want {
				t.Fatalf("ContainsDir(%q, %q) = %v, want %v", tc.parent, tc.child, got, tc.want)
			}
			if got := paths.ContainsDirResolved(tc.parent, tc.child); got != tc.want {
				t.Fatalf("ContainsDirResolved(%q, %q) = %v, want %v (no symlinks involved: must agree with ContainsDir)", tc.parent, tc.child, got, tc.want)
			}
		})
	}
}

func TestSameDirResolved(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{name: "identical", a: "/home/alice", b: "/home/alice", want: true},
		{name: "trailing separator", a: "/home/alice/", b: "/home/alice", want: true},
		{name: "dot-dot spelling", a: "/home/alice/x/..", b: "/home/alice", want: true},
		{name: "child is not the same", a: "/home/alice/.grok", b: "/home/alice", want: false},
		{name: "prefix sibling is not the same", a: "/home/alice2", b: "/home/alice", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := paths.SameDirResolved(tc.a, tc.b); got != tc.want {
				t.Fatalf("SameDirResolved(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestAgentHomeOverride pins the sandbox rule for third-party agent home
// variables: honoured for real users, ignored under AGENTSYNC_TARGET_ROOT so a
// redirected run can never escape to the agent's real home (issue #270).
func TestAgentHomeOverride(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "honoured when no redirect is set",
			env:  map[string]string{"GROK_HOME": "/opt/grok", "HOME": "/Users/real"},
			want: "/opt/grok",
		},
		{
			name: "empty when unset",
			env:  map[string]string{"HOME": "/Users/real"},
			want: "",
		},
		{
			name: "ignored under AGENTSYNC_TARGET_ROOT",
			env:  map[string]string{"GROK_HOME": "/opt/grok", "AGENTSYNC_TARGET_ROOT": "/tmp/x", "HOME": "/Users/real"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := paths.AgentHomeOverride(paths.MapEnv(tc.env), "GROK_HOME")
			if got != tc.want {
				t.Fatalf("AgentHomeOverride = %q, want %q", got, tc.want)
			}
		})
	}
}
