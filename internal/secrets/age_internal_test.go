package secrets

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestCheckIdentityPermissions pins the age-identity permission gate's contract
// directly (issue #167): the stat-error bypass, the 0o600 pass, the
// group/other-readable rejection with its remediation hint, and the
// AGENTSYNC_AGE_SKIP_PERM_CHECK override. FS-touching → container-gated + t.TempDir().
//
// The Windows no-op branch (runtimeIsWindows → nil regardless of mode) is NOT
// directly exercisable here: runtimeIsWindows is a plain func, not an overridable
// var, so it cannot be forced on a Linux container without a production seam
// (out of scope for this test-only pass — noted for a follow-up). The POSIX
// perm-mode rows are therefore gated on !runtimeIsWindows(), mirroring how
// git/perms_test.go skips on Windows; the empty-path and stat-error rows are
// OS-independent (they return before the Windows check) and run everywhere.
func TestCheckIdentityPermissions(t *testing.T) {
	testenv.RequireContainer(t)
	// Neutralize any inherited override so the perm-mode rows exercise the gate;
	// the override row sets it back to "1" within its own scope.
	t.Setenv(SkipPermCheckEnv, "")

	writeMode := func(t *testing.T, tmp, name string, mode os.FileMode) string {
		t.Helper()
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, []byte("AGE-SECRET-KEY-fake"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Explicit chmod: WriteFile's perm is umask-masked at creation, Chmod is not.
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}

	cases := []struct {
		name       string
		setup      func(t *testing.T, tmp string) string // returns the path to check
		skipPerm   bool                                  // set AGENTSYNC_AGE_SKIP_PERM_CHECK=1
		posixOnly  bool                                  // gate on !runtimeIsWindows()
		wantErr    bool
		wantSubstr []string // substrings the error message must contain
	}{
		{
			name:  "empty-path-is-nil",
			setup: func(_ *testing.T, _ string) string { return "" },
		},
		{
			name:  "stat-error-bypasses-the-gate",
			setup: func(_ *testing.T, tmp string) string { return filepath.Join(tmp, "does-not-exist") },
		},
		{
			name:      "mode-0600-passes",
			setup:     func(t *testing.T, tmp string) string { return writeMode(t, tmp, "id600", 0o600) },
			posixOnly: true,
		},
		{
			name:       "mode-0644-other-readable-rejected",
			setup:      func(t *testing.T, tmp string) string { return writeMode(t, tmp, "id644", 0o644) },
			posixOnly:  true,
			wantErr:    true,
			wantSubstr: []string{"0644", SkipPermCheckEnv},
		},
		{
			name:       "mode-0640-group-readable-rejected",
			setup:      func(t *testing.T, tmp string) string { return writeMode(t, tmp, "id640", 0o640) },
			posixOnly:  true,
			wantErr:    true,
			wantSubstr: []string{"0640"},
		},
		{
			name:       "mode-0604-other-readable-rejected",
			setup:      func(t *testing.T, tmp string) string { return writeMode(t, tmp, "id604", 0o604) },
			posixOnly:  true,
			wantErr:    true,
			wantSubstr: []string{"0604"},
		},
		{
			name:     "skip-perm-check-override-allows-0644",
			setup:    func(t *testing.T, tmp string) string { return writeMode(t, tmp, "id644skip", 0o644) },
			skipPerm: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.posixOnly && runtimeIsWindows() {
				t.Skip("POSIX perm bits are meaningless on Windows")
			}
			if tc.skipPerm {
				t.Setenv(SkipPermCheckEnv, "1")
			}
			path := tc.setup(t, t.TempDir())
			err := CheckIdentityPermissions(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("CheckIdentityPermissions(%q) = nil, want an insecure-permissions error", path)
				}
				for _, sub := range tc.wantSubstr {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("error %q does not mention %q", err.Error(), sub)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("CheckIdentityPermissions(%q) = %v, want nil", path, err)
			}
		})
	}

	// Document the Windows no-op: it needs GOOS=windows (runtimeIsWindows is a
	// func, not an overridable var), so on POSIX we only record the intent.
	t.Run("windows-no-op", func(t *testing.T) {
		if !runtimeIsWindows() {
			t.Skip("windows no-op branch requires GOOS=windows; runtimeIsWindows is not an overridable seam")
		}
		p := writeMode(t, t.TempDir(), "id644win", 0o644)
		if err := CheckIdentityPermissions(p); err != nil {
			t.Fatalf("on Windows the mode gate is a no-op, want nil, got %v", err)
		}
	})
}

// TestFlatten_RejectsAmbiguousDuplicateKey is the regression for a silent,
// nondeterministic secret-resolution footgun: two distinct TOML constructs —
// a quoted bare key literally named "github.token" and a nested [github]
// table with a token field — both flatten to the dotted key "github.token".
// The previous flatten kept whichever the random map iteration visited last,
// so `${secret:github.token}` could resolve to a different value run to run.
// flatten must reject the collision instead of silently picking one.
func TestFlatten_RejectsAmbiguousDuplicateKey(t *testing.T) {
	m := map[string]any{
		"github.token": "A",
		"github":       map[string]any{"token": "B"},
	}
	if _, err := flatten("", m); err == nil {
		t.Fatal("expected an error for an ambiguous duplicate flattened key, got nil")
	}
}

// TestFlatten_AllowsDistinctKeys confirms the collision guard does not reject
// legitimate, distinct nested keys.
func TestFlatten_AllowsDistinctKeys(t *testing.T) {
	m := map[string]any{
		"github": map[string]any{"token": "A", "user": "B"},
		"openai": map[string]any{"key": "C"},
	}
	out, err := flatten("", m)
	if err != nil {
		t.Fatalf("unexpected error on distinct keys: %v", err)
	}
	if out["github.token"] != "A" || out["github.user"] != "B" || out["openai.key"] != "C" {
		t.Fatalf("distinct keys flattened wrong: %+v", out)
	}
}

// TestAgeErrors_ChainWithoutRawPath pins BOTH properties of the age.go os-error
// sites at once: the chain survives (errors.Is sees fs.ErrNotExist through the
// pathlessErr unwrap) AND the error string never re-embeds the raw
// config-derived path — a *fs.PathError wrapped verbatim prints the
// unsanitized path, defeating the sanitized untrusted.Wrap operand beside it
// (the #93 escape-injection class; main.go prints errors raw).
func TestAgeErrors_ChainWithoutRawPath(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	hostile := filepath.Join(dir, "no\x1b[31mpe") // missing, ESC-bearing path
	b := NewAgeBackend(filepath.Join(dir, "secrets.age"), hostile)

	_, err := b.Resolve("k")
	if err == nil {
		t.Fatal("Resolve on a missing identity must error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error chain must surface fs.ErrNotExist; got %v", err)
	}
	if strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("error string re-embeds a raw ESC byte from the config-derived path: %q", err.Error())
	}
}
