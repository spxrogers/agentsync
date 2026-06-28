package git

import (
	"fmt"
	"os"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
)

// NoticeFile is written at the repo root on init and committed in the first
// checkpoint, so anyone browsing the history sees what it is and that it must not
// be pushed.
const NoticeFile = "AGENTSYNC_LOCAL_HISTORY.md"

const noticeBody = `# agentsync local-only rollback history

This git repository was created by **agentsync** (https://agentsync.cc) to version
the rendered agent config in this directory, so a bad ` + "`agentsync apply`" + ` can be
rolled back with ` + "`agentsync revert`" + ` (or plain ` + "`git revert` / `git checkout`" + `).

## Do not push this repository

- It is a **local-only** rollback history. agentsync never adds a remote and never
  pushes it.
- The files here are the *rendered* destination config, in which ` + "`${secret:…}`" + `
  references have been **resolved to cleartext**. This history therefore may
  contain secrets. Keeping it local is what makes that acceptable.
- The thing you commit and push in normal use is the canonical ` + "`~/.agentsync/`" + `
  source, which only ever holds secret *references* — not this directory.

## Rolling back

    agentsync revert <agent>           # undo the most recent apply to this dir
    agentsync revert <agent> --to <ref>  # roll back to a specific checkpoint
`

// Init creates a new agentsync-owned git repo at dir: PlainInit, stamp the
// [agentsync] managed=true marker into .git/config (so Detect recognizes it as
// ours), tighten the .git dir to 0o700 (the history may carry cleartext secrets),
// and write the local-only notice file. Callers MUST gate on Detect ==
// StateUntracked first; Init errors if a repo already exists at dir.
func Init(dir string) (*Repo, error) {
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		return nil, fmt.Errorf("git init %s: %w", dir, err)
	}
	cfg, err := repo.Config()
	if err != nil {
		return nil, fmt.Errorf("reading fresh git config at %s: %w", dir, err)
	}
	cfg.Raw.Section(markerSection).SetOption(markerOption, markerValue)
	if err := repo.SetConfig(cfg); err != nil {
		return nil, fmt.Errorf("writing agentsync marker to git config at %s: %w", dir, err)
	}
	// The history may carry cleartext secrets; keep .git readable only by the user.
	// Best-effort: a chmod failure (e.g. Windows) must not abort the apply, but on
	// POSIX it should normally succeed.
	_ = os.Chmod(filepath.Join(dir, ".git"), 0o700)
	if err := os.WriteFile(filepath.Join(dir, NoticeFile), []byte(noticeBody), 0o600); err != nil {
		return nil, fmt.Errorf("writing %s in %s: %w", NoticeFile, dir, err)
	}
	return &Repo{dir: dir, repo: repo}, nil
}
