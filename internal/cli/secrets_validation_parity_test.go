package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestSecretsValidationParity is the DIVERGENCE GUARD for issue #228.
//
// `agentsync check` and `agentsync doctor` must reach the SAME verdict on every
// [secrets] block, because both now render from the one
// secrets.ValidateConfig. Before the unification each carried its own copy of
// the contract and the copies drifted: `backend = "env"` — which apply resolves
// and check accepts — made doctor exit 1 with `unsupported: "env" (want
// "age")`, and a vault path that could not be stat'd made check exit 1 while
// doctor exited 0 with a false "not yet created" warning.
//
// The fixture home is freshly `init`ed and otherwise clean, so doctor's other
// sections (state, schema, adapter detection, plugins, git backup) never fail;
// the [secrets] block is the only variable.
func TestSecretsValidationParity(t *testing.T) {
	testenv.RequireContainer(t)

	tests := []struct {
		name string
		// block returns the body of the [secrets] table (without the header) for
		// a fixture whose identity file is at idPath and whose agentsync home is
		// at home. An empty return means "write no [secrets] table at all".
		block func(idPath, home string) string
		// identityMode is the mode to create the identity file with; 0 means do
		// not create it.
		identityMode os.FileMode
		// blockVault plants a regular file at <home>/blocker so a vault
		// configured at "blocker/x.age" fails os.Stat with ENOTDIR, not ENOENT.
		blockVault bool
		wantFail   bool
	}{
		{
			name:     "no secrets table",
			block:    func(_, _ string) string { return "" },
			wantFail: false,
		},
		{
			name:     "env backend",
			block:    func(_, _ string) string { return "backend = \"env\"\n" },
			wantFail: false,
		},
		{
			name:     "uppercase env backend",
			block:    func(_, _ string) string { return "backend = \"ENV\"\n" },
			wantFail: false,
		},
		{
			name: "complete age backend",
			block: func(idPath, _ string) string {
				return "backend = \"age\"\nrecipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0o600,
			wantFail:     false,
		},
		{
			name: "uppercase age backend",
			block: func(idPath, _ string) string {
				return "backend = \"AGE\"\nrecipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0o600,
			wantFail:     false,
		},
		{
			name:     "unknown backend",
			block:    func(_, _ string) string { return "backend = \"vault\"\n" },
			wantFail: true,
		},
		{
			name: "age backend with no recipient",
			block: func(idPath, _ string) string {
				return "backend = \"age\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0o600,
			wantFail:     true,
		},
		{
			name: "age backend with no identity_file",
			block: func(_, _ string) string {
				return "backend = \"age\"\nrecipient = \"age1qqqq\"\n"
			},
			wantFail: true,
		},
		{
			name: "age backend whose identity file is absent",
			block: func(idPath, _ string) string {
				return "backend = \"age\"\nrecipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0,
			wantFail:     true,
		},
		{
			name: "age backend with a group-readable identity",
			block: func(idPath, _ string) string {
				return "backend = \"age\"\nrecipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0o644,
			wantFail:     true,
		},
		{
			name: "age backend with an un-stat-able vault path",
			block: func(idPath, _ string) string {
				return "backend = \"age\"\nrecipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n" +
					"file = \"blocker/x.age\"\n"
			},
			identityMode: 0o600,
			blockVault:   true,
			wantFail:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			env := map[string]string{
				"AGENTSYNC_TARGET_ROOT": tmp,
				"HOME":                  tmp,
				"NO_COLOR":              "1",
				// Every ambient input either command's verdict depends on is
				// pinned here, or a developer with one exported in their shell
				// gets a different answer from CI:
				//   - AGENTSYNC_AGE_SKIP_PERM_CHECK=1 makes
				//     secrets.CheckIdentityPermissions return nil (it tests the
				//     value against "1"), so the group-readable row would pass
				//     where CI fails it. "" re-arms the check.
				//   - AGENTSYNC_HOME outranks AGENTSYNC_TARGET_ROOT in
				//     paths.AgentsyncHome, so an exported one would point both
				//     commands at the developer's real home instead of tmp.
				//   - AGENTSYNC_ALLOW_OFFLINE_VERIFY=1 skips check's
				//     resolvability pass over ${secret:…}/${env:…}, changing
				//     which code path produces check's half of the parity claim.
				"AGENTSYNC_AGE_SKIP_PERM_CHECK":  "",
				"AGENTSYNC_HOME":                 "",
				"AGENTSYNC_ALLOW_OFFLINE_VERIFY": "",
			}
			if _, err := runCLI(t, env, "init"); err != nil {
				t.Fatalf("init: %v", err)
			}
			home := filepath.Join(tmp, ".agentsync")
			idPath := filepath.Join(tmp, "age.key")

			if tc.identityMode != 0 {
				if err := os.WriteFile(idPath, []byte("# fixture identity\n"), tc.identityMode); err != nil {
					t.Fatal(err)
				}
				// os.WriteFile applies the process umask; chmod is what actually
				// pins the bits CheckIdentityPermissions reads.
				if err := os.Chmod(idPath, tc.identityMode); err != nil {
					t.Fatal(err)
				}
			}
			if tc.blockVault {
				if err := os.WriteFile(filepath.Join(home, "blocker"), []byte("not a dir\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			body := "[agents]\n"
			if b := tc.block(idPath, home); b != "" {
				body += "[secrets]\n" + b
			}
			if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			checkOut, checkErr := runCLI(t, env, "check")
			doctorOut, doctorErr := runCLI(t, env, "doctor")

			if (checkErr != nil) != (doctorErr != nil) {
				t.Fatalf("check and doctor disagree on this [secrets] block — they render from "+
					"one validator and must not.\ncheck err=%v\ncheck out:\n%s\ndoctor err=%v\ndoctor out:\n%s",
					checkErr, checkOut, doctorErr, doctorOut)
			}
			if (checkErr != nil) != tc.wantFail {
				t.Fatalf("want fail=%v, got check err=%v\ncheck out:\n%s\ndoctor out:\n%s",
					tc.wantFail, checkErr, checkOut, doctorOut)
			}
		})
	}
}
