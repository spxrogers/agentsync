package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	secrets_pkg "github.com/spxrogers/agentsync/internal/secrets"
)

// setupSecretsEnv initialises a fresh agentsync home with age [secrets] config,
// returning the env map and paths to the age file + identity file.
func setupSecretsEnv(t *testing.T) (env map[string]string, agePath, idPath string, id *age.X25519Identity) {
	t.Helper()
	tmp := t.TempDir()
	env = map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	// Generate age identity.
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	idPath = filepath.Join(tmp, ".config", "agentsync", "age.key")
	if err := os.MkdirAll(filepath.Dir(idPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idPath, []byte(id.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	// Append [secrets] block to agentsync.toml.
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	existing, _ := os.ReadFile(cfgPath)
	block := fmt.Sprintf("\n[secrets]\nbackend = \"age\"\nfile = \"secrets/secrets.age\"\nrecipient = %q\nidentity_file = %q\n",
		id.Recipient().String(), idPath)
	if err := os.WriteFile(cfgPath, append(existing, []byte(block)...), 0o644); err != nil {
		t.Fatal(err)
	}

	agePath = filepath.Join(tmp, ".agentsync", "secrets", "secrets.age")
	return env, agePath, idPath, id
}

func TestSecretsGetSet(t *testing.T) {
	env, agePath, idPath, id := setupSecretsEnv(t)
	_ = idPath

	// Pre-create the secrets.age file with an initial value.
	plain := []byte("[github]\ntoken = \"initial_token\"\n")
	if err := secrets_pkg.Encrypt(plain, id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}

	// get existing key
	out, err := runCLI(t, env, "secrets", "get", "github.token")
	if err != nil {
		t.Fatalf("secrets get: %v\n%s", err, out)
	}
	if !strings.Contains(out, "initial_token") {
		t.Fatalf("expected initial_token, got %q", out)
	}

	// set new key
	out, err = runCLI(t, env, "secrets", "set", "linear.api_key=lin_xyz")
	if err != nil {
		t.Fatalf("secrets set: %v\n%s", err, out)
	}

	// get newly set key
	out, err = runCLI(t, env, "secrets", "get", "linear.api_key")
	if err != nil {
		t.Fatalf("secrets get after set: %v\n%s", err, out)
	}
	if !strings.Contains(out, "lin_xyz") {
		t.Fatalf("expected lin_xyz, got %q", out)
	}

	// original key still present
	out, err = runCLI(t, env, "secrets", "get", "github.token")
	if err != nil {
		t.Fatalf("get original after set: %v\n%s", err, out)
	}
	if !strings.Contains(out, "initial_token") {
		t.Fatalf("expected initial_token still present, got %q", out)
	}
}

// TestSecretsGet_RejectsNonStringLeaf proves `get` enforces apply's string-only
// contract: a non-string leaf (number/bool/table) errors rather than printing a
// Go-formatted value apply would later refuse.
func TestSecretsGet_RejectsNonStringLeaf(t *testing.T) {
	env, agePath, _, id := setupSecretsEnv(t)
	plain := []byte("[svc]\nport = 8080\nname = \"ok\"\n")
	if err := secrets_pkg.Encrypt(plain, id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "secrets", "get", "svc.port"); err == nil {
		t.Fatal("get of a non-string leaf must error (matches apply's flatten contract)")
	}
	// A string leaf in the same vault still works.
	if out, err := runCLI(t, env, "secrets", "get", "svc.name"); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("get of a string leaf must succeed: err=%v out=%q", err, out)
	}
}

// TestSecretsEdit_EditorWithArgs proves $EDITOR carrying flags ("code --wait")
// is word-split, not treated as a single executable path.
func TestSecretsEdit_EditorWithArgs(t *testing.T) {
	env, agePath, _, id := setupSecretsEnv(t)
	if err := secrets_pkg.Encrypt([]byte("[a]\nb = \"orig\"\n"), id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}
	// An "editor" script that writes fixed content to its LAST argument.
	dir := t.TempDir()
	ed := filepath.Join(dir, "ed.sh")
	if err := os.WriteFile(ed, []byte("#!/bin/sh\nfor a in \"$@\"; do f=\"$a\"; done\nprintf '[edited]\\nval = \"yes\"\\n' > \"$f\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", ed+" --wait")
	if out, err := runCLI(t, env, "secrets", "edit"); err != nil {
		t.Fatalf("secrets edit with $EDITOR carrying a flag must work: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "secrets", "get", "edited.val"); err != nil || !strings.Contains(out, "yes") {
		t.Fatalf("edit did not save via the flagged editor: err=%v out=%q", err, out)
	}
}

// TestSecretsEdit_RejectsCollidingKeys proves `edit` validates against apply's
// flatten contract: a quoted "a.b" key colliding with a [a] b table is refused
// (not silently encrypted into a vault apply would reject).
func TestSecretsEdit_RejectsCollidingKeys(t *testing.T) {
	env, agePath, _, id := setupSecretsEnv(t)
	if err := secrets_pkg.Encrypt([]byte("[a]\nb = \"orig\"\n"), id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ed := filepath.Join(dir, "ed.sh")
	if err := os.WriteFile(ed, []byte("#!/bin/sh\nfor a in \"$@\"; do f=\"$a\"; done\nprintf '\"x.y\" = \"one\"\\n[x]\\ny = \"two\"\\n' > \"$f\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", ed)
	if _, err := runCLI(t, env, "secrets", "edit"); err == nil {
		t.Fatal("edit must reject a flatten-colliding vault rather than save it")
	}
}

// TestSecretsSet_RejectsRecipientIdentityMismatch is the regression for
// `secrets set` re-encrypting the whole store to a recipient the configured
// identity_file cannot decrypt — silently locking the user out of their own
// secrets. It must refuse and leave the existing store intact.
func TestSecretsSet_RejectsRecipientIdentityMismatch(t *testing.T) {
	env, agePath, _, id := setupSecretsEnv(t)
	// Seed a store readable by the configured identity.
	if err := secrets_pkg.Encrypt([]byte("[a]\nb = \"c\"\n"), id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}
	// Point recipient at a DIFFERENT key while leaving identity_file unchanged.
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(env["AGENTSYNC_TARGET_ROOT"], ".agentsync", "agentsync.toml")
	data, _ := os.ReadFile(cfgPath)
	mismatched := strings.Replace(string(data), id.Recipient().String(), other.Recipient().String(), 1)
	if err := os.WriteFile(cfgPath, []byte(mismatched), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "secrets", "set", "x.y=z"); err == nil {
		t.Fatal("secrets set with a recipient the identity can't decrypt should be rejected")
	}
	// The original store must still be readable (rolled back, not clobbered).
	out, gerr := runCLI(t, env, "secrets", "get", "a.b")
	if gerr != nil || !strings.Contains(out, "c") {
		t.Fatalf("original store not preserved after rejected set: err=%v out=%q", gerr, out)
	}
}

func TestSecretsGet_MissingKey(t *testing.T) {
	env, agePath, _, id := setupSecretsEnv(t)
	plain := []byte("[foo]\nbar = \"baz\"\n")
	if err := secrets_pkg.Encrypt(plain, id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "secrets", "get", "nonexistent.key")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

// TestSecretsSet_NoEqualsNonTTYErrors is the regression for the bug where
// `agentsync secrets set <something>` (no `=`) printed the user's argument
// back via `got %q` — leaking it if the argument was a real token. The
// new behaviour refuses without echoing the argument when stdin is not a
// TTY (the test environment).
func TestSecretsSet_NoEqualsNonTTYErrors(t *testing.T) {
	const sentinel = "ghp_SENTINEL_NEVER_ECHO_THIS_TOKEN"
	env, _, _, _ := setupSecretsEnv(t)
	out, err := runCLI(t, env, "secrets", "set", sentinel)
	if err == nil {
		t.Fatal("expected error when no '=' and stdin is not a terminal")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("SECURITY: error message echoed sentinel %q: %v", sentinel, err)
	}
	if strings.Contains(out, sentinel) {
		t.Fatalf("SECURITY: stdout/stderr echoed sentinel %q:\n%s", sentinel, out)
	}
}

// TestSecretsSet_StdinPath proves the safe input mode works end-to-end:
// the secret never appears on argv (so ps(1) / history don't see it) and
// the stored value matches the stdin payload.
func TestSecretsSet_StdinPath(t *testing.T) {
	env, _, _, _ := setupSecretsEnv(t)
	const value = "ghp_VIA_STDIN_FLOW_123"
	out, err := runCLIWithStdin(t, env, value+"\n", "secrets", "set", "github.token", "--stdin")
	if err != nil {
		t.Fatalf("secrets set --stdin: %v\n%s", err, out)
	}
	got, err := runCLI(t, env, "secrets", "get", "github.token")
	if err != nil {
		t.Fatalf("get after stdin set: %v\n%s", err, got)
	}
	if !strings.Contains(got, value) {
		t.Fatalf("stored value mismatch: want %q in %q", value, got)
	}
}

// TestSecretsSet_LegacyArgWarns proves the back-compat path still works
// but warns the user that the value just hit argv.
func TestSecretsSet_LegacyArgWarns(t *testing.T) {
	env, _, _, _ := setupSecretsEnv(t)
	out, err := runCLI(t, env, "secrets", "set", "legacy.key=legacy_value")
	if err != nil {
		t.Fatalf("legacy set: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warning") || !strings.Contains(out, "--stdin") {
		t.Fatalf("legacy form did not warn about argv exposure; got:\n%s", out)
	}
}

// TestSecretsSet_RefusesClobberOfScalarParent is the regression for the
// vault-data-loss bug where `secrets set a.b=v` (with `a` an existing scalar
// secret) silently replaced the scalar with a fresh nested map — irreversibly
// destroying the cleartext value in an often-committed age vault — while still
// reporting success. The fix refuses *destructive type changes* (nesting under a
// scalar parent; a leaf assignment overwriting an existing table) and leaves
// secrets.age byte-for-byte unchanged, while the legitimate cases (new nested
// key under an absent or table parent; in-place scalar update) still succeed.
//
// Each sub-case drives the REAL command end-to-end against an on-disk encrypted
// vault and asserts the observable outcome (raw ciphertext bytes and `secrets
// get`), never a parsed-map round-trip.
func TestSecretsSet_RefusesClobberOfScalarParent(t *testing.T) {
	const liveSecret = "ghp_live_DO_NOT_DESTROY"
	cases := []struct {
		name           string
		seed           string            // decrypted TOML pre-encrypted into the vault
		setArg         string            // argument to `secrets set`
		wantRefused    bool              // must the set be refused?
		wantErrSubstrs []string          // full dotted paths the refusal must name
		mustNotLeak    []string          // secret values that must never appear on a refusal
		survivors      map[string]string // `secrets get` key -> expected substring afterwards
	}{
		{
			name:           "scalar parent refused",
			seed:           fmt.Sprintf("token = %q\n", liveSecret),
			setArg:         "token.scope=repo",
			wantRefused:    true,
			wantErrSubstrs: []string{`"token.scope"`, `"token"`},
			mustNotLeak:    []string{liveSecret},
			survivors:      map[string]string{"token": liveSecret},
		},
		{
			// Regression: with scalar `a.b`, `set a.b.c=v` used to report the
			// recursion's LOCAL names — `"b.c"` clashing with `"b"` — not the
			// full dotted paths the user typed.
			name:           "deep scalar parent refusal names the full dotted paths",
			seed:           fmt.Sprintf("[a]\nb = %q\n", liveSecret),
			setArg:         "a.b.c=repo",
			wantRefused:    true,
			wantErrSubstrs: []string{`"a.b.c"`, `"a.b"`},
			mustNotLeak:    []string{liveSecret},
			survivors:      map[string]string{"a.b": liveSecret},
		},
		{
			name:        "table parent allowed",
			seed:        "[github]\ntoken = \"gh_tok\"\n",
			setArg:      "github.scope=repo",
			wantRefused: false,
			survivors:   map[string]string{"github.scope": "repo", "github.token": "gh_tok"},
		},
		{
			name:        "absent parent allowed",
			seed:        "[github]\ntoken = \"gh_tok\"\n",
			setArg:      "linear.api_key=lin_new",
			wantRefused: false,
			survivors:   map[string]string{"linear.api_key": "lin_new", "github.token": "gh_tok"},
		},
		{
			name:        "scalar leaf in-place update allowed",
			seed:        "[github]\ntoken = \"old_tok\"\n",
			setArg:      "github.token=new_tok",
			wantRefused: false,
			survivors:   map[string]string{"github.token": "new_tok"},
		},
		{
			name:           "table leaf overwrite refused",
			seed:           "[github]\ntoken = \"gh_tok\"\n",
			setArg:         "github=whole_value",
			wantRefused:    true,
			wantErrSubstrs: []string{`"github"`},
			mustNotLeak:    []string{"gh_tok"},
			survivors:      map[string]string{"github.token": "gh_tok"},
		},
		{
			// The nested-table variant of the same refusal must also name the
			// full path (`a.b`, not the recursion-local `b`).
			name:           "nested table leaf overwrite refusal names the full dotted path",
			seed:           "[a.b]\nc = \"nested_tok\"\n",
			setArg:         "a.b=whole_value",
			wantRefused:    true,
			wantErrSubstrs: []string{`"a.b"`},
			mustNotLeak:    []string{"nested_tok"},
			survivors:      map[string]string{"a.b.c": "nested_tok"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, agePath, _, id := setupSecretsEnv(t)
			if err := secrets_pkg.Encrypt([]byte(tc.seed), id.Recipient().String(), agePath); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(agePath)
			if err != nil {
				t.Fatalf("read vault before set: %v", err)
			}

			out, setErr := runCLI(t, env, "secrets", "set", tc.setArg)

			if tc.wantRefused {
				if setErr == nil {
					t.Fatalf("secrets set %q must be refused, got success\n%s", tc.setArg, out)
				}
				// The vault must be byte-for-byte unchanged on disk: no
				// re-encryption, no rollback artifact.
				after, rerr := os.ReadFile(agePath)
				if rerr != nil {
					t.Fatalf("read vault after refused set: %v", rerr)
				}
				if !bytes.Equal(before, after) {
					t.Fatalf("a refused set must leave secrets.age byte-for-byte unchanged (len before=%d after=%d)",
						len(before), len(after))
				}
				// The refusal must not echo any secret value byte.
				for _, s := range tc.mustNotLeak {
					if strings.Contains(setErr.Error(), s) || strings.Contains(out, s) {
						t.Fatalf("SECURITY: refusal leaked secret value %q:\nerr=%v\nout=%s", s, setErr, out)
					}
				}
				// The refusal must name the FULL dotted paths the user typed,
				// not the recursion's local remainder.
				for _, want := range tc.wantErrSubstrs {
					if !strings.Contains(setErr.Error(), want) {
						t.Fatalf("refusal should name the full dotted path %s; got: %v", want, setErr)
					}
				}
			} else if setErr != nil {
				t.Fatalf("secrets set %q must succeed, got error: %v\n%s", tc.setArg, setErr, out)
			}

			// The survivors / results are observable via `secrets get` against
			// the real encrypted vault.
			for key, want := range tc.survivors {
				got, gErr := runCLI(t, env, "secrets", "get", key)
				if gErr != nil {
					t.Fatalf("secrets get %q: %v\n%s", key, gErr, got)
				}
				if !strings.Contains(got, want) {
					t.Fatalf("secrets get %q = %q, want it to contain %q", key, got, want)
				}
			}
		})
	}
}

// TestSecretsSet_ValidatesVaultBeforePersist proves the `set` path now runs the
// same flatten-contract validation (secrets.ValidateVaultTOML) the `edit` path
// already applies, BEFORE encrypting — so a `set` that would yield a vault
// `apply` later refuses is rejected at save time with a "not saved" message and
// the prior vault is left untouched.
//
// Fixture: the vault holds a literal quoted top-level key "x.y" (a single key
// whose NAME contains a dot — NOT a nested table), which is a valid vault on its
// own. `secrets set x.y=two` creates a nested `[x] y="two"` (parent "x" is
// absent, so the clobber guard does NOT fire) which now COLLIDES with the quoted
// "x.y" under apply's flatten contract (mirrors internal/secrets/age.go:120-128).
// The validation guard — not the clobber guard — is what must catch it.
func TestSecretsSet_ValidatesVaultBeforePersist(t *testing.T) {
	env, agePath, idPath, id := setupSecretsEnv(t)
	const seed = "\"x.y\" = \"one\"\n"
	if err := secrets_pkg.Encrypt([]byte(seed), id.Recipient().String(), agePath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(agePath)
	if err != nil {
		t.Fatal(err)
	}

	out, setErr := runCLI(t, env, "secrets", "set", "x.y=two")
	if setErr == nil {
		t.Fatalf("a set that yields a flatten collision must be refused, got success\n%s", out)
	}
	if !strings.Contains(setErr.Error(), "not saved") {
		t.Fatalf("validation refusal should carry a '(not saved)' message, got: %v", setErr)
	}

	// The vault must be byte-for-byte unchanged on disk.
	after, rerr := os.ReadFile(agePath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a validation-refused set must leave secrets.age byte-for-byte unchanged")
	}
	// And the original cleartext survives, decrypted straight from the artifact.
	plain, derr := secrets_pkg.Decrypt(agePath, idPath)
	if derr != nil {
		t.Fatalf("decrypt vault after refused set: %v", derr)
	}
	if string(plain) != seed {
		t.Fatalf("vault cleartext changed after a refused set: got %q, want %q", string(plain), seed)
	}
}

// TestSecretsSet_RefusesEmptyValue pins that an empty (or whitespace-only) value
// is refused by default across every input mode, stores nothing, and never echoes
// the attempted value (issue #165).
func TestSecretsSet_RefusesEmptyValue(t *testing.T) {
	cases := []struct {
		name     string
		useStdin bool
		stdin    string
		args     []string
	}{
		{"stdin-empty", true, "", []string{"secrets", "set", "github.token", "--stdin"}},
		{"stdin-newline-only", true, "\n", []string{"secrets", "set", "github.token", "--stdin"}},
		{"stdin-whitespace-only", true, "   ", []string{"secrets", "set", "github.token", "--stdin"}},
		{"legacy-empty", false, "", []string{"secrets", "set", "github.token="}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, _, _, _ := setupSecretsEnv(t)
			var out string
			var err error
			if tc.useStdin {
				out, err = runCLIWithStdin(t, env, tc.stdin, tc.args...)
			} else {
				out, err = runCLI(t, env, tc.args...)
			}
			if err == nil {
				t.Fatalf("empty value must be refused; got success:\n%s", out)
			}
			if !strings.Contains(err.Error(), "empty") {
				t.Fatalf("refusal should explain the empty-value; got: %v", err)
			}
			// Nothing was stored: get on the key fails (no vault / not found).
			if got, gerr := runCLI(t, env, "secrets", "get", "github.token"); gerr == nil {
				t.Fatalf("no secret should have been stored; get returned: %s", got)
			}
		})
	}
}

// TestSecretsSet_AllowEmptyStoresEmpty proves the escape hatch stores an empty
// value end-to-end through encrypt->decrypt (issue #165).
func TestSecretsSet_AllowEmptyStoresEmpty(t *testing.T) {
	env, _, _, _ := setupSecretsEnv(t)
	if out, err := runCLIWithStdin(t, env, "", "secrets", "set", "github.token", "--stdin", "--allow-empty"); err != nil {
		t.Fatalf("--allow-empty should store an empty value; got err=%v\n%s", err, out)
	}
	got, err := runCLI(t, env, "secrets", "get", "github.token")
	if err != nil {
		t.Fatalf("get after allow-empty set: %v\n%s", err, got)
	}
	if strings.TrimSpace(got) != "" {
		t.Fatalf("stored value should be empty; got %q", got)
	}
}
