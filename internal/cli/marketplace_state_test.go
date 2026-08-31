package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMarketplaceFixture lays down a minimal local marketplace directory that
// `marketplace add <path>` can fetch without touching the network.
func writeMarketplaceFixture(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name": "` + name + `", "owner": {"name": "x"}, "plugins": []}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestMarketplaceAdd_DoesNotClobberUnreadableState pins that a targets.json
// agentsync CANNOT read is left alone rather than replaced with an empty state.
//
// state.Load returns a fresh empty state with NO error for an ABSENT file, so a
// non-nil error means the file exists and is unreadable — which the typed-key
// migration made materially more reachable (an ambiguous v1 key, a v2 role
// violation, a non-UTF-8 key), and whose remedy text invites the user to keep
// running commands meanwhile. Falling back to state.New() and saving would
// discard every Files/Keys/Plugins entry and every other marketplace; the next
// apply would then own nothing and back up every managed destination as a
// foreign collision — the exact mass-backup failure the typed key exists to
// prevent. The record must be skipped instead.
//
// Skipping SILENTLY would be its own defect, so the test also pins the warning:
// without it the user reads a success line while status/apply/diff/doctor are
// all already failing on the same file, with nothing connecting the two.
// (Those commands' half of the contract — refuse outright — is
// TestCommandsRefuseUnreadableState.)
func TestMarketplaceAdd_DoesNotClobberUnreadableState(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	fixture := writeMarketplaceFixture(t, filepath.Join(tmp, "fixture-mp"), "test-mp")
	mustRun(t, env, "init")

	statePath := writeUnreadableState(t, tmp)

	out, err := runCLI(t, env, "marketplace", "add", fixture)
	if err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}
	// The add itself succeeded, so this is a warning beside the success line —
	// not a failure. It must name the file, or the user has nothing to go on.
	for _, want := range []string{"WARN", "not recorded", "targets.json", "agentsync doctor"} {
		if !strings.Contains(out, want) {
			t.Errorf("skipping the state record must warn and mention %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "added marketplace") {
		t.Errorf("the add still succeeded and must still say so; got:\n%s", out)
	}

	got, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != unreadableStateJSON {
		t.Fatalf("marketplace add rewrote an unreadable targets.json, destroying its records:\n got: %s\nwant: %s", got, unreadableStateJSON)
	}
}

// TestMarketplaceRemove_DoesNotClobberUnreadableState is the `remove` twin of
// the test above, and exists for the same reason: `marketplace remove` deletes
// marketplaces/<name>.toml and the cache, then edits the state record
// best-effort. Falling back to an empty state there would save away every
// ownership entry exactly as `add` once did, so the record is skipped — and,
// because the success line otherwise claims a clean removal while a stale
// record survives, the skip warns.
//
// Without this the warn half was unpinned: deleting the diagnostic left
// ./internal/cli/ green while its `add` twin stayed covered.
func TestMarketplaceRemove_DoesNotClobberUnreadableState(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	fixture := writeMarketplaceFixture(t, filepath.Join(tmp, "fixture-mp"), "test-mp")
	mustRun(t, env, "init")
	// Register it while the state file is still readable, so `remove` has
	// something to remove and reaches the state edit.
	mustRun(t, env, "marketplace", "add", fixture)

	statePath := writeUnreadableState(t, tmp)

	out, err := runCLI(t, env, "marketplace", "remove", "test-mp")
	if err != nil {
		t.Fatalf("marketplace remove: %v\n%s", err, out)
	}
	for _, want := range []string{"WARN", "record remains", "targets.json", "agentsync doctor"} {
		if !strings.Contains(out, want) {
			t.Errorf("skipping the state record must warn and mention %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "removed marketplace") {
		t.Errorf("the removal still succeeded and must still say so; got:\n%s", out)
	}

	got, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != unreadableStateJSON {
		t.Fatalf("marketplace remove rewrote an unreadable targets.json, destroying its records:\n got: %s\nwant: %s", got, unreadableStateJSON)
	}
}

// TestMarketplaceStateWarnings_CarryNoRawEscape sweeps the two warn sites above
// for the terminal-escape class (#93/#171). The warning interpolates a
// state.Load error, which names each unreadable key by copying the map key
// VERBATIM out of a targets.json the schema-2 remedy explicitly invites the user
// to hand-edit — and neither sink sanitizes on its own (p.Warnf and diag both
// reach fmt.Fprintf through ui.Fdiagf untouched), which is why marketplace.go
// calls sanitizeLines at both call sites.
//
// Like TestDoctor_CorruptStateDiagnosticCarriesNoRawEscape, this is an
// END-TO-END assertion and pins no single layer: migrate's %q already escapes
// the whole of ui.Sanitize's strip set, so removing sanitizeLines leaves this
// test green. That is the honest state of affairs — the sanitizeLines calls are
// the backstop for a future error constructor that forgets the %q, and
// marketplace.go says so.
func TestMarketplaceStateWarnings_CarryNoRawEscape(t *testing.T) {
	// A v1 key whose SCOPE field carries an ESC. "\u001b" is how JSON must
	// spell a control byte, so json.Unmarshal hands migrate a map key holding a real
	// one. "[2J" (clear screen) rather than a color code, so the assertion cannot
	// collide with the printer's own styling.
	const corrupt = `{"schema_version":1,"files":{"claude:no\u001b[2Jpe::x":{"sha256":"a"}}}`

	tests := []struct {
		name string
		// args is run once the corrupt state is in place; setup runs before.
		args  []string
		setup func(t *testing.T, tmp string, env map[string]string, fixture string)
	}{
		{
			name: "marketplace add",
			args: nil, // filled in below: the fixture path is per-test
		},
		{
			name: "marketplace remove",
			args: []string{"marketplace", "remove", "test-mp"},
			setup: func(t *testing.T, _ string, env map[string]string, fixture string) {
				t.Helper()
				mustRun(t, env, "marketplace", "add", fixture)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "NO_COLOR": "1"}
			fixture := writeMarketplaceFixture(t, filepath.Join(tmp, "fixture-mp"), "test-mp")
			mustRun(t, env, "init")
			if tc.setup != nil {
				tc.setup(t, tmp, env, fixture)
			}
			statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
			if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(statePath, []byte(corrupt), 0o644); err != nil {
				t.Fatal(err)
			}
			args := tc.args
			if args == nil {
				args = []string{"marketplace", "add", fixture}
			}
			out, err := runCLI(t, env, args...)
			if err != nil {
				t.Fatalf("%v: %v\n%s", args, err, out)
			}
			// The command succeeded and warned; the warning must name the key
			// with its escape stripped, not pass it through.
			assertSanitized(t, out, "[2Jpe")
		})
	}
}
