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
// the internal-looking `check: stat agentsync.toml: stat …/agentsync.toml: not
// a directory`), and both check and doctor accepted an agentsync.toml that was
// a DIRECTORY, reporting "✓ home dir   ok" and leaving the failure to the
// schema load.
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

// TestSourceInitUnreadableNamesTheFileThatFailed pins that check's message for
// the two *Unreadable probe states names the path that actually failed to stat.
//
// One `default:` arm serves sourceInitRootUnreadable AND
// sourceInitConfigUnreadable, so a formatted `stat <root>: ` prefix is right
// for the first and wrong for the second: it printed
// `check: stat <root>: stat <root>/agentsync.toml: too many levels of symbolic
// links` — a self-contradicting message that points a user debugging a symlink
// or permission problem at the directory rather than the file. doctor rendered
// the same state correctly, so check was the outlier on a branch whose subject
// is that the two must not diverge. The fix is to wrap the probe's own
// *fs.PathError unchanged, which is what the single-`stat ` count below pins.
func TestSourceInitUnreadableNamesTheFileThatFailed(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmp,
		"HOME":                  tmp,
		"NO_COLOR":              "1",
		// AGENTSYNC_HOME outranks AGENTSYNC_TARGET_ROOT in paths.AgentsyncHome,
		// so an exported one would aim both commands at the developer's real
		// home instead of the planted tree.
		"AGENTSYNC_HOME": "",
	}
	home := filepath.Join(tmp, ".agentsync")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	// A self-referential symlink: os.Stat follows it and fails with ELOOP,
	// which is neither ErrNotExist nor a successful stat — the one way to reach
	// sourceInitConfigUnreadable without needing to drop privileges.
	cfg := filepath.Join(home, "agentsync.toml")
	if err := os.Symlink(cfg, cfg); err != nil {
		t.Fatal(err)
	}

	_, checkErr := runCLI(t, env, "check")
	if checkErr == nil {
		t.Fatal("check must refuse a source root whose agentsync.toml cannot be stat'd")
	}
	msg := checkErr.Error()
	if !strings.Contains(msg, cfg+":") {
		t.Errorf("check error should name the agentsync.toml that failed to stat; got: %q", msg)
	}
	// Exactly one `stat ` segment: a second one is the re-prefixed root that
	// contradicted the wrapped error.
	if got := strings.Count(msg, "stat "); got != 1 {
		t.Errorf("check error should carry exactly one `stat ` segment (the failing path), got %d: %q", got, msg)
	}

	doctorOut, doctorErr := runCLI(t, env, "doctor")
	if doctorErr == nil {
		t.Fatalf("doctor must refuse the same tree; got:\n%s", doctorOut)
	}
	for _, l := range strings.Split(doctorOut, "\n") {
		if !strings.Contains(l, "home dir") {
			continue
		}
		if !strings.Contains(l, cfg+":") {
			t.Errorf("doctor `home dir` line should name the same failing path check names; got: %q", l)
		}
		return
	}
	t.Fatalf("doctor printed no `home dir` line:\n%s", doctorOut)
}
