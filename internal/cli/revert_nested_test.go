package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	agit "github.com/spxrogers/agentsync/internal/git"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
	"github.com/spxrogers/agentsync/internal/ui"
)

// initForeignRepoWithFile builds a REAL foreign git repo at dir with rel committed as a
// foreign-tracked file — the on-disk fixture for issue #149's nested-repo cases (the bug
// is precisely that this checked-out file survives, so it must be a real filesystem
// repo, not a model).
func initForeignRepoWithFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fr, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	wt, err := fr.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(rel); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "user", Email: "user@example.com", When: time.Now()}
	if _, err := wt.Commit("foreign checkout", &gogit.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}
}

// TestRevertRootSkipsForeignNestedRepo is the core regression for issue #149's D2: an
// agentsync-owned root that has acquired a foreign nested repo since init must NOT be
// hard-reset. revertRoot refuses/skips it before any destructive write. Table over the
// default / strict / dry-run modes; the foreign checked-out file must be byte-for-byte
// unchanged in EVERY case.
func TestRevertRootSkipsForeignNestedRepo(t *testing.T) {
	testenv.RequireContainer(t)
	const sentinel = "keep me — foreign\n"

	setup := func(t *testing.T) (root, foreignFile string) {
		t.Helper()
		root = t.TempDir()
		r, err := agit.Init(root)
		if err != nil {
			t.Fatal(err)
		}
		// One agentsync checkpoint so root is a real owned backup.
		if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := r.Stage([]string{"settings.json"}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.CommitStaged("apply 1", agit.DefaultIdentity); err != nil {
			t.Fatal(err)
		}
		// A foreign repo cloned below the owned root AFTER init, tracking a sentinel file.
		skills := filepath.Join(root, "skills")
		initForeignRepoWithFile(t, skills, "keep.txt", sentinel)
		return root, filepath.Join(skills, "keep.txt")
	}

	cases := []struct {
		name    string
		dryRun  bool
		strict  bool
		wantErr bool
	}{
		{name: "default skips", dryRun: false, strict: false, wantErr: false},
		{name: "strict errors", dryRun: false, strict: true, wantErr: true},
		{name: "dry-run skips", dryRun: true, strict: false, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, foreignFile := setup(t)
			var out, errOut bytes.Buffer
			p := ui.New(&out, &errOut, ui.ColorNever)

			managed, err := revertRoot(p, root, "", tc.dryRun, agit.DefaultIdentity, nil, tc.strict)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("strict mode must error on a nested foreign repo; out=%q err=%q", out.String(), errOut.String())
				}
				if !strings.Contains(err.Error(), "nested git repository") {
					t.Fatalf("strict error should name the nested-repo hazard; got %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected a safe skip, got err: %v", err)
				}
				if !managed {
					t.Fatal("a nested-repo skip should still report managed=true (the dir IS an agentsync backup)")
				}
				if !strings.Contains(errOut.String(), "nested git repository") || !strings.Contains(errOut.String(), "skipping") {
					t.Fatalf("expected a skip warning naming the nested repo; err=%q", errOut.String())
				}
			}
			// In EVERY case the foreign checked-out file is byte-for-byte unchanged — no
			// hard reset ran under the owned root.
			if got, rerr := os.ReadFile(foreignFile); rerr != nil || string(got) != sentinel {
				t.Fatalf("foreign nested-repo file must be untouched; got %q err %v", got, rerr)
			}
		})
	}
}

// TestCommitPathWarnsOnNestedForeignRepo is the regression for issue #149's D1: when a
// foreign repo appears below an already-owned root, the apply/commit tail warns (still
// recording the harmless append-only checkpoint) so the user is told before ever running
// revert.
func TestCommitPathWarnsOnNestedForeignRepo(t *testing.T) {
	testenv.RequireContainer(t)
	target := t.TempDir()
	home := t.TempDir()
	t.Setenv("AGENTSYNC_TARGET_ROOT", target)
	if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte("[agents]\nclaude = { enabled = true }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(target, ".claude")
	settings := filepath.Join(claudeDir, "settings.json")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := registryFactory()
	cmd := &cobra.Command{Use: "apply"}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	commit := func(errW *bytes.Buffer) {
		p := ui.New(io.Discard, errW, ui.ColorNever)
		if err := runDestinationGitBackup(cmd, p, reg, []string{"claude"},
			adapter.ScopeUser, "", home,
			source.DestinationGitBackupConfig{Mode: source.GitBackupModeOn}, map[string]bool{settings: true}, false); err != nil {
			t.Fatalf("runDestinationGitBackup: %v", err)
		}
	}

	// First apply: init + checkpoint the owned repo. No warning yet.
	var errOut1 bytes.Buffer
	commit(&errOut1)
	if st, _ := agit.Detect(claudeDir); st != agit.StateAgentsyncOwned {
		t.Fatalf("precondition: claude dir should be owned, got %v", st)
	}
	if strings.Contains(errOut1.String(), "nested git repository") {
		t.Fatalf("no nested repo exists yet; unexpected warn: %q", errOut1.String())
	}
	repo, _ := agit.Open(claudeDir)
	before, _ := repo.Log(0)

	// A foreign repo appears below the owned root AFTER init.
	initForeignRepoWithFile(t, filepath.Join(claudeDir, "skills"), "keep.txt", "foreign\n")

	// Second apply (commit path over the owned repo): change a managed file so a new
	// checkpoint is recorded, and assert the warning is emitted to the Err stream.
	if err := os.WriteFile(settings, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut2 bytes.Buffer
	commit(&errOut2)
	if !strings.Contains(errOut2.String(), "nested git repository") {
		t.Fatalf("commit path should warn about the nested foreign repo; Err=%q", errOut2.String())
	}
	after, _ := repo.Log(0)
	if len(after) != len(before)+1 {
		t.Fatalf("append-only checkpoint should still be recorded: %d -> %d", len(before), len(after))
	}
	// The foreign checked-out file is untouched (the commit path is non-destructive).
	if got, _ := os.ReadFile(filepath.Join(claudeDir, "skills", "keep.txt")); string(got) != "foreign\n" {
		t.Fatalf("foreign file changed by the commit path: %q", got)
	}
}
