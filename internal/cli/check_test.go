package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/ui"
)

// TestCheck_UninitializedHomeErrors is the regression for check printing a
// false "ok" (exit 0) when run before `init` — source.Load tolerates a
// missing home, so the user got a green light on a non-existent config.
func TestCheck_HalfInitMissingToml(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	// Scaffold component dirs/files but NO agentsync.toml (the half-init an
	// authoring command leaves when run before `init`). check must flag it
	// rather than report a false "ok: schema valid".
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "x.toml")
	if err := os.MkdirAll(filepath.Dir(mcp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"y\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "check")
	if err == nil {
		t.Fatal("check on a home missing agentsync.toml must error")
	}
	if !strings.Contains(err.Error(), "agentsync.toml") || !strings.Contains(err.Error(), "init") {
		t.Fatalf("error should name agentsync.toml + point at init; got: %v", err)
	}
}

func TestCheck_UninitializedHomeErrors(t *testing.T) {
	tmp := t.TempDir()
	// Note: no `init` — the agentsync home does not exist.
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	out, err := runCLI(t, env, "check")
	if err == nil {
		t.Fatalf("check on uninitialized home should error; got ok:\n%s", out)
	}
	if !strings.Contains(err.Error(), "init") {
		t.Fatalf("error should point at `init`; got: %v", err)
	}
}

func TestCheck_Empty(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")

	out, err := runCLI(t, env, "check")
	if err != nil {
		t.Fatalf("check on empty home: %v", err)
	}
	if !strings.Contains(out, ui.EmojiSuccess) || !strings.Contains(out, "schema valid") {
		t.Fatalf("check output missing the success line: %s", out)
	}
}

func TestCheck_BadTOML(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")

	badPath := filepath.Join(tmp, ".agentsync", "mcp", "broken.toml")
	_ = os.MkdirAll(filepath.Dir(badPath), 0o755)
	_ = os.WriteFile(badPath, []byte("[server\nmissing-bracket"), 0o644)

	_, err := runCLI(t, env, "check")
	if err == nil {
		t.Fatal("check should fail on malformed TOML")
	}
}

// TestCheck_AgeMissingRecipient asserts check catches a half-configured
// [secrets] block (backend = age but no recipient).
func TestCheck_AgeMissingRecipient(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp}
	_, _ = runCLI(t, env, "init")

	identity := filepath.Join(tmp, "age.key")
	_ = os.WriteFile(identity, []byte("AGE-SECRET-KEY-...\n"), 0o600)
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body := `[agents]
[secrets]
backend       = "age"
identity_file = "` + identity + `"
`
	_ = os.WriteFile(cfgPath, []byte(body), 0o644)

	out, err := runCLI(t, env, "check")
	if err == nil {
		t.Fatalf("check should fail when recipient is missing; got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Fatalf("check should name the missing field; got err=%q out=%s", err, out)
	}
}

// TestCheck_AgeIdentityPerms asserts check rejects a world-readable
// identity file.
func TestCheck_AgeIdentityPerms(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp}
	_, _ = runCLI(t, env, "init")

	identity := filepath.Join(tmp, "age.key")
	_ = os.WriteFile(identity, []byte("AGE-SECRET-KEY-...\n"), 0o644)
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body := `[agents]
[secrets]
backend       = "age"
recipient     = "age1qqqq"
identity_file = "` + identity + `"
`
	_ = os.WriteFile(cfgPath, []byte(body), 0o644)

	out, err := runCLI(t, env, "check")
	if err == nil {
		t.Fatalf("check should fail on 0644 identity; got:\n%s", out)
	}
}

func TestCheck_UnknownAgent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "agent", "add", "claude")

	cfg := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body, _ := os.ReadFile(cfg)
	body = append(body, []byte("\n[agents]\nbogus = { enabled = true }\n")...)
	_ = os.WriteFile(cfg, body, 0o644)

	_, err := runCLI(t, env, "check")
	if err == nil {
		t.Fatal("check should reject unknown agent name")
	}
}

// TestCheck_HelpListsScopeFlags is the acceptance check that `check --help`
// now documents --scope and --project, matching status/diff.
func TestCheck_HelpListsScopeFlags(t *testing.T) {
	out, _ := runCLI(t, nil, "check", "--help")
	for _, flag := range []string{"--scope", "--project"} {
		if !strings.Contains(out, flag) {
			t.Fatalf("check --help missing %q. Got:\n%s", flag, out)
		}
	}
}

// TestCheck_ProjectScope_OK schema-lints a project .agentsync/ tree and
// resolves its references via --project (which implies project scope). The
// project MCP server carries an ${env:} reference so the secret-resolution pass
// is exercised, not just the schema decode.
func TestCheck_ProjectScope_OK(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome, "PROJ_TOKEN": "s3cr3t"}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", proj); err != nil {
		t.Fatal(err)
	}
	declareProjectAgent(t, env, proj, "claude")
	// A valid project-scope MCP server referencing an env var that is set, so
	// the ${env:} resolution pass succeeds.
	mcp := filepath.Join(proj, ".agentsync", "mcp", "projapi.toml")
	body := "[server]\ntype = \"stdio\"\ncommand = \"node\"\nargs = [\"s.js\"]\nenv = { TOKEN = \"${env:PROJ_TOKEN}\" }\n"
	if err := os.WriteFile(mcp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "check", "--project", proj)
	if err != nil {
		t.Fatalf("check --project: %v\n%s", err, out)
	}
	if !strings.Contains(out, ui.EmojiSuccess) || !strings.Contains(out, "schema valid") {
		t.Fatalf("check --project output missing the success line: %s", out)
	}
}

// TestCheck_ProjectScope_BadTOML asserts schema-linting reaches the project
// tree: a malformed file under <proj>/.agentsync/ fails check at project scope.
func TestCheck_ProjectScope_BadTOML(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", proj); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(proj, ".agentsync", "mcp", "broken.toml")
	if err := os.WriteFile(bad, []byte("[server\nmissing-bracket"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "check", "--scope", "project", "--project", proj); err == nil {
		t.Fatal("check --scope project should fail on malformed project TOML")
	}
}

// TestCheck_ProjectScope_HalfInit asserts the half-init guard adapts to the
// project case: a <proj>/.agentsync/ directory with no agentsync.toml (so
// source.Load tolerates it and would otherwise report a false "ok") is rejected
// with a message naming agentsync.toml and the project init command.
func TestCheck_ProjectScope_HalfInit(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	// A .agentsync/ tree exists (so scope resolution accepts the project root)
	// but it has no agentsync.toml.
	if err := os.MkdirAll(filepath.Join(proj, ".agentsync", "mcp"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "check", "--project", proj)
	if err == nil {
		t.Fatal("check on a half-initialized project tree must error")
	}
	if !strings.Contains(err.Error(), "agentsync.toml") || !strings.Contains(err.Error(), "init") {
		t.Fatalf("error should name agentsync.toml + point at init; got: %v", err)
	}
}

// TestCheck_ProjectScope_NoTree asserts check surfaces the shared
// scope-resolution error when --project points at a path with no .agentsync/
// tree, rather than silently degrading to user scope.
func TestCheck_ProjectScope_NoTree(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "check", "--project", proj)
	if err == nil {
		t.Fatal("check --project with no .agentsync/ tree should error")
	}
	if !strings.Contains(err.Error(), ".agentsync") {
		t.Fatalf("error should mention the missing .agentsync/ tree; got: %v", err)
	}
}

// TestCheck_OfflineMode pins that AGENTSYNC_ALLOW_OFFLINE_VERIFY=1 (the CI path)
// validates reference SHAPE but not resolvability (issue #171): a malformed
// ${secret:} is rejected offline, a well-formed-but-unresolvable ref passes offline,
// and the online path still fails to resolve it.
func TestCheck_OfflineMode(t *testing.T) {
	writeMCP := func(t *testing.T, tmp, body string) {
		t.Helper()
		p := filepath.Join(tmp, ".agentsync", "mcp", "x.toml")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("offline-rejects-malformed-ref", func(t *testing.T) {
		tmp := t.TempDir()
		env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "AGENTSYNC_ALLOW_OFFLINE_VERIFY": "1"}
		_, _ = runCLI(t, env, "init")
		writeMCP(t, tmp, "[server]\ntype=\"stdio\"\ncommand=\"${secret:}\"\n")
		_, err := runCLI(t, env, "check")
		if err == nil {
			t.Fatal("offline check must reject a malformed ${secret:} reference")
		}
		if !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("error should name the malformed ref; got: %v", err)
		}
	})

	// The shape check must run ONLINE too: the strict resolver passes a
	// malformed token through as literal text, so before the check ran in both
	// modes a green local `check` ("all references resolve") contradicted a
	// red offline CI check on the same config.
	t.Run("online-rejects-malformed-ref", func(t *testing.T) {
		tmp := t.TempDir()
		env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
		_, _ = runCLI(t, env, "init")
		writeMCP(t, tmp, "[server]\ntype=\"stdio\"\ncommand=\"${secret:}\"\n")
		_, err := runCLI(t, env, "check")
		if err == nil {
			t.Fatal("online check must reject a malformed ${secret:} reference (offline CI does)")
		}
		if !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("error should name the malformed ref; got: %v", err)
		}
		if strings.Contains(err.Error(), "offline mode checks reference shape only") {
			t.Fatalf("online error should not carry the offline-mode hint; got: %v", err)
		}
	})

	// A malformed ref is config-derived and can carry hostile bytes (agentsync
	// configs are shareable dotfiles). looseRefRe's `[^}]*` tail captures ESC/C1
	// bytes, so the offline error must sanitize the candidate on display —
	// otherwise a crafted config injects terminal escapes into the CI log
	// (issue #171 / the #93 escape-injection class). MalformedSecretRefs returns
	// []untrusted.Text and check joins via untrusted.Join to close this.
	t.Run("offline-malformed-ref-is-sanitized", func(t *testing.T) {
		tmp := t.TempDir()
		env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "AGENTSYNC_ALLOW_OFFLINE_VERIFY": "1"}
		_, _ = runCLI(t, env, "init")
		// TOML  decodes to a raw ESC byte inside the Command field; the key
		// after the colon is illegal, so it is flagged malformed and displayed.
		writeMCP(t, tmp, "[server]\ntype=\"stdio\"\ncommand=\"${secret:\\u001b[2K\\u001b[31mHACKED}\"\n")
		_, err := runCLI(t, env, "check")
		if err == nil {
			t.Fatal("offline check must reject the malformed ref")
		}
		if strings.ContainsRune(err.Error(), 0x1b) {
			t.Fatalf("offline check error must not carry a raw ESC byte (escape injection); got: %q", err.Error())
		}
		if !strings.Contains(err.Error(), "malformed") || !strings.Contains(err.Error(), "HACKED") {
			t.Fatalf("error should still name the (sanitized) malformed ref; got: %q", err.Error())
		}
	})

	// A malformed OUTER ref that embeds a well-formed nested ref: the loose
	// candidate "${secret:${env:FOO}" contains a valid "${env:FOO}", so an
	// unanchored substring shape-check would wrongly accept it. The offline check
	// must flag it (apply resolves only the inner ref, leaving a partial literal).
	t.Run("offline-rejects-nested-ref", func(t *testing.T) {
		tmp := t.TempDir()
		env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "AGENTSYNC_ALLOW_OFFLINE_VERIFY": "1"}
		_, _ = runCLI(t, env, "init")
		writeMCP(t, tmp, "[server]\ntype=\"stdio\"\ncommand=\"${secret:${env:FOO}}\"\n")
		_, err := runCLI(t, env, "check")
		if err == nil {
			t.Fatal("offline check must reject a nested/partial ${secret:${env:…}} reference")
		}
		if !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("error should name the malformed ref; got: %v", err)
		}
	})

	t.Run("offline-accepts-wellformed-unresolvable", func(t *testing.T) {
		tmp := t.TempDir()
		env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "AGENTSYNC_ALLOW_OFFLINE_VERIFY": "1"}
		_, _ = runCLI(t, env, "init")
		writeMCP(t, tmp, "[server]\ntype=\"stdio\"\ncommand=\"${secret:some.key}\"\n")
		out, err := runCLI(t, env, "check")
		if err != nil {
			t.Fatalf("offline check must NOT resolve a well-formed ref: %v\n%s", err, out)
		}
		if !strings.Contains(out, "resolvability not checked") {
			t.Fatalf("offline ok line should note resolvability is not checked; got: %s", out)
		}
	})

	t.Run("online-rejects-unresolvable", func(t *testing.T) {
		tmp := t.TempDir()
		env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp} // online: no offline flag
		_, _ = runCLI(t, env, "init")
		writeMCP(t, tmp, "[server]\ntype=\"stdio\"\ncommand=\"${secret:some.key}\"\n")
		_, err := runCLI(t, env, "check")
		if err == nil {
			t.Fatal("online check must fail to resolve ${secret:some.key}")
		}
	})
}

// TestCheck_SanitizesHostileAgentKey pins that check's agent-validation error
// escapes a config-derived [agents.<name>] key: a TOML quoted key can carry raw
// ESC bytes, and the wrap must use %q (not %s) so a shared config cannot inject
// terminal escapes on plain `agentsync check` (issue #93/#171 class).
func TestCheck_SanitizesHostileAgentKey(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp}
	_, _ = runCLI(t, env, "init")
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	// The quoted key decodes to a map key holding a raw ESC; the agent is
	// unknown, so validateAgent fails and check wraps the key in its error.
	body := "[agents.\"cla\\u001b[2K\\u001b[31mHACKED\"]\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "check")
	if err == nil {
		t.Fatal("check must reject an unknown agent")
	}
	if strings.ContainsRune(err.Error(), 0x1b) {
		t.Fatalf("check error must not carry a raw ESC byte from the agent key; got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "HACKED") {
		t.Fatalf("error should still name the (escaped) agent key; got: %q", err.Error())
	}
}

// TestCheck_SanitizesHostileSecretPath pins that check's [secrets] validation
// escapes a config-derived [secrets].identity_file path: a shareable dotfiles
// config with an ESC-laden, non-existent path must not inject terminal escapes
// into `check`'s error on the default (non-offline) path (issue #93/#171 class).
// The escaping now lives one layer down — secrets.ValidateConfig reports the
// path in a Finding whose Message is an untrusted.Text — so this also covers
// secretsProblems rendering that Message through String().
func TestCheck_SanitizesHostileSecretPath(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp}
	_, _ = runCLI(t, env, "init")
	cfgPath := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	// identity_file holds a raw ESC (TOML \u escape) and points nowhere, so
	// the validator's os.Stat fails and it reports the path (and the
	// *PathError) in a SeverityFail finding — which must be sanitized.
	body := "[agents]\n[secrets]\nbackend       = \"age\"\nrecipient     = \"age1qqqq\"\n" +
		"identity_file = \"/nope/x\\u001b[2K\\u001b[31mHACKED/age.key\"\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "check")
	if err == nil {
		t.Fatal("check must fail on a non-existent identity_file")
	}
	if strings.ContainsRune(err.Error(), 0x1b) {
		t.Fatalf("check error must not carry a raw ESC byte from a config-derived path; got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "HACKED") {
		t.Fatalf("error should still name the (sanitized) path; got: %q", err.Error())
	}
}

// TestCheck_AcceptsBackendSpellingsApplyAccepts is the regression for #228:
// secrets.SelectBackend — the resolver `apply` renders through — lower-cases
// the backend name, but `check` compared it against the literal "age"/"env".
// A config written `backend = "AGE"` therefore applied cleanly and failed
// `check` with `backend "AGE" not supported`.
func TestCheck_AcceptsBackendSpellingsApplyAccepts(t *testing.T) {
	tests := []struct {
		name    string
		backend string
	}{
		{name: "uppercase age", backend: "AGE"},
		{name: "mixed case age", backend: "Age"},
		{name: "lowercase env", backend: "env"},
		{name: "uppercase env", backend: "ENV"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			// The perm-check opt-out and AGENTSYNC_HOME are pinned so an
			// exported value in a developer's shell cannot change what this
			// test exercises: AGENTSYNC_AGE_SKIP_PERM_CHECK=1 short-circuits
			// CheckIdentityPermissions, so the age rows would pass without the
			// 0600 identity below ever being inspected, and AGENTSYNC_HOME
			// outranks AGENTSYNC_TARGET_ROOT in paths.AgentsyncHome, so an
			// exported one would aim check at the developer's real home.
			env := map[string]string{
				"AGENTSYNC_TARGET_ROOT":         tmp,
				"HOME":                          tmp,
				"AGENTSYNC_AGE_SKIP_PERM_CHECK": "",
				"AGENTSYNC_HOME":                "",
			}
			if _, err := runCLI(t, env, "init"); err != nil {
				t.Fatalf("init: %v", err)
			}
			identity := filepath.Join(tmp, "age.key")
			if err := os.WriteFile(identity, []byte("# fixture identity\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(identity, 0o600); err != nil {
				t.Fatal(err)
			}
			body := "[agents]\n[secrets]\nbackend       = \"" + tc.backend + "\"\n" +
				"recipient     = \"age1qqqq\"\nidentity_file = \"" + identity + "\"\n"
			if err := os.WriteFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := runCLI(t, env, "check")
			if err != nil {
				t.Fatalf("check must accept backend = %q (apply resolves it); got err=%v\n%s", tc.backend, err, out)
			}
		})
	}
}

// TestCheck_ReportsEveryMissingSecretsField pins that check no longer stops at
// the first missing [secrets] field. It used to name `recipient` and return,
// hiding the missing identity_file until the user fixed the first one —
// while `doctor` reports both.
func TestCheck_ReportsEveryMissingSecretsField(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmp,
		"HOME":                  tmp,
		// AGENTSYNC_HOME outranks AGENTSYNC_TARGET_ROOT in paths.AgentsyncHome,
		// so an exported one would aim check at the developer's real home. No
		// identity file exists in this fixture, so the perm-check opt-out is not
		// in play here.
		"AGENTSYNC_HOME": "",
	}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	body := "[agents]\n[secrets]\nbackend = \"age\"\n"
	if err := os.WriteFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "check")
	if err == nil {
		t.Fatal("check must fail on an age backend with no recipient and no identity_file")
	}
	for _, want := range []string{"recipient", "identity_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("check error should name %q; got: %v", want, err)
		}
	}
}

// TestCheck_LabelsSecretsFailureWithItsField pins that check names the
// [secrets] key each failure came from. secrets.ValidateConfig carries the key
// in Finding.Field because several of its messages do not name it themselves,
// and the vault path is the sharp case: [secrets].file is optional, so the path
// below is the DEFAULT one — an unlabelled "<path> — not readable" would print
// a path this config never mentions.
func TestCheck_LabelsSecretsFailureWithItsField(t *testing.T) {
	tmp := t.TempDir()
	// AGENTSYNC_HOME outranks AGENTSYNC_TARGET_ROOT in paths.AgentsyncHome, so
	// an exported one would aim check at the developer's real home.
	env := map[string]string{
		"AGENTSYNC_TARGET_ROOT": tmp,
		"HOME":                  tmp,
		"AGENTSYNC_HOME":        "",
	}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	identity := filepath.Join(tmp, "age.key")
	if err := os.WriteFile(identity, []byte("# fixture identity\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(identity, 0o600); err != nil {
		t.Fatal(err)
	}
	// A self-referential symlink at the DEFAULT vault path (secrets.DefaultAgeFile
	// under the agentsync home): os.Stat follows it and fails with ELOOP, which
	// is not IsNotExist, so the validator reports it as a hard failure rather
	// than the "not yet created" warning an absent vault earns.
	vault := filepath.Join(tmp, ".agentsync", "secrets", "secrets.age")
	if err := os.MkdirAll(filepath.Dir(vault), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(vault, vault); err != nil {
		t.Fatal(err)
	}
	body := "[agents]\n[secrets]\nbackend       = \"age\"\n" +
		"recipient     = \"age1qqqq\"\nidentity_file = \"" + identity + "\"\n"
	if err := os.WriteFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "check")
	if err == nil {
		t.Fatal("check must fail on a vault path that stats with something other than ENOENT")
	}
	if !strings.Contains(err.Error(), "file: ") {
		t.Errorf("check error should label the failure with its [secrets] key; got: %v", err)
	}
	// The identity is present and 0600, so the vault is the only failure — if
	// identity_file appears, the label above is not the one being asserted.
	if strings.Contains(err.Error(), "identity_file") {
		t.Errorf("only the vault should fail in this fixture; got: %v", err)
	}
}
