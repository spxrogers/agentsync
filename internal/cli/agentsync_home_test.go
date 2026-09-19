package cli_test

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAgentsyncHome_Precedence pins the two halves of the AGENTSYNC_HOME contract
// through the real CLI (issue #270):
//
//   - For a real user (no AGENTSYNC_TARGET_ROOT), AGENTSYNC_HOME is the explicit
//     override and wins over the <HOME>/.agentsync default — unchanged behaviour.
//   - Under AGENTSYNC_TARGET_ROOT (the test-isolation redirect), the redirect
//     wins: the canonical source lands under the redirect root and an ambient
//     AGENTSYNC_HOME is ignored, so no test can escape to a developer's real tree.
//
// Every path here is a tmp dir — HOME included — so the "real user" case never
// touches the container user's home either.
func TestAgentsyncHome_Precedence(t *testing.T) {
	cases := []struct {
		name string
		// env returns the environment given the three tmp dirs.
		env func(home, override, redirect string) map[string]string
		// wantSource / wantAbsent pick which dir must (not) hold the scaffolded
		// canonical source after `init`.
		wantSource func(home, override, redirect string) string
		wantAbsent func(home, override, redirect string) []string
	}{
		{
			name: "real user: AGENTSYNC_HOME overrides the HOME default",
			env: func(home, override, _ string) map[string]string {
				return map[string]string{"HOME": home, "AGENTSYNC_HOME": override}
			},
			wantSource: func(_, override, _ string) string { return override },
			wantAbsent: func(home, _, _ string) []string { return []string{filepath.Join(home, ".agentsync")} },
		},
		{
			name: "real user: no override falls back to HOME/.agentsync",
			env: func(home, _, _ string) map[string]string {
				return map[string]string{"HOME": home}
			},
			wantSource: func(home, _, _ string) string { return filepath.Join(home, ".agentsync") },
			wantAbsent: func(_, override, _ string) []string { return []string{override} },
		},
		{
			name: "redirected run: AGENTSYNC_TARGET_ROOT outranks an ambient AGENTSYNC_HOME",
			env: func(home, override, redirect string) map[string]string {
				return map[string]string{"HOME": home, "AGENTSYNC_HOME": override, "AGENTSYNC_TARGET_ROOT": redirect}
			},
			wantSource: func(_, _, redirect string) string { return filepath.Join(redirect, ".agentsync") },
			wantAbsent: func(home, override, _ string) []string {
				return []string{override, filepath.Join(home, ".agentsync")}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, override, redirect := t.TempDir(), filepath.Join(t.TempDir(), "override"), t.TempDir()
			env := tc.env(home, override, redirect)
			if out, err := runCLI(t, env, "init"); err != nil {
				t.Fatalf("init: %v\n%s", err, out)
			}
			want := tc.wantSource(home, override, redirect)
			if _, err := os.Stat(filepath.Join(want, "agentsync.toml")); err != nil {
				t.Fatalf("canonical source not scaffolded at %s: %v", want, err)
			}
			for _, absent := range tc.wantAbsent(home, override, redirect) {
				if _, err := os.Stat(absent); !os.IsNotExist(err) {
					t.Fatalf("%s must not exist (err=%v): init landed on the wrong root", absent, err)
				}
			}
		})
	}
}
