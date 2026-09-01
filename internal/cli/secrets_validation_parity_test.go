package cli_test

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// secretsReportLabels are the four padded columns doctor renders the [secrets]
// validator's fields under (doctor.go's secretsFieldLabel). They are spelled out
// here rather than reached for through the unexported helper because this guard
// is on doctor's RENDERED output — the thing a user reads and the thing a glyph
// regression would break.
var secretsReportLabels = []string{"backend    ", "recipient  ", "identity   ", "age file   "}

// The four glyphs doctor's readiness lines can carry, spelled out for the same
// reason the labels above are: this guard is on the RENDERED output. They are
// ui.GlyphOK / GlyphErr / GlyphWarn / GlyphInfo.
const (
	glyphOK   = "✓"
	glyphFail = "✗"
	glyphWarn = "⚠"
	glyphInfo = "•"
)

// doctorSecretsGlyphs maps each [secrets] report label doctor printed to the
// glyph on its line.
//
// It exists because exit-code parity pins only the ✗ tier: SeverityWarn and
// SeverityInfo both rendering as okCheck, or infoCheck rendering a ✓, changes
// nothing any other assertion in this file reads — and `✓ age file   … not yet
// created` is a false pass of exactly the #228 kind. The severity→glyph mapping
// in doctor.go is the last hand-written step between the single validator and
// what a user actually reads, so it is pinned here per row.
//
// The returned map's KEY SET is meaningful too: a label appears only when
// ValidateConfig emitted a finding for that field, so comparing the whole map
// also pins which fields get a line at all.
func doctorSecretsGlyphs(out string) map[string]string {
	got := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		for _, g := range []string{glyphOK, glyphFail, glyphWarn, glyphInfo} {
			rest, ok := strings.CutPrefix(l, "  "+g+" ")
			if !ok {
				continue
			}
			for _, label := range secretsReportLabels {
				if strings.HasPrefix(rest, label) {
					got[label] = g
				}
			}
		}
	}
	return got
}

// doctorFailLines splits doctor's failing REPORT lines into the ones sitting on
// a [secrets] label and the ones sitting on any other label.
//
// failCheck renders "  <glyph> <padded label><status>" and the fixture sets
// NO_COLOR=1, so a failing report line starts with the bare "  \u2717 ". doctor's
// closing "N issue(s) detected" summary carries the same glyph at column 0, so
// the two-space indent is what keeps it out of this classification.
func doctorFailLines(out string) (secretsFails, otherFails []string) {
	const failPrefix = "  \u2717 "
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, failPrefix) {
			continue
		}
		rest := strings.TrimPrefix(l, failPrefix)
		isSecrets := false
		for _, label := range secretsReportLabels {
			if strings.HasPrefix(rest, label) {
				isSecrets = true
				break
			}
		}
		if isSecrets {
			secretsFails = append(secretsFails, l)
		} else {
			otherFails = append(otherFails, l)
		}
	}
	return secretsFails, otherFails
}

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
//
// It enforces three things per row, not one:
//   - check and doctor agree on the VERDICT (both exit zero, or both do not),
//     and that verdict is the expected one;
//   - doctor's ✗ actually lands on a [secrets] report line when the row is
//     supposed to fail. Exit-code parity alone would pass a doctor that stopped
//     rendering the glyph but still counted the issue, and it would call a row
//     "parity" when doctor's non-zero exit came from an unrelated section;
//   - NO other doctor report line carries a ✗, on any row. That is the "other
//     sections never fail" sentence above, asserted rather than assumed: if one
//     of them starts failing, this test says so instead of blaming the
//     [secrets] block for a divergence that is not there.
//
// It also pins doctor's severity→glyph mapping per row (wantGlyphs). Exit-code
// parity alone covers only the ✗ tier, so a Warn or Info line rendered as ✓
// would slip through everything above while telling a user their config is
// healthy — see doctorSecretsGlyphs.
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
		// dirIdentity plants a DIRECTORY at the identity path instead of a
		// regular file. Mutually exclusive with identityMode. It is created
		// 0o700 so it also clears CheckIdentityPermissions, leaving the
		// regular-file rule as the only arm that can catch it.
		dirIdentity bool
		// blockVault plants a regular file at <home>/blocker so a vault
		// configured at "blocker/x.age" fails os.Stat with ENOTDIR, not ENOENT.
		blockVault bool
		// dirVault plants a DIRECTORY at the default vault path, which stats
		// fine and so reaches neither the "not yet created" nor the "not
		// readable" arm.
		dirVault bool
		// bareTable writes a `[secrets]` HEADER with nothing under it. It is
		// distinct from a block func returning "", which writes no table at
		// all, and the difference is the whole point of the row that sets it.
		bareTable bool
		wantFail  bool
		// wantGlyphs is the complete set of [secrets] report lines doctor must
		// print for this row, label → glyph. Compared for equality, so a
		// missing or extra line fails too.
		wantGlyphs map[string]string
	}{
		{
			name:       "no secrets table",
			block:      func(_, _ string) string { return "" },
			wantFail:   false,
			wantGlyphs: map[string]string{"backend    ": glyphInfo},
		},
		{
			// THE DECODER ROWS. ValidateConfig's empty-backend arm splits on
			// `cfg == (source.SecretsConfig{})`, and the user guide and the
			// configuration reference both promise a specific answer for three
			// TOML spellings that arm cannot tell apart — "no [secrets] block at
			// all (or a bare [secrets] header, which parses identically)" and
			// "an absent OR EMPTY backend". Nothing asserted that, because
			// TestValidateConfig builds source.SecretsConfig with Go struct
			// literals, which cannot distinguish `backend = ""` from an absent
			// key: the claim is about the DECODER, so only a row that starts
			// from real TOML can back it. These three do, end to end through
			// both commands.
			//
			// A bare header: the promise is that it is indistinguishable from no
			// table, so the expectation here is deliberately identical to the
			// row above, glyph for glyph.
			name:       "bare [secrets] header",
			block:      func(_, _ string) string { return "" },
			bareTable:  true,
			wantFail:   false,
			wantGlyphs: map[string]string{"backend    ": glyphInfo},
		},
		{
			// An EMPTY backend and no other key: `backend = ""` decodes to the
			// same zero-valued SecretsConfig as an absent key, so this is the
			// informational tier too, not the warning one. If the decoder ever
			// stopped round-tripping "" to "" — or a loader started defaulting a
			// sibling key like `file`, making the struct non-zero — this row
			// flips to ⚠ and says so.
			name:       "empty backend string alone",
			block:      func(_, _ string) string { return "backend = \"\"\n" },
			wantFail:   false,
			wantGlyphs: map[string]string{"backend    ": glyphInfo},
		},
		{
			// The other half of "absent or empty": an empty backend beside a key
			// that carries a VALUE is a block that was never switched on, so it
			// takes the warning tier — the same answer as omitting `backend`
			// entirely (the "secrets table with no backend" row below). That
			// equality is the doc claim; asserting the pair is what pins it.
			name:       "empty backend string with a recipient",
			block:      func(_, _ string) string { return "backend = \"\"\nrecipient = \"age1qqqq\"\n" },
			wantFail:   false,
			wantGlyphs: map[string]string{"backend    ": glyphWarn},
		},
		{
			// The residual the rest of this branch's thesis targets: a [secrets]
			// block carrying recipient and identity_file but no backend used to
			// print "• backend    not configured (skip — no [secrets] block)" —
			// a sentence that is false about the block it is describing — while
			// SelectBackend hands apply a NopResolver for it. It now warns.
			name: "secrets table with no backend",
			block: func(idPath, _ string) string {
				return "recipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0o600,
			// Non-fatal on both surfaces: with no ${secret:…} reference in the
			// source this config applies cleanly, so failing `check` here would
			// be a new check/apply divergence.
			wantFail:   false,
			wantGlyphs: map[string]string{"backend    ": glyphWarn},
		},
		{
			name:       "env backend",
			block:      func(_, _ string) string { return "backend = \"env\"\n" },
			wantFail:   false,
			wantGlyphs: map[string]string{"backend    ": glyphOK},
		},
		{
			name:       "uppercase env backend",
			block:      func(_, _ string) string { return "backend = \"ENV\"\n" },
			wantFail:   false,
			wantGlyphs: map[string]string{"backend    ": glyphOK},
		},
		{
			name: "complete age backend",
			block: func(idPath, _ string) string {
				return "backend = \"age\"\nrecipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0o600,
			wantFail:     false,
			// The ⚠ here is the load-bearing one: the vault has not been created
			// yet, which is legitimate on a fresh install and must not read as a
			// pass. `✓ age file   … not yet created` is the false green this row
			// exists to catch.
			wantGlyphs: map[string]string{
				"backend    ": glyphOK, "recipient  ": glyphOK,
				"identity   ": glyphOK, "age file   ": glyphWarn,
			},
		},
		{
			name: "uppercase age backend",
			block: func(idPath, _ string) string {
				return "backend = \"AGE\"\nrecipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0o600,
			wantFail:     false,
			wantGlyphs: map[string]string{
				"backend    ": glyphOK, "recipient  ": glyphOK,
				"identity   ": glyphOK, "age file   ": glyphWarn,
			},
		},
		{
			name:       "unknown backend",
			block:      func(_, _ string) string { return "backend = \"vault\"\n" },
			wantFail:   true,
			wantGlyphs: map[string]string{"backend    ": glyphFail},
		},
		{
			name: "age backend with no recipient",
			block: func(idPath, _ string) string {
				return "backend = \"age\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0o600,
			wantFail:     true,
			wantGlyphs: map[string]string{
				"backend    ": glyphOK, "recipient  ": glyphFail,
				"identity   ": glyphOK, "age file   ": glyphWarn,
			},
		},
		{
			name: "age backend with no identity_file",
			block: func(_, _ string) string {
				return "backend = \"age\"\nrecipient = \"age1qqqq\"\n"
			},
			wantFail: true,
			wantGlyphs: map[string]string{
				"backend    ": glyphOK, "recipient  ": glyphOK,
				"identity   ": glyphFail, "age file   ": glyphWarn,
			},
		},
		{
			name: "age backend whose identity file is absent",
			block: func(idPath, _ string) string {
				return "backend = \"age\"\nrecipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0,
			wantFail:     true,
			wantGlyphs: map[string]string{
				"backend    ": glyphOK, "recipient  ": glyphOK,
				"identity   ": glyphFail, "age file   ": glyphWarn,
			},
		},
		{
			name: "age backend with a group-readable identity",
			block: func(idPath, _ string) string {
				return "backend = \"age\"\nrecipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0o644,
			wantFail:     true,
			wantGlyphs: map[string]string{
				"backend    ": glyphOK, "recipient  ": glyphOK,
				"identity   ": glyphFail, "age file   ": glyphWarn,
			},
		},
		{
			// The identity twin of the vault row below. A directory stats fine
			// and at 0o700 clears the permission gate too, so both surfaces
			// printed `✓ identity   ok` for a path os.ReadFile then fails on
			// with `is a directory` — the same false green the vault row exists
			// to catch, on the path that is actually read.
			//
			// The FIFO case cannot be driven from here: it would report ✓ the
			// same way, but a command that went on to read it would block
			// forever rather than fail. It is pinned one layer down, where the
			// validator only stats — see TestValidateConfigFIFOIdentity in
			// internal/secrets.
			name: "age backend with a directory at the identity path",
			block: func(idPath, _ string) string {
				return "backend = \"age\"\nrecipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n"
			},
			dirIdentity: true,
			wantFail:    true,
			wantGlyphs: map[string]string{
				"backend    ": glyphOK, "recipient  ": glyphOK,
				"identity   ": glyphFail, "age file   ": glyphWarn,
			},
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
			wantGlyphs: map[string]string{
				"backend    ": glyphOK, "recipient  ": glyphOK,
				"identity   ": glyphOK, "age file   ": glyphFail,
			},
		},
		{
			// A directory at the vault path stats fine, so it reached neither
			// the "not yet created" nor the "not readable" arm and reported ✓ on
			// both surfaces — the regular-file rule probeSourceInit adopted for
			// agentsync.toml, carried to the vault.
			name: "age backend with a directory at the vault path",
			block: func(idPath, _ string) string {
				return "backend = \"age\"\nrecipient = \"age1qqqq\"\nidentity_file = \"" + idPath + "\"\n"
			},
			identityMode: 0o600,
			dirVault:     true,
			wantFail:     true,
			wantGlyphs: map[string]string{
				"backend    ": glyphOK, "recipient  ": glyphOK,
				"identity   ": glyphOK, "age file   ": glyphFail,
			},
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
			if tc.dirIdentity {
				if err := os.MkdirAll(idPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.blockVault {
				if err := os.WriteFile(filepath.Join(home, "blocker"), []byte("not a dir\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.dirVault {
				// The DEFAULT vault path ([secrets].file unset), so the row does
				// not have to restate it.
				if err := os.MkdirAll(filepath.Join(home, "secrets", "secrets.age"), 0o700); err != nil {
					t.Fatal(err)
				}
			}

			body := "[agents]\n"
			if b := tc.block(idPath, home); b != "" {
				body += "[secrets]\n" + b
			} else if tc.bareTable {
				body += "[secrets]\n"
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

			secretsFails, otherFails := doctorFailLines(doctorOut)
			if len(otherFails) > 0 {
				t.Errorf("doctor failed a section other than [secrets] on a fixture that is "+
					"otherwise freshly `init`ed — the parity claim above is then about the "+
					"wrong block:\n  %s\ndoctor out:\n%s",
					strings.Join(otherFails, "\n  "), doctorOut)
			}
			if got := len(secretsFails) > 0; got != tc.wantFail {
				t.Errorf("doctor [secrets] report lines carrying ✗ = %v, want %v — check and "+
					"doctor must not only reach the same exit code but blame the same "+
					"block.\nfailing [secrets] lines: %v\ndoctor out:\n%s",
					got, tc.wantFail, secretsFails, doctorOut)
			}
			if got := doctorSecretsGlyphs(doctorOut); !maps.Equal(got, tc.wantGlyphs) {
				t.Errorf("doctor [secrets] glyphs = %v, want %v — every tier the validator "+
					"emits must reach the user as its own glyph; a ⚠ or • rendered as ✓ is a "+
					"false pass that exit-code parity cannot see.\ndoctor out:\n%s",
					got, tc.wantGlyphs, doctorOut)
			}
		})
	}
}
