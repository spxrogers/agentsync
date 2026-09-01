package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSourceInitProbeAgreement pins that check and doctor classify a broken
// source root the same way. Three copies of this probe disagreed before #228:
// check accepted a ~/.agentsync that was a regular FILE (then failed later with
// the internal-looking `stat agentsync.toml: not a directory`), and both check
// and doctor accepted an agentsync.toml that was a DIRECTORY, reporting
// "✓ home dir   ok" and leaving the failure to the schema load.
func TestSourceInitProbeAgreement(t *testing.T) {
	tests := []struct {
		name string
		// plant builds the broken tree under tmp. It is called INSTEAD of `init`.
		plant func(t *testing.T, tmp string)
		// wantCheckContains is a substring the check error must carry.
		wantCheckContains string
		// wantDoctorContains is a substring doctor's output must carry.
		wantDoctorContains string
	}{
		{
			name:               "home is missing",
			plant:              func(_ *testing.T, _ string) {},
			wantCheckContains:  "does not exist",
			wantDoctorContains: "agentsync init",
		},
		{
			name: "home is a regular file",
			plant: func(t *testing.T, tmp string) {
				if err := os.WriteFile(filepath.Join(tmp, ".agentsync"), []byte("x\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantCheckContains:  "is not a directory",
			wantDoctorContains: "is not a directory",
		},
		{
			name: "agentsync.toml is missing",
			plant: func(t *testing.T, tmp string) {
				if err := os.MkdirAll(filepath.Join(tmp, ".agentsync"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantCheckContains:  "agentsync.toml",
			wantDoctorContains: "agentsync.toml",
		},
		{
			name: "agentsync.toml is a directory",
			plant: func(t *testing.T, tmp string) {
				if err := os.MkdirAll(filepath.Join(tmp, ".agentsync", "agentsync.toml"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantCheckContains:  "agentsync.toml",
			wantDoctorContains: "agentsync.toml",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			env := map[string]string{
				"AGENTSYNC_TARGET_ROOT": tmp,
				"HOME":                  tmp,
				"NO_COLOR":              "1",
				// AGENTSYNC_HOME outranks AGENTSYNC_TARGET_ROOT in
				// paths.AgentsyncHome, so an exported one would aim both commands
				// at the developer's real home instead of the planted tree. No
				// row here touches [secrets], so the perm-check opt-out is not in
				// play.
				"AGENTSYNC_HOME": "",
			}
			tc.plant(t, tmp)

			_, checkErr := runCLI(t, env, "check")
			if checkErr == nil {
				t.Fatal("check must refuse an uninitialized or broken source root")
			}
			if !strings.Contains(checkErr.Error(), tc.wantCheckContains) {
				t.Errorf("check error %q should contain %q", checkErr, tc.wantCheckContains)
			}
			if !strings.Contains(checkErr.Error(), "init") {
				t.Errorf("check error %q should point at `init`", checkErr)
			}

			doctorOut, doctorErr := runCLI(t, env, "doctor")
			if doctorErr == nil {
				t.Fatalf("doctor must refuse the same tree; got:\n%s", doctorOut)
			}
			homeLine := ""
			for _, l := range strings.Split(doctorOut, "\n") {
				if strings.Contains(l, "home dir") {
					homeLine = l
					break
				}
			}
			if homeLine == "" {
				t.Fatalf("doctor printed no `home dir` line:\n%s", doctorOut)
			}
			if strings.Contains(homeLine, "home dir   ok") {
				t.Errorf("doctor reported `home dir   ok` on a broken tree: %q", homeLine)
			}
			if !strings.Contains(homeLine, tc.wantDoctorContains) {
				t.Errorf("doctor `home dir` line %q should contain %q", homeLine, tc.wantDoctorContains)
			}
		})
	}
}
