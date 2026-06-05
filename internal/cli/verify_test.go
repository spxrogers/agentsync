package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerify_UninitializedHomeErrors is the regression for verify printing a
// false "ok" (exit 0) when run before `init` — source.Load tolerates a
// missing home, so the user got a green light on a non-existent config.
func TestVerify_HalfInitMissingToml(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	// Scaffold component dirs/files but NO agentsync.toml (the half-init an
	// authoring command leaves when run before `init`). verify must flag it
	// rather than report a false "ok: schema valid".
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "x.toml")
	if err := os.MkdirAll(filepath.Dir(mcp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"y\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "verify")
	if err == nil {
		t.Fatal("verify on a home missing agentsync.toml must error")
	}
	if !strings.Contains(err.Error(), "agentsync.toml") || !strings.Contains(err.Error(), "init") {
		t.Fatalf("error should name agentsync.toml + point at init; got: %v", err)
	}
}

func TestVerify_UninitializedHomeErrors(t *testing.T) {
	tmp := t.TempDir()
	// Note: no `init` — the agentsync home does not exist.
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	out, err := runCLI(t, env, "verify")
	if err == nil {
		t.Fatalf("verify on uninitialized home should error; got ok:\n%s", out)
	}
	if !strings.Contains(err.Error(), "init") {
		t.Fatalf("error should point at `init`; got: %v", err)
	}
}

func TestVerify_Empty(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")

	out, err := runCLI(t, env, "verify")
	if err != nil {
		t.Fatalf("verify on empty home: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("verify output missing 'ok': %s", out)
	}
}

func TestVerify_BadTOML(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")

	badPath := filepath.Join(tmp, ".agentsync", "mcp", "broken.toml")
	_ = os.MkdirAll(filepath.Dir(badPath), 0o755)
	_ = os.WriteFile(badPath, []byte("[server\nmissing-bracket"), 0o644)

	_, err := runCLI(t, env, "verify")
	if err == nil {
		t.Fatal("verify should fail on malformed TOML")
	}
}

// TestVerify_AgeMissingRecipient asserts verify catches a half-configured
// [secrets] block (backend = age but no recipient).
func TestVerify_AgeMissingRecipient(t *testing.T) {
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

	out, err := runCLI(t, env, "verify")
	if err == nil {
		t.Fatalf("verify should fail when recipient is missing; got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Fatalf("verify should name the missing field; got err=%q out=%s", err, out)
	}
}

// TestVerify_AgeIdentityPerms asserts verify rejects a world-readable
// identity file.
func TestVerify_AgeIdentityPerms(t *testing.T) {
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

	out, err := runCLI(t, env, "verify")
	if err == nil {
		t.Fatalf("verify should fail on 0644 identity; got:\n%s", out)
	}
}

func TestVerify_UnknownAgent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	_, _ = runCLI(t, env, "init")
	_, _ = runCLI(t, env, "agent", "add", "claude")

	cfg := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body, _ := os.ReadFile(cfg)
	body = append(body, []byte("\n[agents]\nbogus = { enabled = true }\n")...)
	_ = os.WriteFile(cfg, body, 0o644)

	_, err := runCLI(t, env, "verify")
	if err == nil {
		t.Fatal("verify should reject unknown agent name")
	}
}

// TestVerify_HelpListsScopeFlags is the acceptance check that `verify --help`
// now documents --scope and --project, matching status/diff.
func TestVerify_HelpListsScopeFlags(t *testing.T) {
	out, _ := runCLI(t, nil, "verify", "--help")
	for _, flag := range []string{"--scope", "--project"} {
		if !strings.Contains(out, flag) {
			t.Fatalf("verify --help missing %q. Got:\n%s", flag, out)
		}
	}
}

// TestVerify_ProjectScope_OK schema-lints a project .agentsync/ tree and
// resolves its references via --project (which implies project scope). The
// project MCP server carries an ${env:} reference so the secret-resolution pass
// is exercised, not just the schema decode.
func TestVerify_ProjectScope_OK(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome, "PROJ_TOKEN": "s3cr3t"}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", proj); err != nil {
		t.Fatal(err)
	}
	// A valid project-scope MCP server referencing an env var that is set, so
	// the ${env:} resolution pass succeeds.
	mcp := filepath.Join(proj, ".agentsync", "mcp", "projapi.toml")
	body := "[server]\ntype = \"stdio\"\ncommand = \"node\"\nargs = [\"s.js\"]\nenv = { TOKEN = \"${env:PROJ_TOKEN}\" }\n"
	if err := os.WriteFile(mcp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "verify", "--project", proj)
	if err != nil {
		t.Fatalf("verify --project: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("verify --project output missing 'ok': %s", out)
	}
}

// TestVerify_ProjectScope_BadTOML asserts schema-linting reaches the project
// tree: a malformed file under <proj>/.agentsync/ fails verify at project scope.
func TestVerify_ProjectScope_BadTOML(t *testing.T) {
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

	if _, err := runCLI(t, env, "verify", "--scope", "project", "--project", proj); err == nil {
		t.Fatal("verify --scope project should fail on malformed project TOML")
	}
}

// TestVerify_ProjectScope_HalfInit asserts the half-init guard adapts to the
// project case: a <proj>/.agentsync/ directory with no agentsync.toml (so
// source.Load tolerates it and would otherwise report a false "ok") is rejected
// with a message naming agentsync.toml and the project init command.
func TestVerify_ProjectScope_HalfInit(t *testing.T) {
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

	_, err := runCLI(t, env, "verify", "--project", proj)
	if err == nil {
		t.Fatal("verify on a half-initialized project tree must error")
	}
	if !strings.Contains(err.Error(), "agentsync.toml") || !strings.Contains(err.Error(), "init") {
		t.Fatalf("error should name agentsync.toml + point at init; got: %v", err)
	}
}

// TestVerify_ProjectScope_NoTree asserts verify surfaces the shared
// scope-resolution error when --project points at a path with no .agentsync/
// tree, rather than silently degrading to user scope.
func TestVerify_ProjectScope_NoTree(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "verify", "--project", proj)
	if err == nil {
		t.Fatal("verify --project with no .agentsync/ tree should error")
	}
	if !strings.Contains(err.Error(), ".agentsync") {
		t.Fatalf("error should mention the missing .agentsync/ tree; got: %v", err)
	}
}
