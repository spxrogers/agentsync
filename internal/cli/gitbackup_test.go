package cli

import (
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	agit "github.com/spxrogers/agentsync/internal/git"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
	"github.com/spxrogers/agentsync/internal/ui"
)

// tracked reports whether rel is in the repo's HEAD tree.
func tracked(t *testing.T, dir, rel string) bool {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := c.Tree()
	if err != nil {
		t.Fatal(err)
	}
	_, err = tree.File(rel)
	return err == nil
}

// gitPlainInit creates a bare-bones (non-agentsync) repo, simulating a user's
// own source control over a destination dir.
func gitPlainInit(dir string) (*gogit.Repository, error) {
	return gogit.PlainInit(dir, false)
}

// stubPrompter overrides the interactive prompt to return res; the returned func
// restores the original.
func stubPrompter(res promptResult) func() {
	orig := gitBackupPrompter
	gitBackupPrompter = func(*cobra.Command, *ui.Printer, string) promptResult { return res }
	return func() { gitBackupPrompter = orig }
}

// loadMode returns the persisted [destination_directory_git_backup] mode.
func loadMode(t *testing.T, home string) string {
	t.Helper()
	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		t.Fatal(err)
	}
	return c.Config.DestinationGitBackup.Mode
}

// gbHarness wires runDestinationGitBackup against the real claude adapter rooted at
// a temp target root, with agentsync home at a second temp dir.
type gbHarness struct {
	t        *testing.T
	home     string // agentsync home (agentsync.toml lives here)
	target   string // AGENTSYNC_TARGET_ROOT
	claudDir string // <target>/.claude
	cmd      *cobra.Command
	p        *ui.Printer
	reg      *adapter.Registry
}

func newGBHarness(t *testing.T) *gbHarness {
	t.Helper()
	testenv.RequireContainer(t)
	target := t.TempDir()
	home := t.TempDir()
	t.Setenv("AGENTSYNC_TARGET_ROOT", target)
	// A minimal agentsync.toml so mode persistence has a file to edit.
	if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte("[agents]\nclaude = { enabled = true }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{Use: "apply"}
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	return &gbHarness{
		t:        t,
		home:     home,
		target:   target,
		claudDir: filepath.Join(target, ".claude"),
		cmd:      cmd,
		p:        ui.New(os.Stdout, os.Stderr, ui.ColorNever),
		reg:      registryFactory(),
	}
}

// writeManaged simulates apply having written a managed file under ~/.claude and
// returns the absolute path (for the `written` set).
func (h *gbHarness) writeManaged(rel, content string) string {
	h.t.Helper()
	abs := filepath.Join(h.claudDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return abs
}

func (h *gbHarness) run(cfg source.DestinationGitBackupConfig, written map[string]bool, noGitBackup bool) {
	h.t.Helper()
	err := runDestinationGitBackup(h.cmd, h.p, h.reg, []string{"claude"},
		adapter.ScopeUser, "", h.home, cfg, written, noGitBackup)
	if err != nil {
		h.t.Fatalf("runDestinationGitBackup: %v", err)
	}
}

func TestGitBackup_ModeOn_CheckpointsWrittenFiles(t *testing.T) {
	h := newGBHarness(t)
	abs := h.writeManaged("settings.json", `{"a":1}`)
	// An UNtracked user file in the same dir must not be swept into the commit.
	stray := filepath.Join(h.claudDir, "user-scratch.txt")
	if err := os.WriteFile(stray, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	h.run(source.DestinationGitBackupConfig{Mode: source.GitBackupModeOn}, map[string]bool{abs: true}, false)

	st, err := agit.Detect(h.claudDir)
	if err != nil {
		t.Fatal(err)
	}
	if st != agit.StateAgentsyncOwned {
		t.Fatalf("dir state = %v, want agentsync-owned", st)
	}
	repo, err := agit.Open(h.claudDir)
	if err != nil {
		t.Fatal(err)
	}
	cps, _ := repo.Log(0)
	if len(cps) != 1 {
		t.Fatalf("want 1 checkpoint, got %d", len(cps))
	}
	// The checkpoint tracks settings.json + the notice, NOT the stray user file.
	if !tracked(t, h.claudDir, "settings.json") {
		t.Error("settings.json should be tracked")
	}
	if !tracked(t, h.claudDir, agit.NoticeFile) {
		t.Error("notice file should be tracked")
	}
	if tracked(t, h.claudDir, "user-scratch.txt") {
		t.Error("untracked user file must NOT be committed")
	}
}

func TestGitBackup_NoOpPaths(t *testing.T) {
	cases := []struct {
		name     string
		cfg      source.DestinationGitBackupConfig
		written  func(h *gbHarness) map[string]bool
		noGit    bool
		wantRepo bool
	}{
		{name: "off", cfg: source.DestinationGitBackupConfig{Mode: source.GitBackupModeOff}, wantRepo: false},
		{name: "no-git-backup flag", cfg: source.DestinationGitBackupConfig{Mode: source.GitBackupModeOn}, noGit: true, wantRepo: false},
		{name: "empty written", cfg: source.DestinationGitBackupConfig{Mode: source.GitBackupModeOn}, written: func(*gbHarness) map[string]bool { return nil }, wantRepo: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGBHarness(t)
			written := map[string]bool{h.writeManaged("settings.json", "{}"): true}
			if tc.written != nil {
				written = tc.written(h)
			}
			h.run(tc.cfg, written, tc.noGit)
			st, _ := agit.Detect(h.claudDir)
			if (st == agit.StateAgentsyncOwned) != tc.wantRepo {
				t.Fatalf("repo created = %v, want %v (state %v)", st == agit.StateAgentsyncOwned, tc.wantRepo, st)
			}
		})
	}
}

func TestGitBackup_ProjectScopeIsNoOp(t *testing.T) {
	h := newGBHarness(t)
	abs := h.writeManaged("settings.json", "{}")
	err := runDestinationGitBackup(h.cmd, h.p, h.reg, []string{"claude"},
		adapter.ScopeProject, filepath.Join(h.target, "proj"), h.home,
		source.DestinationGitBackupConfig{Mode: source.GitBackupModeOn}, map[string]bool{abs: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := agit.Detect(h.claudDir)
	if st == agit.StateAgentsyncOwned {
		t.Fatal("project scope must not init the user-scope dir")
	}
}

func TestGitBackup_ForeignDirLeftAlone(t *testing.T) {
	h := newGBHarness(t)
	abs := h.writeManaged("settings.json", "{}")
	// Pre-existing user repo (no agentsync marker).
	if err := os.MkdirAll(h.claudDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := gitPlainInit(h.claudDir); err != nil {
		t.Fatal(err)
	}
	h.run(source.DestinationGitBackupConfig{Mode: source.GitBackupModeOn}, map[string]bool{abs: true}, false)
	st, _ := agit.Detect(h.claudDir)
	if st != agit.StateForeign {
		t.Fatalf("foreign dir state = %v, want foreign (agentsync must not adopt it)", st)
	}
}

func TestGitBackup_PromptYesPersistsOn(t *testing.T) {
	h := newGBHarness(t)
	abs := h.writeManaged("settings.json", "{}")
	restore := stubPrompter(promptYes)
	defer restore()

	h.run(source.DestinationGitBackupConfig{Mode: source.GitBackupModePrompt}, map[string]bool{abs: true}, false)

	if st, _ := agit.Detect(h.claudDir); st != agit.StateAgentsyncOwned {
		t.Fatalf("after prompt-yes, dir state = %v, want owned", st)
	}
	// Sticky: mode persisted to "on".
	if got := loadMode(t, h.home); got != source.GitBackupModeOn {
		t.Fatalf("persisted mode = %q, want on", got)
	}
}

func TestGitBackup_PromptNeverPersistsOff(t *testing.T) {
	h := newGBHarness(t)
	abs := h.writeManaged("settings.json", "{}")
	restore := stubPrompter(promptNever)
	defer restore()

	h.run(source.DestinationGitBackupConfig{Mode: source.GitBackupModePrompt}, map[string]bool{abs: true}, false)

	if st, _ := agit.Detect(h.claudDir); st == agit.StateAgentsyncOwned {
		t.Fatal("after prompt-never, dir must not be inited")
	}
	if got := loadMode(t, h.home); got != source.GitBackupModeOff {
		t.Fatalf("persisted mode = %q, want off", got)
	}
}

func TestInterpretPromptLine(t *testing.T) {
	cases := map[string]struct {
		want promptResult
		ok   bool
	}{
		"y\n": {promptYes, true}, "YES": {promptYes, true},
		"n": {promptNo, true}, "no\n": {promptNo, true},
		"d": {promptNever, true}, "don't": {promptNever, true},
		"": {promptNo, false}, "maybe": {promptNo, false},
	}
	for in, want := range cases {
		got, ok := interpretPromptLine(in)
		if ok != want.ok || (ok && got != want.want) {
			t.Errorf("interpretPromptLine(%q) = (%v,%v), want (%v,%v)", in, got, ok, want.want, want.ok)
		}
	}
}
