package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/state"
)

// unreadableStateJSON is a targets.json agentsync CANNOT read, used by every
// test in this file and by the marketplace pair in marketplace_state_test.go.
//
// The files key is a v1 key with more than one possible reading — issue #227's
// colon-bearing project root is exactly the user who hits this — and it sits
// alongside REAL ownership and marketplace records, so a test can tell "left
// alone" apart from "rewritten with the same bytes by luck".
const unreadableStateJSON = `{"schema_version":1,` +
	`"files":{"claude:project:${HOME}/a:${HOME}/b:${HOME}/c":{"sha256":"deadbeef"}},` +
	`"marketplaces":{"other-mp":{"url":"https://example.invalid/mp","head_sha":"cafe"}}}`

// stateRefusalMarker is the line migrate produces for unreadableStateJSON. A
// test asserting only "the command failed" would pass for any reason at all —
// a bad argument, a missing home — so every row also proves the failure it got
// is the state refusal it was after.
const stateRefusalMarker = "could not be read at schema_version=1"

// writeUnreadableState lays unreadableStateJSON down as tmp's targets.json and
// returns its path, guarding the premise: if state.Load ever stops refusing this
// document, every test built on it silently stops testing anything.
func writeUnreadableState(t *testing.T, tmp string) string {
	t.Helper()
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(unreadableStateJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Load(statePath); err == nil {
		t.Fatal("premise broken: state.Load must refuse this targets.json")
	}
	return statePath
}

// TestCommandsRefuseUnreadableState pins the behaviour docs/architecture.md §7.8
// and the CHANGELOG's downgrade note both assert: a targets.json agentsync
// cannot read stops the command, and the file is left exactly as it was found.
//
// It is the generalization of TestMarketplaceAdd_DoesNotClobberUnreadableState.
// state.Load returns a fresh EMPTY state with no error for an ABSENT file, so
// the tempting shape at each of these call sites is
//
//	s, err := state.Load(statePath)
//	if err != nil { s = state.New() }
//
// which is the bug round 1 fixed in addMarketplaceSource — and, before this
// test, regressing status.go or apply.go to exactly that passed the entire
// ./internal/... suite. For `apply` it is the worst of the family: an empty
// state owns nothing, so every managed destination is treated as unowned and
// backed up as a foreign collision on the next run — the mass-backup failure
// the typed key exists to prevent.
//
// The list is the eight commands the docs name, plus doctor. Every command that
// reads state is covered except the five that deliberately do NOT fail on it:
// `marketplace add`/`remove` skip the record and warn (their own tests in
// marketplace_state_test.go pin that, including the untouched file), and
// import's two sites plus opencode's Ingest warn and continue for the same
// reason. Nothing that reads state is left unpinned in one direction or the
// other.
func TestCommandsRefuseUnreadableState(t *testing.T) {
	tests := []struct {
		name string
		args []string
		// setup runs after `init` + `agent add claude` and before the state file
		// is written, for the rows that need more than a bare home.
		setup func(t *testing.T, tmp string)
	}{
		{name: "status", args: []string{"status"}},
		{name: "diff", args: []string{"diff"}},
		{name: "apply", args: []string{"apply"}},
		{name: "apply --dry-run", args: []string{"apply", "--dry-run"}},
		{name: "reconcile", args: []string{"reconcile"}},
		{name: "explain", args: []string{"explain", "~/.claude/CLAUDE.md"}},
		{name: "plugin outdated", args: []string{"plugin", "outdated"}},
		{
			// This one flips `enabled = false` in agentsync.toml BEFORE reaching
			// the purge, so it is not atomic — but the purge itself needs state to
			// know which destinations the agent owns, and it refuses rather than
			// deleting against an empty one. targets.json is untouched either way,
			// which is what this test is about.
			name: "agent disable --purge",
			args: []string{"agent", "disable", "claude", "--purge"},
		},
		{
			// `migrate subagents` short-circuits with "nothing to migrate" (exit 0)
			// when agents/ holds no *.md, never reaching state at all — so the row
			// needs a file to move. It moves agents/ → subagents/ and THEN refuses
			// at the state rewrite, which is why the assertion is on targets.json
			// rather than on the tree.
			name: "migrate subagents",
			args: []string{"migrate", "subagents"},
			setup: func(t *testing.T, tmp string) {
				t.Helper()
				dir := filepath.Join(tmp, ".agentsync", "agents")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				body := "---\nname: rev\ndescription: d\n---\nbody\n"
				if err := os.WriteFile(filepath.Join(dir, "rev.md"), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			// doctor is the one that reports rather than returns: it prints the
			// refusal as a failing check and exits non-zero through its own issue
			// count, so the marker lands on stdout instead of in the error.
			name: "doctor",
			args: []string{"doctor"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
			mustRun(t, env, "init")
			mustRun(t, env, "agent", "add", "claude")
			if tc.setup != nil {
				tc.setup(t, tmp)
			}
			statePath := writeUnreadableState(t, tmp)

			out, err := runCLI(t, env, tc.args...)
			if err == nil {
				t.Fatalf("%v must refuse an unreadable targets.json, not proceed against an empty state; got:\n%s", tc.args, out)
			}
			// Prove the refusal is the state one. Search the command's output AND
			// its error: the returning commands carry it in the error, doctor
			// prints it.
			if hay := out + "\n" + err.Error(); !strings.Contains(hay, stateRefusalMarker) {
				t.Fatalf("%v failed for some other reason than the state refusal (%v); got:\n%s", tc.args, err, out)
			}
			got, rerr := os.ReadFile(statePath)
			if rerr != nil {
				t.Fatalf("targets.json is gone after %v: %v", tc.args, rerr)
			}
			if string(got) != unreadableStateJSON {
				t.Fatalf("%v rewrote an unreadable targets.json, destroying its records:\n got: %s\nwant: %s",
					tc.args, got, unreadableStateJSON)
			}
		})
	}
}
