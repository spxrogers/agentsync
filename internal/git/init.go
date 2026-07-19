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

Every apply records a checkpoint here. The FIRST apply also records a **pre-apply
baseline** — a commit of this directory's contents *as they were before* that apply
overwrote them — so even the very first apply is revertible (the baseline is the
apply checkpoint's parent).

    agentsync revert <agent>           # undo the most recent apply to this dir
    agentsync revert <agent> --to <ref>  # roll back to a specific checkpoint
`

// Init creates a new agentsync-owned git repo at dir: PlainInit, write the local-only
// NOTICE, stamp the [agentsync] managed=true marker into .git/config (so Detect
// recognizes it as ours), and best-effort tighten the .git dir to 0o700 (the history
// may carry cleartext secrets). Callers MUST gate on Detect == StateUntracked first;
// Init errors if a repo already exists at dir.
//
// ATOMIC-ish: the NOTICE is written BEFORE the marker is stamped, so Detect never
// reports StateAgentsyncOwned until the notice exists — a repo that Detects as owned
// ALWAYS carries the "do not push / may contain cleartext secrets" NOTICE. If any
// post-PlainInit step fails, the freshly-created .git is removed so Detect reverts to
// StateUntracked and the next apply retries cleanly, rather than leaving a
// marked-but-noticeless (or notice-less owned) cleartext-secret repo.
//
// The .git chmod is BEST-EFFORT: a chmod failure (e.g. Windows) never fails Init and
// is routed through tightenGitDir. A silent init-time failure is NOT swallowed — it is
// re-checked and surfaced on every apply's commit path via EnsureDirSecure.
func Init(dir string) (*Repo, error) {
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		return nil, fmt.Errorf("git init %s: %w", dir, err)
	}
	if err := finishInit(dir, repo); err != nil {
		// Roll back the half-initialized repo so Detect reverts to StateUntracked and
		// the next apply retries — never a marked-but-noticeless owned repo. Only the
		// .git we just created is removed; the caller's dir and files are untouched.
		_ = os.RemoveAll(filepath.Join(dir, ".git"))
		return nil, err
	}
	// The history may carry cleartext secrets; keep .git readable only by the user.
	// Best-effort — EnsureDirSecure re-asserts (and surfaces) this on every apply.
	_, _ = tightenGitDir(filepath.Join(dir, ".git"))
	return &Repo{dir: dir, repo: repo}, nil
}

// finishInit writes the local-only NOTICE and stamps the agentsync-managed marker, in
// that order: notice BEFORE marker, so Detect only ever reports StateAgentsyncOwned
// once the notice is present. Split out so Init can roll back the freshly-created .git
// on any failure here.
func finishInit(dir string, repo *gogit.Repository) error {
	if err := os.WriteFile(filepath.Join(dir, NoticeFile), []byte(noticeBody), 0o600); err != nil {
		return fmt.Errorf("writing %s in %s: %w", NoticeFile, dir, err)
	}
	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("reading fresh git config at %s: %w", dir, err)
	}
	cfg.Raw.Section(markerSection).SetOption(markerOption, markerValue)
	if err := repo.SetConfig(cfg); err != nil {
		return fmt.Errorf("writing agentsync marker to git config at %s: %w", dir, err)
	}
	return nil
}
