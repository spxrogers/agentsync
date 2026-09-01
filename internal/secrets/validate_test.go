package secrets_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// findingFor returns the single finding for field, or fails. It is the
// at-most-one-finding-per-field half of the contract: every assertion below
// reads one field's verdict, and a duplicate finding fails the lookup rather
// than being silently picked from.
func findingFor(t *testing.T, fs []secrets.Finding, field secrets.Field) secrets.Finding {
	t.Helper()
	var got []secrets.Finding
	for _, f := range fs {
		if f.Field == field {
			got = append(got, f)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 finding for field %q, got %d: %+v", field, len(got), fs)
	}
	return got[0]
}

// TestNormalizeBackend pins that the validator folds a backend name EXACTLY the
// way SelectBackend does — lower-cased, not trimmed. A validator that trimmed
// would bless a `" age"` that apply resolves as NopResolver; a validator that
// did not lower-case rejects a `"AGE"` that apply resolves as age (issue #228).
func TestNormalizeBackend(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "lowercase age is unchanged", in: "age", want: "age"},
		{name: "uppercase age folds", in: "AGE", want: "age"},
		{name: "mixed case age folds", in: "Age", want: "age"},
		{name: "uppercase env folds", in: "ENV", want: "env"},
		{name: "empty stays empty", in: "", want: ""},
		{name: "leading space is NOT trimmed", in: " age", want: " age"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := secrets.NormalizeBackend(tc.in); got != tc.want {
				t.Fatalf("NormalizeBackend(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSelectBackendUsesNormalizeBackend pins that the resolver apply renders
// through, the validator `check`/`doctor` report from, and the gate the
// `secret` group refuses on all agree on every spelling of the backend name —
// the exact axis on which check/doctor disagreed with apply before #228.
//
// All three are exercised on ONE fixture per spelling, which is the point: the
// header used to claim the validator was covered while the body only called
// SelectBackend. The agreement asserted is not "same verdict" (the three have
// deliberately different rules — RequireAgeVault refuses the perfectly valid
// env backend, because there is no vault for it to manage) but "same reading of
// the NAME":
//   - a spelling SelectBackend resolves to a real backend is one ValidateConfig
//     blesses (SeverityOK on the backend field);
//   - a spelling that degrades to NopResolver is one ValidateConfig does NOT
//     bless — Fail for a typo, Warn for an unset backend in a block that
//     carries other keys;
//   - RequireAgeVault accepts exactly the spellings that select an AgeBackend.
func TestSelectBackendUsesNormalizeBackend(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		want    string // %T of the returned Resolver
		// wantBackendSeverity is ValidateConfig's verdict on the BACKEND field
		// for the same spelling. (The fixture's identity file does not exist, so
		// the age rows also carry an identity_file failure; only the backend
		// field's finding is read here.)
		wantBackendSeverity secrets.Severity
		// wantVaultOK is whether RequireAgeVault admits the same spelling.
		wantVaultOK bool
	}{
		{
			name: "age", backend: "age", want: "*secrets.AgeBackend",
			wantBackendSeverity: secrets.SeverityOK, wantVaultOK: true,
		},
		{
			name: "AGE folds to age", backend: "AGE", want: "*secrets.AgeBackend",
			wantBackendSeverity: secrets.SeverityOK, wantVaultOK: true,
		},
		{
			name: "env", backend: "env", want: "secrets.EnvBackend",
			wantBackendSeverity: secrets.SeverityOK, wantVaultOK: false,
		},
		{
			name: "ENV folds to env", backend: "ENV", want: "secrets.EnvBackend",
			wantBackendSeverity: secrets.SeverityOK, wantVaultOK: false,
		},
		{
			// The fixture sets recipient and identity_file, so this is the
			// "configured a vault and never switched it on" block: NopResolver
			// at apply time, and a warning — not a pass — from the validator.
			name: "empty is NOT env", backend: "", want: "secrets.NopResolver",
			wantBackendSeverity: secrets.SeverityWarn, wantVaultOK: false,
		},
		{
			name: "unknown degrades", backend: "vault", want: "secrets.NopResolver",
			wantBackendSeverity: secrets.SeverityFail, wantVaultOK: false,
		},
		{
			// NormalizeBackend does not trim, so this is a typo on all three
			// surfaces — a validator that trimmed would bless a spelling apply
			// resolves as NopResolver.
			name: "leading space is not trimmed", backend: " age", want: "secrets.NopResolver",
			wantBackendSeverity: secrets.SeverityFail, wantVaultOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			userHome := t.TempDir()
			home := filepath.Join(userHome, ".agentsync")
			idPath := filepath.Join(userHome, "age.key") // deliberately not created
			cfg := source.SecretsConfig{Backend: tc.backend, Recipient: "age1qqqq", IdentityFile: idPath}

			got := secrets.SelectBackend(cfg, home, userHome)
			if gotType := typeName(got); gotType != tc.want {
				t.Fatalf("SelectBackend(backend=%q) = %s, want %s", tc.backend, gotType, tc.want)
			}

			f := findingFor(t, secrets.ValidateConfig(cfg, home, userHome), secrets.FieldBackend)
			if f.Severity != tc.wantBackendSeverity {
				t.Errorf("ValidateConfig(backend=%q) backend severity = %s, want %s — the validator "+
					"and SelectBackend must read the same spelling the same way (message: %s)",
					tc.backend, f.Severity, tc.wantBackendSeverity, f.Message)
			}

			vaultErr := secrets.RequireAgeVault(cfg, "secret list", secrets.VaultRead)
			if (vaultErr == nil) != tc.wantVaultOK {
				t.Errorf("RequireAgeVault(backend=%q) = %v, want ok=%v — the `secret` group must admit "+
					"exactly the spellings SelectBackend resolves to an age backend",
					tc.backend, vaultErr, tc.wantVaultOK)
			}
		})
	}
}

func typeName(v any) string {
	switch v.(type) {
	case *secrets.AgeBackend:
		return "*secrets.AgeBackend"
	case secrets.EnvBackend:
		return "secrets.EnvBackend"
	case secrets.NopResolver:
		return "secrets.NopResolver"
	}
	return "unknown"
}

// TestValidateConfig walks every branch of the [secrets] contract. The
// (field, severity) pairs are what the two consumption shapes act on — a
// severity for an error-or-nil surface to fail on, a field to hang a report
// line off — and wantFails pins the error-or-nil verdict directly. The message
// substrings are the load-bearing words a user reads; "too permissive" is also
// the one already pinned by a CLI test (internal/cli/doctor_test.go).
func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name string
		// mutate customises the fixture: it receives the temp agentsync home and
		// the default identity path and returns the config to validate.
		mutate       func(t *testing.T, home, idPath string) source.SecretsConfig
		wantField    secrets.Field
		wantSeverity secrets.Severity
		wantContains string
		// wantNotContains, when set, must NOT appear in the field's message.
		wantNotContains string
		// wantFails is the total number of SeverityFail findings.
		wantFails int
	}{
		{
			// Pinned as the WHOLE message, and with the warning twin's wording
			// pinned absent, for the same reason that row does it: these two
			// arms differ only in which sentence they print about the same empty
			// backend, so a substring loose enough to match either one would let
			// them swap without failing. "no [secrets] block" is the sentence
			// that is TRUE here and false one row down — the split, from this
			// side.
			name: "no backend is informational",
			mutate: func(_ *testing.T, _, _ string) source.SecretsConfig {
				return source.SecretsConfig{}
			},
			wantField:       secrets.FieldBackend,
			wantSeverity:    secrets.SeverityInfo,
			wantContains:    "not configured (skip — no [secrets] block)",
			wantNotContains: "will not resolve",
			wantFails:       0,
		},
		{
			// The branch's own thesis applied to itself: a [secrets] block with
			// recipient and identity_file but no backend used to report
			// "not configured (skip — no [secrets] block)" — a sentence that is
			// false about the block sitting right there — at SeverityInfo, so
			// doctor printed it as a neutral bullet and nothing anywhere said
			// that SelectBackend will hand apply a NopResolver.
			name: "a [secrets] block with no backend warns instead of claiming there is no block",
			mutate: func(t *testing.T, _, idPath string) source.SecretsConfig {
				writeIdentity(t, idPath, 0o600)
				return source.SecretsConfig{Recipient: "age1qqqq", IdentityFile: idPath}
			},
			wantField:    secrets.FieldBackend,
			wantSeverity: secrets.SeverityWarn,
			wantContains: "not set — ${secret:…} will not resolve",
			// The false sentence, pinned as gone rather than merely reworded.
			wantNotContains: "no [secrets] block",
			// Warn, not Fail: with no ${secret:…} reference in the source this
			// config applies cleanly, so failing `check` here would be a NEW
			// check/apply divergence. See ValidateConfig's empty-backend arm.
			wantFails: 0,
		},
		{
			// ANY set key makes source.SecretsConfig non-zero, so the warning is
			// not keyed on recipient/identity_file specifically: a block holding
			// only `file` — the one key that is defaulted anyway — is still a
			// block whose ${secret:…} will not resolve. (A `[secrets]` header
			// with nothing under it is the one case that stays informational: it
			// unmarshals to the same zero value as no block at all, and there is
			// nothing after parsing that could tell the two apart.)
			name: "a [secrets] block with only a defaulted file key still warns",
			mutate: func(_ *testing.T, _, _ string) source.SecretsConfig {
				return source.SecretsConfig{File: "secrets/secrets.age"}
			},
			wantField:    secrets.FieldBackend,
			wantSeverity: secrets.SeverityWarn,
			wantContains: "not set",
			wantFails:    0,
		},
		{
			name: "env backend is supported",
			mutate: func(_ *testing.T, _, _ string) source.SecretsConfig {
				return source.SecretsConfig{Backend: "env"}
			},
			wantField:    secrets.FieldBackend,
			wantSeverity: secrets.SeverityOK,
			wantContains: "env",
			wantFails:    0,
		},
		{
			name: "ENV backend folds and is supported",
			mutate: func(_ *testing.T, _, _ string) source.SecretsConfig {
				return source.SecretsConfig{Backend: "ENV"}
			},
			wantField:    secrets.FieldBackend,
			wantSeverity: secrets.SeverityOK,
			wantContains: "env",
			wantFails:    0,
		},
		{
			name: "unknown backend fails",
			mutate: func(_ *testing.T, _, _ string) source.SecretsConfig {
				return source.SecretsConfig{Backend: "vault"}
			},
			wantField:    secrets.FieldBackend,
			wantSeverity: secrets.SeverityFail,
			wantContains: `want "age" or "env"`,
			wantFails:    1,
		},
		{
			name: "AGE backend folds and is accepted",
			mutate: func(t *testing.T, _, idPath string) source.SecretsConfig {
				writeIdentity(t, idPath, 0o600)
				return source.SecretsConfig{Backend: "AGE", Recipient: "age1qqqq", IdentityFile: idPath}
			},
			wantField:    secrets.FieldBackend,
			wantSeverity: secrets.SeverityOK,
			wantContains: "age",
			wantFails:    0,
		},
		{
			name: "age without recipient fails on recipient",
			mutate: func(t *testing.T, _, idPath string) source.SecretsConfig {
				writeIdentity(t, idPath, 0o600)
				return source.SecretsConfig{Backend: "age", IdentityFile: idPath}
			},
			wantField:    secrets.FieldRecipient,
			wantSeverity: secrets.SeverityFail,
			wantContains: "missing — required for backend",
			wantFails:    1,
		},
		{
			name: "age without identity_file fails on identity_file",
			mutate: func(_ *testing.T, _, _ string) source.SecretsConfig {
				return source.SecretsConfig{Backend: "age", Recipient: "age1qqqq"}
			},
			wantField:    secrets.FieldIdentityFile,
			wantSeverity: secrets.SeverityFail,
			wantContains: "missing — required for backend",
			wantFails:    1,
		},
		{
			name: "age with an absent identity file fails",
			mutate: func(_ *testing.T, _, idPath string) source.SecretsConfig {
				return source.SecretsConfig{Backend: "age", Recipient: "age1qqqq", IdentityFile: idPath}
			},
			wantField:    secrets.FieldIdentityFile,
			wantSeverity: secrets.SeverityFail,
			wantContains: "not readable",
			wantFails:    1,
		},
		{
			name: "age with a group-readable identity fails",
			mutate: func(t *testing.T, _, idPath string) source.SecretsConfig {
				writeIdentity(t, idPath, 0o644)
				return source.SecretsConfig{Backend: "age", Recipient: "age1qqqq", IdentityFile: idPath}
			},
			wantField:    secrets.FieldIdentityFile,
			wantSeverity: secrets.SeverityFail,
			wantContains: "too permissive",
			wantFails:    1,
		},
		{
			// A DIRECTORY at the identity path stats fine, and at 0o700 it also
			// clears CheckIdentityPermissions (0o700&0o077 == 0), so before the
			// regular-file arm neither of validateIdentity's failure arms could
			// see it: `doctor` printed `✓ identity   ok` and `check` exited 0.
			// The identity is the path age.ParseIdentities is fed through
			// os.ReadFile (internal/secrets/age.go), so a directory is not an
			// identity — the same rule validateAgeFile applies to the vault, and
			// probeSourceInit to agentsync.toml, finally carried to the third
			// path this package stats before something else reads it.
			name: "age with a directory at the identity path fails",
			mutate: func(t *testing.T, _, idPath string) source.SecretsConfig {
				if err := os.MkdirAll(idPath, 0o700); err != nil {
					t.Fatal(err)
				}
				return source.SecretsConfig{Backend: "age", Recipient: "age1qqqq", IdentityFile: idPath}
			},
			wantField:    secrets.FieldIdentityFile,
			wantSeverity: secrets.SeverityFail,
			wantContains: "not a regular file",
			wantFails:    1,
		},
		{
			// ORDERING, pinned: 0o755 is an ordinary mode for a directory and a
			// group/other-readable one for a file, so both of validateIdentity's
			// failure arms match it. The shape arm must answer, because
			// "too permissive … chmod 600" would name the wrong problem and hand
			// the user a remedy that does not apply to a directory.
			name: "a 0755 directory identity is named as the wrong shape, not as too permissive",
			mutate: func(t *testing.T, _, idPath string) source.SecretsConfig {
				if err := os.MkdirAll(idPath, 0o755); err != nil {
					t.Fatal(err)
				}
				// MkdirAll applies the umask; chmod is what pins the bits.
				if err := os.Chmod(idPath, 0o755); err != nil {
					t.Fatal(err)
				}
				return source.SecretsConfig{Backend: "age", Recipient: "age1qqqq", IdentityFile: idPath}
			},
			wantField:       secrets.FieldIdentityFile,
			wantSeverity:    secrets.SeverityFail,
			wantContains:    "not a regular file",
			wantNotContains: "too permissive",
			wantFails:       1,
		},
		{
			name: "age with an absent vault only warns",
			mutate: func(t *testing.T, _, idPath string) source.SecretsConfig {
				writeIdentity(t, idPath, 0o600)
				return source.SecretsConfig{Backend: "age", Recipient: "age1qqqq", IdentityFile: idPath}
			},
			wantField:    secrets.FieldFile,
			wantSeverity: secrets.SeverityWarn,
			wantContains: "not yet created",
			wantFails:    0,
		},
		{
			name: "age with an un-stat-able vault fails",
			mutate: func(t *testing.T, home, idPath string) source.SecretsConfig {
				writeIdentity(t, idPath, 0o600)
				// A regular file where the vault path's parent directory would be,
				// so os.Stat fails with ENOTDIR rather than ENOENT. doctor's "not
				// yet created" wording is only correct for ENOENT; conflating the
				// two is what let doctor exit 0 on a config check exits 1 on.
				if err := os.WriteFile(filepath.Join(home, "blocker"), []byte("not a dir\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return source.SecretsConfig{
					Backend: "age", Recipient: "age1qqqq", IdentityFile: idPath,
					File: "blocker/x.age",
				}
			},
			wantField:    secrets.FieldFile,
			wantSeverity: secrets.SeverityFail,
			wantContains: "not readable",
			wantFails:    1,
		},
		{
			// A directory stats fine, so the "not yet created" / "not readable"
			// arms both miss it and the vault path reported ✓ on check and
			// doctor alike. secrets.Decrypt os.Opens this path and reads an age
			// stream out of it, so a directory is not a vault — the same
			// regular-file rule probeSourceInit adopted for agentsync.toml.
			name: "age with a directory at the vault path fails",
			mutate: func(t *testing.T, home, idPath string) source.SecretsConfig {
				writeIdentity(t, idPath, 0o600)
				if err := os.MkdirAll(filepath.Join(home, "secrets", "secrets.age"), 0o700); err != nil {
					t.Fatal(err)
				}
				return source.SecretsConfig{Backend: "age", Recipient: "age1qqqq", IdentityFile: idPath}
			},
			wantField:    secrets.FieldFile,
			wantSeverity: secrets.SeverityFail,
			wantContains: "not a regular file",
			wantFails:    1,
		},
		{
			name: "age with a present vault is ok",
			mutate: func(t *testing.T, home, idPath string) source.SecretsConfig {
				writeIdentity(t, idPath, 0o600)
				vault := filepath.Join(home, "secrets", "secrets.age")
				if err := os.MkdirAll(filepath.Dir(vault), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(vault, []byte("ciphertext\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return source.SecretsConfig{Backend: "age", Recipient: "age1qqqq", IdentityFile: idPath}
			},
			wantField:    secrets.FieldFile,
			wantSeverity: secrets.SeverityOK,
			wantContains: "secrets.age",
			wantFails:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Explicitly clear the perm-check opt-out so the identity-permission
			// rows are deterministic regardless of the ambient environment: with
			// AGENTSYNC_AGE_SKIP_PERM_CHECK=1 exported in a developer's shell,
			// CheckIdentityPermissions returns nil and the "group-readable
			// identity" row would pass where CI fails it (age.go:246 tests == "1").
			t.Setenv(secrets.SkipPermCheckEnv, "")
			userHome := t.TempDir()
			home := filepath.Join(userHome, ".agentsync")
			if err := os.MkdirAll(home, 0o700); err != nil {
				t.Fatal(err)
			}
			cfg := tc.mutate(t, home, filepath.Join(userHome, "age.key"))

			got := secrets.ValidateConfig(cfg, home, userHome)

			f := findingFor(t, got, tc.wantField)
			if f.Severity != tc.wantSeverity {
				t.Errorf("field %q severity = %s, want %s (message: %s)",
					tc.wantField, f.Severity, tc.wantSeverity, f.Message)
			}
			if !strings.Contains(f.Message.String(), tc.wantContains) {
				t.Errorf("field %q message = %q, want it to contain %q",
					tc.wantField, f.Message.String(), tc.wantContains)
			}
			if tc.wantNotContains != "" && strings.Contains(f.Message.String(), tc.wantNotContains) {
				t.Errorf("field %q message = %q, want it NOT to contain %q",
					tc.wantField, f.Message.String(), tc.wantNotContains)
			}
			fails := 0
			for _, x := range got {
				if x.Severity == secrets.SeverityFail {
					fails++
				}
			}
			if fails != tc.wantFails {
				t.Errorf("got %d fail(s), want %d: %+v", fails, tc.wantFails, got)
			}
			// FIELD-RELATIVE contract (see the Field doc): a message never names
			// its own key, because Finding.Field carries it and both surfaces
			// supply the label themselves — `check` composes "<field>: <message>"
			// and `doctor` prints the message under a padded label column. A
			// message written "[secrets].recipient is required …" doubles the key
			// on both. `[secrets]` unqualified is fine ("no [secrets] block"); it
			// is the "[secrets].<key>" form that stutters.
			for _, x := range got {
				if strings.Contains(x.Message.String(), "[secrets].") {
					t.Errorf("field %q message names its own key — keep messages field-relative: %q",
						x.Field, x.Message.String())
				}
			}
		})
	}
}

// TestValidateConfigFieldOrder pins the order ValidateConfig's doc comment
// promises — backend → recipient → identity_file → file — which is what lets a
// report surface render one line per field without sorting them itself.
func TestValidateConfigFieldOrder(t *testing.T) {
	t.Setenv(secrets.SkipPermCheckEnv, "")
	userHome := t.TempDir()
	home := filepath.Join(userHome, ".agentsync")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	idPath := filepath.Join(userHome, "age.key")
	writeIdentity(t, idPath, 0o600)

	var got []secrets.Field
	for _, f := range secrets.ValidateConfig(
		source.SecretsConfig{Backend: "age", Recipient: "age1qqqq", IdentityFile: idPath},
		home, userHome,
	) {
		got = append(got, f.Field)
	}
	want := []secrets.Field{
		secrets.FieldBackend,
		secrets.FieldRecipient,
		secrets.FieldIdentityFile,
		secrets.FieldFile,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("field order = %v, want %v", got, want)
	}
}

// TestValidateConfigSanitizesHostilePaths pins that a config-derived path
// carrying a raw ESC byte cannot reach a terminal through a Finding: Message is
// untrusted.Text, whose String() sanitizes (issue #93/#171 class).
func TestValidateConfigSanitizesHostilePaths(t *testing.T) {
	// Same neutralization as TestValidateConfig: this fixture's identity file
	// does not exist, so the "not readable" arm fires first either way — pin the
	// var anyway so no row here can depend on the developer's shell.
	t.Setenv(secrets.SkipPermCheckEnv, "")
	userHome := t.TempDir()
	home := filepath.Join(userHome, ".agentsync")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := source.SecretsConfig{
		Backend:      "age",
		Recipient:    "age1qqqq",
		IdentityFile: "/nope/x\x1b[2K\x1b[31mHACKED/age.key",
	}
	for _, f := range secrets.ValidateConfig(cfg, home, userHome) {
		if strings.ContainsRune(f.Message.String(), 0x1b) {
			t.Fatalf("finding for %q carries a raw ESC byte: %q", f.Field, f.Message.String())
		}
	}
	f := findingFor(t, secrets.ValidateConfig(cfg, home, userHome), secrets.FieldIdentityFile)
	if !strings.Contains(f.Message.String(), "HACKED") {
		t.Fatalf("the sanitized path should still be named; got %q", f.Message.String())
	}
}

// writeIdentity creates a fixture age identity file with an exact mode.
// os.WriteFile applies the process umask, so chmod after the write is what
// actually pins the bits the permission check reads.
func writeIdentity(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("# fixture identity\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// TestRequireAgeVault pins the `secret` group's precondition. It is a DIFFERENT
// rule from ValidateConfig — these commands manage the age vault, so
// backend = "env" is a legitimate refusal for them even though it is a valid
// configuration — but it MUST share NormalizeBackend, or a backend spelling
// apply accepts gets refused here (issue #228).
func TestRequireAgeVault(t *testing.T) {
	const idPath = "/home/u/.config/agentsync/age.key"
	tests := []struct {
		name         string
		cfg          source.SecretsConfig
		access       secrets.VaultAccess
		wantErr      bool
		wantContains string
	}{
		{
			name:    "complete age config, read",
			cfg:     source.SecretsConfig{Backend: "age", Recipient: "age1qqqq", IdentityFile: idPath},
			access:  secrets.VaultRead,
			wantErr: false,
		},
		{
			name:    "uppercase age is accepted",
			cfg:     source.SecretsConfig{Backend: "AGE", Recipient: "age1qqqq", IdentityFile: idPath},
			access:  secrets.VaultWrite,
			wantErr: false,
		},
		{
			name:         "env backend is refused",
			cfg:          source.SecretsConfig{Backend: "env"},
			access:       secrets.VaultRead,
			wantErr:      true,
			wantContains: `backend = "age"`,
		},
		{
			name:         "no backend is refused",
			cfg:          source.SecretsConfig{},
			access:       secrets.VaultRead,
			wantErr:      true,
			wantContains: `backend = "age"`,
		},
		{
			name:         "read without identity_file is refused",
			cfg:          source.SecretsConfig{Backend: "age", Recipient: "age1qqqq"},
			access:       secrets.VaultRead,
			wantErr:      true,
			wantContains: "identity_file",
		},
		{
			name:    "read without recipient is allowed",
			cfg:     source.SecretsConfig{Backend: "age", IdentityFile: idPath},
			access:  secrets.VaultRead,
			wantErr: false,
		},
		{
			name:         "write without recipient is refused",
			cfg:          source.SecretsConfig{Backend: "age", IdentityFile: idPath},
			access:       secrets.VaultWrite,
			wantErr:      true,
			wantContains: "recipient",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := secrets.RequireAgeVault(tc.cfg, "secret list", tc.access)
			if (err != nil) != tc.wantErr {
				t.Fatalf("RequireAgeVault = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil {
				return
			}
			if !strings.Contains(err.Error(), tc.wantContains) {
				t.Errorf("error %q should contain %q", err, tc.wantContains)
			}
			if !strings.Contains(err.Error(), "secret list") {
				t.Errorf("error %q should name the operation it refused", err)
			}
		})
	}
}
