# Implementation Plan — Destination auto-git versioning + `agentsync revert`

**Date:** 2026-06-28
**Issue:** [#118](https://github.com/spxrogers/agentsync/issues/118)
**Spec:** [`docs/superpowers/specs/2026-06-28-destination-git-versioning-design.md`](../specs/2026-06-28-destination-git-versioning-design.md)
**PR:** [#119](https://github.com/spxrogers/agentsync/pull/119)

> **Note (post-implementation):** during review the design evolved in two ways the
> plan below predates — see the spec for the final design. (1) The optional adapter
> extension is **`VersionedDirs.VersionRoots() []string`** (a *set* of dirs), not
> `VersionedHome.HomeDir() (string,bool)`; **every** adapter implements it (deep +
> breadth), and the apply tail unions/de-nests/de-dups the roots so **shared
> cross-agent dirs like `~/.agents/skills` are versioned, deduped to one repo**.
> (2) `revert` takes the global lock and snapshots uncommitted tracked edits before
> its hard reset (no data loss).

---

## Goal

Give each **user-scope** destination agent dir (`~/.claude`, `~/.codex`, `~/.cursor`,
…) its own **local-only** git repo so every `apply` that changes managed files
leaves a checkpoint commit, and ship `agentsync revert` to roll a dir back to a
prior checkpoint (append-only). Add a `[destination_directory_git_backup]` config
table, an `apply --no-git-backup` bypass, and a read-only `agentsync doctor` status
line. Never push these repos.

## Architecture (where each piece lands)

```
internal/git/                    NEW package — the only go-git surface for this feature.
  git.go        Repo open/init/detect, Identity, marker, notice, perms.
  commit.go     Stage specific files + commit with the dedicated signature.
  restore.go    Append-only restore: materialize a checkpoint's tree, commit on top.
  log.go        List checkpoints (short hash, subject, time).
  (NO remote.go / push — the absence IS the never-push invariant.)

internal/adapter/adapter.go      NEW optional interface: VersionedHome.
internal/adapter/<each>/*.go     Implement HomeDir(scope, project) (string, bool).

internal/source/schema.go        NEW DestinationGitBackupConfig + Config field.
internal/project/project.go      Merge the new table in the project overlay.

internal/cli/gitbackup.go        NEW: apply-tail orchestration (group written → per
                                 agent → detect/prompt/init/commit) + mode persistence.
internal/cli/apply.go            Add --no-git-backup; call the orchestration after
                                 PruneBackups (line ~222).
internal/cli/revert.go           NEW `agentsync revert` command.
internal/cli/doctor.go           NEW "Destination git backup" section.
internal/cli/root.go             Register newRevertCmd().

docs/…, README.md, website/…, CHANGELOG.md, init.go initialAgentsyncTOML.
```

## Tech stack / facts the implementer must not re-derive

- **go-git is already vendored:** `github.com/go-git/go-git/v5 v5.18.0` (used today
  only by `git.PlainClone` in `internal/cli/init.go:244`). No new dependency.
- **The managed-file set** is the `written map[string]bool` returned by
  `render.Apply(...)` — see `internal/cli/apply.go:178`:
  `collisions, written, unchanged, applyErr := render.Apply(plan, reg, s, home, userHome, sc, projectRoot)`.
  Keys are **absolute** destination paths actually written this run.
- **The apply tail hook point** is `internal/cli/apply.go` immediately after
  `_ = render.PruneBackups(home, render.DefaultBackupKeep)` (line ~222) and before
  the success message (line ~224). State is already saved; `written`, `plan`, `reg`,
  `agents`, `sc`, `projectRoot`, `userHome`, `home`, `p` are all in scope.
- **Registry:** `reg.Lookup(name) adapter.Adapter` and `reg.Names() []string`
  (`internal/adapter/registry.go:25,28`). Adapters are constructed with
  `Options{TargetRoot: home}` where `home = paths.HomeDir(paths.OSEnv{})`
  (`internal/cli/registry_internal.go`). Registered: claude, opencode, codex,
  cursor, gemini, continuedev, windsurf, roo, cline + one `generic.New(spec,…)` per
  `generic.Specs()`.
- **Each adapter resolves its own config dir** via `ResolvePaths(targetRoot,
  project, projectScope)` returning a struct with a `ConfigDir` field (e.g.
  `cursor/paths.go:13` → `~/.cursor`). That `ConfigDir` at user scope IS the git
  root. (Claude additionally writes `~/.claude.json` at `$HOME` — **out of scope**,
  decision #10.)
- **TTY / non-interactive helpers already exist** in `internal/cli/apply.go`:
  `stdinIsTerminal(cmd)` (line 495) and `noInputFlag(cmd)` (line 481); the prompt
  idiom is `promptScopeChoice` (line 504, `bufio.NewReader(cmd.InOrStdin())`).
- **In-place `agentsync.toml` edits** preserve everything outside the edited
  section via a line-splice + `iox.AtomicWrite(p, …, 0o644)` — see
  `writeAgents`/`readAgentsyncTOML` in `internal/cli/agent.go:144–240`. Persisting
  `mode` follows the same pattern; do NOT `toml.Marshal` the whole `Config` (it
  nukes comments).
- **Atomic writes:** `iox.AtomicWrite(dest, data, mode)` (`internal/iox/atomic.go`)
  writes `0o600` first then chmods. Rendered files are already `0o600`.
- **`time.Now()` caveat applies only to `internal/render` / `internal/state`** —
  `internal/git` and `internal/cli` may call `time.Now().UTC()` directly (commit
  timestamps don't feed a hash). Confirmed in `CLAUDE.md`.

## Global constraints (apply to every task)

- **Stdlib testing only**, table-driven with a `name` field + `t.Run`; `t.Helper()`
  in fatal helpers. No testify/gomega.
- **FS-touching tests** use `t.TempDir()` (real FS — go-git needs a real worktree,
  `afero.MemMapFs` won't back go-git) and **must** call
  `testenv.RequireContainer(t)` / `MustRunInContainer()` so they refuse to run on a
  host without `AGENTSYNC_TEST_IN_CONTAINER=1`.
- **Errors** wrap: `fmt.Errorf("doing X: %w", err)`; match with `errors.Is/As`.
- **Never** add a git remote or call any push API anywhere under `internal/git`.
- **Commits**: conventional, scoped — `feat(git):`, `feat(adapter):`,
  `feat(cli):`, `feat(source):`, `docs(...):`, `test(...):`.
- Run `just build` then `just test-fast` after each task; the lint quirk for
  containers is `GOTOOLCHAIN=go1.26.2 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...`
  (see `CLAUDE.md` "container/cloud sessions" note). Re-stage anything `just lint`
  rewrites before committing.

## How to work this plan

Milestones are ordered so each builds on the last and stays green. **M0** (the
`internal/git` package) is fully isolated and unit-tested with zero agentsync
coupling — build and land it first. **M1** adds the canonical/adapter plumbing.
**M2** wires the apply tail. **M3** adds `revert`. **M4** is doctor + docs (the
`CLAUDE.md` doc-sync rule means docs land in the same PR as the behavior). Check
off each `[ ]` as you go.

---

# Milestone 0 — `internal/git` helper package (isolated, unit-tested)

No agentsync types here — pure git mechanics over a real directory. This is the
entire go-git surface for the feature; keeping it in one package makes the
never-push invariant auditable (there is simply no push function to call).

## Task 0.1 — Package skeleton, `Identity`, work-tree detection

**Files**
- create `internal/git/git.go`
- create `internal/git/git_test.go`

**Interface this task exposes**
```go
package git

// Identity is the author/committer for checkpoint commits.
type Identity struct{ Name, Email string }

// DefaultIdentity is used when the config overrides are empty.
var DefaultIdentity = Identity{Name: "agentsync", Email: "agentsync@localhost"}

// State classifies a destination dir for the apply tail.
type State int
const (
    StateUntracked      State = iota // no work tree anywhere at/above dir
    StateAgentsyncOwned              // a work tree WE created (carries the marker)
    StateForeign                     // a work tree the user owns (no marker) — leave alone
)

// Detect reports how dir is tracked. It uses DetectDotGit so a dir nested inside
// a user's existing repo (dotfiles) is StateForeign, not StateUntracked.
func Detect(dir string) (State, error)
```

**Steps**
- [ ] Write `git.go` with the import `gogit "github.com/go-git/go-git/v5"` and
  `"github.com/go-git/go-git/v5/config"`.
- [ ] Implement `Detect`:
  ```go
  const markerSection = "agentsync"
  const markerOption  = "managed"

  func Detect(dir string) (State, error) {
      repo, err := gogit.PlainOpenWithOptions(dir, &gogit.PlainOpenOptions{DetectDotGit: true})
      if errors.Is(err, gogit.ErrRepositoryNotExists) {
          return StateUntracked, nil
      }
      if err != nil {
          return StateUntracked, fmt.Errorf("opening git repo at %s: %w", dir, err)
      }
      cfg, err := repo.Config()
      if err != nil {
          return StateForeign, fmt.Errorf("reading git config at %s: %w", dir, err)
      }
      if cfg.Raw.Section(markerSection).Option(markerOption) == "true" {
          return StateAgentsyncOwned, nil
      }
      return StateForeign, nil
  }
  ```
- [ ] Write `git_test.go`. First line of every FS test body:
  `testenv.RequireContainer(t)`. Cases (table-driven where natural):
  - empty `t.TempDir()` → `StateUntracked`.
  - `gogit.PlainInit(dir, false)` with the marker set → `StateAgentsyncOwned`.
  - `gogit.PlainInit(dir, false)` WITHOUT the marker → `StateForeign`.
  - init a repo at `t.TempDir()`, make a child subdir, `Detect(child)` →
    `StateForeign` (proves `DetectDotGit`).

**Test command + expected**
```
AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/git/ -run TestDetect -count=1
# ok  github.com/spxrogers/agentsync/internal/git
```

**Commit:** `feat(git): add internal/git package with work-tree state detection`

## Task 0.2 — `Init`: create an agentsync-owned repo (marker, notice, perms)

**Files**
- create `internal/git/init.go`
- modify `internal/git/git_test.go` (add `TestInit`)

**Interface**
```go
// Init creates a new agentsync-owned git repo at dir: PlainInit, stamp the
// [agentsync] managed=true marker into .git/config, write the local-only notice
// file, tighten .git to 0o700. Idempotent-safe: errors if dir already holds a repo
// (callers gate on Detect == StateUntracked first).
func Init(dir string) (*Repo, error)

// Repo wraps a go-git repository + its absolute work-tree root.
type Repo struct {
    dir  string
    repo *gogit.Repository
}

// Open opens an existing agentsync-owned repo (no marker check here; callers use
// Detect). Returns the wrapper used by Commit/Restore/Log.
func Open(dir string) (*Repo, error)

const NoticeFile = "AGENTSYNC_LOCAL_HISTORY.md"
```

**Steps**
- [ ] `Init`:
  ```go
  func Init(dir string) (*Repo, error) {
      repo, err := gogit.PlainInit(dir, false)
      if err != nil {
          return nil, fmt.Errorf("git init %s: %w", dir, err)
      }
      // Marker — distinguishes our repo from a user's own (Detect reads it back).
      cfg, err := repo.Config()
      if err != nil {
          return nil, fmt.Errorf("read fresh git config: %w", err)
      }
      cfg.Raw.Section(markerSection).SetOption(markerOption, "true")
      if err := repo.SetConfig(cfg); err != nil {
          return nil, fmt.Errorf("write marker to git config: %w", err)
      }
      // Privacy: history may carry cleartext secrets; keep .git user-only.
      if err := os.Chmod(filepath.Join(dir, ".git"), 0o700); err != nil {
          return nil, fmt.Errorf("chmod .git: %w", err)
      }
      if err := os.WriteFile(filepath.Join(dir, NoticeFile), []byte(noticeBody), 0o600); err != nil {
          return nil, fmt.Errorf("write %s: %w", NoticeFile, err)
      }
      return &Repo{dir: dir, repo: repo}, nil
  }
  ```
- [ ] Define `noticeBody` (a `const string`) covering: local-only agentsync
  rollback history; MAY contain cleartext secrets; MUST NOT be pushed; roll back
  with `agentsync revert <agent>`.
- [ ] `Open`: `gogit.PlainOpen(dir)` → wrap; wrap `gogit.ErrRepositoryNotExists`
  in a clear error.
- [ ] `TestInit` (container): after `Init`, assert `.git` exists, `Detect` →
  `StateAgentsyncOwned`, `NoticeFile` exists with mode `0o600`, and (best-effort,
  skip on Windows) `.git` mode is `0o700`.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/git/ -run TestInit -count=1` → `ok`

**Commit:** `feat(git): Init creates agentsync-owned repo with marker + local-only notice`

## Task 0.3 — `Commit`: stage specific files + commit with the dedicated signature

**Files**
- create `internal/git/commit.go`
- modify `internal/git/git_test.go` (add `TestCommit`)

**Interface**
```go
// Commit stages exactly relPaths (slash-relative to the repo root) and records one
// commit authored by id. Returns the new commit hash. relPaths may include
// deletions (a path that no longer exists on disk is staged as a removal). Returns
// (zeroHash, nil) and commits nothing when there is nothing to stage.
func (r *Repo) Commit(relPaths []string, message string, id Identity) (string, error)
```

**Steps**
- [ ] Implement:
  ```go
  func (r *Repo) Commit(relPaths []string, message string, id Identity) (string, error) {
      wt, err := r.repo.Worktree()
      if err != nil {
          return "", fmt.Errorf("worktree: %w", err)
      }
      staged := 0
      for _, rel := range relPaths {
          // wt.Add stages both modifications and deletions for an existing index
          // entry; for a brand-new file it stages the addition.
          if _, err := wt.Add(rel); err != nil {
              return "", fmt.Errorf("git add %s: %w", rel, err)
          }
          staged++
      }
      if staged == 0 {
          return "", nil
      }
      if id.Name == "" { id = DefaultIdentity }
      sig := &object.Signature{Name: id.Name, Email: id.Email, When: time.Now().UTC()}
      h, err := wt.Commit(message, &gogit.CommitOptions{Author: sig, Committer: sig})
      if err != nil {
          return "", fmt.Errorf("git commit: %w", err)
      }
      return h.String(), nil
  }
  ```
- [ ] `TestCommit` (container): `Init` a dir, write `a.txt`, `Commit(["a.txt"],
  "first", DefaultIdentity)` → non-empty hash; `Log` (next task, or read via
  go-git directly in this test) shows author `agentsync <agentsync@localhost>`.
  Second case: overwrite `a.txt`, commit again → new distinct hash whose parent is
  the first. Third: empty `relPaths` → returns `("", nil)`, no new commit.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/git/ -run TestCommit -count=1` → `ok`

**Commit:** `feat(git): Commit stages named files and records the agentsync checkpoint`

## Task 0.4 — `Log`: list checkpoints

**Files**
- create `internal/git/log.go`
- modify `internal/git/git_test.go` (add `TestLog`)

**Interface**
```go
// Checkpoint is one commit in a destination repo's history.
type Checkpoint struct {
    Hash    string    // full hash
    Short   string    // first 7 chars
    Subject string    // first line of the message
    When    time.Time
}

// Log returns up to n checkpoints, newest first (n<=0 → all).
func (r *Repo) Log(n int) ([]Checkpoint, error)

// Resolve turns a revision (e.g. "HEAD", "HEAD~1", a short/long hash) into a full
// hash, erroring clearly if it doesn't resolve.
func (r *Repo) Resolve(rev string) (string, error)
```

**Steps**
- [ ] `Log`: `r.repo.Log(&gogit.LogOptions{})` → iterate `iter.ForEach`, build
  `Checkpoint`s, stop at `n`. Subject = `strings.SplitN(c.Message, "\n", 2)[0]`.
- [ ] `Resolve`: `r.repo.ResolveRevision(plumbing.Revision(rev))`; wrap a
  not-found in `fmt.Errorf("no such checkpoint %q in %s: %w", rev, r.dir, err)`.
- [ ] `TestLog` (container): three commits → `Log(0)` returns 3 newest-first;
  `Log(2)` returns 2; `Resolve("HEAD~1")` equals the 2nd-newest hash;
  `Resolve("deadbeef")` errors.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/git/ -run TestLog -count=1` → `ok`

**Commit:** `feat(git): Log + Resolve for browsing destination checkpoints`

## Task 0.5 — `Restore`: append-only roll-back to a checkpoint

**Files**
- create `internal/git/restore.go`
- modify `internal/git/git_test.go` (add `TestRestore`)

**Interface**
```go
// FileChange describes one file a restore would touch (for --dry-run preview).
type FileChange struct {
    Path string
    Kind string // "modify" | "create" | "delete"
}

// Plan computes what Restore(target) would change vs the current worktree HEAD,
// WITHOUT writing anything.
func (r *Repo) Plan(targetRev string) (targetHash string, changes []FileChange, err error)

// Restore makes the worktree match the targetRev checkpoint and records the result
// as a NEW commit on top of HEAD (append-only — HEAD advances, nothing is
// rewritten or lost). Returns the new commit hash. Commits nothing and returns
// (zeroHash, nil) when the worktree already matches target.
func (r *Repo) Restore(targetRev, message string, id Identity) (string, error)
```

**Steps**
- [ ] Helper `treeOf(rev)`: `Resolve` → `r.repo.CommitObject(hash)` → `c.Tree()`.
- [ ] `Plan`: diff HEAD tree vs target tree:
  ```go
  headTree, _ := r.headTree()      // CommitObject(HEAD).Tree()
  tgtHash, _ := r.Resolve(targetRev)
  tgtTree, _ := r.treeOfHash(tgtHash)
  changes, err := headTree.Diff(tgtTree)   // object.Changes: HEAD -> target
  // For each *object.Change: action, _ := ch.Action()
  //   merkletrie.Insert -> file exists in target, not HEAD  => Kind "create"
  //   merkletrie.Delete -> file exists in HEAD,  not target => Kind "delete"
  //   merkletrie.Modify -> Kind "modify"
  ```
  Build `[]FileChange` (use `ch.To.Name` for create/modify, `ch.From.Name` for
  delete).
- [ ] `Restore`: call `Plan`; if no changes return `("", nil)`. Else apply each
  change to the worktree on the real FS under `r.dir`:
  - create/modify → read target blob (`tgtTree.File(name)` → `f.Contents()`),
    `os.MkdirAll(parent, 0o700)`, `os.WriteFile(abs, []byte(content), 0o600)`.
  - delete → `os.Remove(abs)`.
  Collect the touched rel-paths, then `r.Commit(touched, message, id)` to record
  the append-only checkpoint.
- [ ] `TestRestore` (container): commit `v1` of `a.txt`; commit `v2`; add `b.txt`
  in a 3rd commit. `Restore("HEAD~2", "revert", id)`:
  - `a.txt` bytes == `v1`, `b.txt` is gone.
  - a NEW commit exists whose parent is the prior HEAD (assert via `Log` length +
    parent), proving append-only (HEAD~2 commit still reachable in `Log`).
  - `Plan` on an already-matching target returns no changes; `Restore` returns
    `("", nil)`.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/git/ -run TestRestore -count=1` → `ok`

**Commit:** `feat(git): append-only Restore to roll a worktree back to a checkpoint`

## Task 0.6 — Never-push guard test

**Files**
- create `internal/git/nopush_test.go`

**Steps**
- [ ] A reflection/source guard that fails if the package ever grows a remote/push
  surface. Simplest robust form: statically scan the package's own `.go` source
  (non-test) for the tokens `Push`, `CreateRemote`, `remote(`, `Remote(`:
  ```go
  func TestNoPushSurface(t *testing.T) {
      files, _ := filepath.Glob("*.go")
      for _, f := range files {
          if strings.HasSuffix(f, "_test.go") { continue }
          b, err := os.ReadFile(f); if err != nil { t.Fatal(err) }
          for _, bad := range []string{"Push", "CreateRemote", ".Remote(", "Remotes("} {
              if bytes.Contains(b, []byte(bad)) {
                  t.Errorf("%s references %q — internal/git must never push or add a remote (issue #118)", f, bad)
              }
          }
      }
  }
  ```
- [ ] Document the rule in the package doc comment so the intent is discoverable.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/git/ -count=1` → all green.

**Commit:** `test(git): guard that internal/git exposes no remote/push surface`

---

# Milestone 1 — canonical config + `VersionedHome`

## Task 1.1 — `DestinationGitBackupConfig` in the canonical schema

**Files**
- modify `internal/source/schema.go`
- create/extend `internal/source/schema_test.go` (or the existing loader test) with
  a parse case.

**Steps**
- [ ] Add to `Config` (after `Memory`):
  ```go
  DestinationGitBackup DestinationGitBackupConfig `toml:"destination_directory_git_backup"`
  ```
- [ ] Add the struct + mode constants:
  ```go
  // DestinationGitBackupConfig mirrors [destination_directory_git_backup] in
  // agentsync.toml. It controls whether `apply` git-versions the rendered
  // destination dirs (a local-only rollback history; never pushed — see issue
  // #118 and the secret-handling note in CLAUDE.md). Empty Mode == ModePrompt.
  type DestinationGitBackupConfig struct {
      Mode        string `toml:"mode,omitempty"`         // "prompt" | "on" | "off"
      AuthorName  string `toml:"author_name,omitempty"`
      AuthorEmail string `toml:"author_email,omitempty"`
  }

  const (
      GitBackupModePrompt = "prompt" // default: ask on first untracked write
      GitBackupModeOn     = "on"     // init + checkpoint silently
      GitBackupModeOff    = "off"    // never init/commit/prompt
  )

  // EffectiveMode returns Mode or GitBackupModePrompt when unset.
  func (g DestinationGitBackupConfig) EffectiveMode() string {
      if g.Mode == "" { return GitBackupModePrompt }
      return g.Mode
  }
  ```
- [ ] Test: unmarshal a fixture with
  `[destination_directory_git_backup]\nmode = "on"\nauthor_name = "x"` and assert
  the fields; assert an absent table yields `EffectiveMode() == "prompt"`.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/source/ -run GitBackup -count=1` → `ok`

**Commit:** `feat(source): add [destination_directory_git_backup] to the canonical schema`

## Task 1.2 — project-overlay merge for the new table

**Files**
- modify `internal/project/project.go` (the `Merge` function — see how
  `UpdateDefaults`/`MemoryConfig` are merged today)
- modify the project merge test.

**Steps**
- [ ] In `project.Merge`, after the existing `Updates`/`Memory` merge, override the
  base `DestinationGitBackup` with the project's non-zero fields (project wins when
  set; inherit base otherwise), matching the precedence the sibling tables use.
  (Functionally this is a user-scope concern, but keeping the merge symmetric
  avoids a surprising "project config silently ignored" gap.)
- [ ] Test: base mode `"on"`, project mode `""` → merged `"on"`; project mode
  `"off"` → merged `"off"`.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/project/ -count=1` → `ok`

**Commit:** `feat(project): merge [destination_directory_git_backup] in the project overlay`

## Task 1.3 — `VersionedHome` optional adapter interface + implementations

**Files**
- modify `internal/adapter/adapter.go` (declare the interface)
- modify each deep adapter: `internal/adapter/{claude,opencode,codex,cursor,gemini,continuedev,windsurf,roo,cline}/*.go`
- modify `internal/adapter/generic/*.go`
- create `internal/adapter/versionedhome_test.go` (registry-wide behavioral guard)

**Interface (in `adapter.go`, near `PluginIngester`/`WarnEmitter`)**
```go
// VersionedHome is an OPTIONAL Adapter extension. An adapter that writes into a
// single coherent on-disk config directory declares it so the apply tail can
// git-init and checkpoint that directory (issue #118). Adapters with no single
// versionable root return ("", false).
//
// Read-only and user-scope-focused: HomeDir reports the dir to back up; it does
// NOT widen the render/Apply contract. At ScopeProject it SHOULD return ("",false)
// — project destinations live inside the user's own project repo and are left to
// that repo's source control.
type VersionedHome interface {
    HomeDir(scope Scope, project string) (string, bool)
}
```

**Steps**
- [ ] Add the interface to `adapter.go` with the doc comment above.
- [ ] For each deep adapter, add a method that returns its user-scope `ConfigDir`.
  Each adapter already stores its target root (`a.opts.TargetRoot`) and has a
  `ResolvePaths`. Example (cursor):
  ```go
  func (a *Adapter) HomeDir(scope adapter.Scope, project string) (string, bool) {
      if scope != adapter.ScopeUser {
          return "", false
      }
      cd := ResolvePaths(a.opts.TargetRoot, "", false).ConfigDir
      if cd == "" {
          return "", false
      }
      return cd, true
  }
  ```
  Mirror per adapter using that adapter's own paths accessor + `ConfigDir`/root
  field name. **Claude:** return `~/.claude` (its `ConfigDir`), NOT `$HOME` — the
  stray `~/.claude.json` is intentionally excluded (decision #10).
- [ ] **Generic adapter:** implement `HomeDir` by reading the dir from
  `generic.Spec` (the Spec already encodes the agent's config dir/root used by its
  Render paths). Return ("", false) only if a Spec genuinely has no single root.
- [ ] Guard test `versionedhome_test.go`: build `registryFactory()`, iterate
  `reg.Names()`, assert **every** registered adapter implements `VersionedHome` and
  returns a non-empty absolute dir at `ScopeUser` that sits under the test target
  root, AND returns `("", false)` at `ScopeProject`. (This is the analogue of
  `TestEveryAdapterClassifiesSkips` — it pins the capability in code so a new
  adapter can't silently skip git-backup support.) Use `AGENTSYNC_TARGET_ROOT` via
  the existing test env helpers so the dirs resolve under a temp root.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/adapter/... -run VersionedHome -count=1` → `ok`

**Commit:** `feat(adapter): VersionedHome extension exposing each agent's versionable dir`

## Task 1.4 — persist `mode` back to `agentsync.toml` (comment-preserving)

**Files**
- create `internal/cli/gitbackup_config.go`
- create `internal/cli/gitbackup_config_test.go`

**Interface**
```go
// setDestinationGitBackupMode writes mode into the [destination_directory_git_backup]
// table of ~/.agentsync/agentsync.toml, preserving everything outside that table
// (comments, key order, other sections) via a line-splice — never a full marshal.
// Creates the table if absent.
func setDestinationGitBackupMode(home, mode string) error
```

**Steps**
- [ ] Implement with the same line-splice strategy as `writeAgents`
  (`agent.go:185`): read raw, drop the existing
  `[destination_directory_git_backup]` section's lines, splice in a regenerated
  block (`mode = "<mode>"` plus any preserved `author_name`/`author_email`), write
  via `iox.AtomicWrite(p, …, 0o644)`. Re-emit only the `mode` line plus author
  lines that were already present (parse them out before dropping), so an edit
  doesn't silently delete a user's identity overrides.
- [ ] Test (container): start from a fixture `agentsync.toml` with comments and an
  `[updates]` table; `setDestinationGitBackupMode(home, "on")` then reload via
  `source.Load` → mode `"on"`, `[updates]` intact, leading comments intact. Run it
  twice → no duplicate table, spacing stable (mirror the `writeAgents` idempotency
  assertion).

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/cli/ -run GitBackupConfig -count=1` → `ok`

**Commit:** `feat(cli): persist destination-git-backup mode without clobbering agentsync.toml`

---

# Milestone 2 — apply-tail checkpoint hook

## Task 2.1 — `apply --no-git-backup` flag

**Files**
- modify `internal/cli/apply.go`

**Steps**
- [ ] Add `var noGitBackup bool` in `newApplyCmd`; register
  `cmd.Flags().BoolVar(&noGitBackup, "no-git-backup", false, "skip destination git
  versioning/checkpoint for this run (CI/scripting); does not modify
  agentsync.toml")`.
- [ ] Thread it into `applyRun` (add a `noGitBackup bool` param; pass through both
  call sites at lines 42 and 45).
- [ ] No behavior yet beyond plumbing; build only.

**Test:** `go build ./... && AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/cli/ -run Apply -count=1` → `ok` (existing apply tests still pass).

**Commit:** `feat(cli): add apply --no-git-backup flag (plumbing)`

## Task 2.2 — checkpoint orchestration (group `written` → per agent → init/commit)

**Files**
- create `internal/cli/gitbackup.go`
- create `internal/cli/gitbackup_test.go`

**Interface**
```go
// runDestinationGitBackup checkpoints each user-scope agent dir that got managed
// writes this apply. It is a no-op (returns nil) for: project scope, --no-git-backup,
// mode "off", an empty `written` set, or an agent whose dir is foreign-tracked.
// Best-effort by contract: a git failure for one agent is reported to p.Err and
// does NOT fail the apply (the files are already written + state saved).
func runDestinationGitBackup(
    cmd *cobra.Command, p *ui.Printer, reg *adapter.Registry, agents []string,
    sc adapter.Scope, projectRoot, home, userHome string,
    cfg source.DestinationGitBackupConfig, written map[string]bool, noGitBackup bool,
) error
```

**Steps**
- [ ] Early returns: `sc != adapter.ScopeUser`, `noGitBackup`, or
  `cfg.EffectiveMode() == GitBackupModeOff`, or `len(written) == 0` → return nil.
- [ ] Build the dedicated identity: `id := agitgit.Identity{Name: cfg.AuthorName,
  Email: cfg.AuthorEmail}` (empty fields fall back to `DefaultIdentity` inside
  `Commit`). (Alias the package, e.g. `import agit "…/internal/git"`, to avoid
  colliding with go-git if both are ever imported.)
- [ ] For each enabled agent name in `agents`:
  - `ad := reg.Lookup(name)`; `vh, ok := ad.(adapter.VersionedHome)`; if `!ok`
    continue.
  - `dir, ok := vh.HomeDir(sc, projectRoot)`; if `!ok` continue.
  - Select this agent's written files under `dir`:
    ```go
    var rels []string
    for abs := range written {
        if underDir(dir, abs) {            // filepath.Rel + no ".." prefix
            rels = append(rels, relSlash(dir, abs))
        }
    }
    if len(rels) == 0 { continue }         // nothing managed under this dir changed
    sort.Strings(rels)
    ```
  - `st, err := agit.Detect(dir)`; on error → warn to `p.Err`, continue.
  - `switch st`:
    - `StateForeign` → optional one-line hint (only once per dir), continue (decision #5).
    - `StateUntracked` → consult `mode`:
      - `GitBackupModeOn` → `repo, err := agit.Init(dir)`.
      - `GitBackupModePrompt` → call `promptInitGitBackup(cmd, p, dir)` (Task 2.3).
        On "yes" → `Init` + persist `setDestinationGitBackupMode(home, "on")`. On
        "no" → continue. On "no, don't ask again" → persist
        `setDestinationGitBackupMode(home, "off")` and continue. On no-TTY/no-input
        → print a one-line hint and continue (can't ask).
    - `StateAgentsyncOwned` → `repo, err := agit.Open(dir)`.
  - With `repo` in hand: also stage the notice file if it's new (Init already wrote
    it; include `agit.NoticeFile` in `rels` on the first commit so it's tracked).
  - `msg := fmt.Sprintf("agentsync apply: %s (%s) — %d file(s)\n\n%s", name, sc, len(rels), strings.Join(rels, "\n"))`.
  - `h, err := repo.Commit(rels, msg, id)`; on error → warn, continue. On success
    with non-empty `h` → optional faint confirmation line to `p.Err`.
- [ ] Helpers `underDir(dir, abs) bool` and `relSlash(dir, abs) string`
  (`filepath.Rel` + `filepath.ToSlash`, reject `..`).
- [ ] `gitbackup_test.go` (container, real FS): drive the orchestration directly
  (not through full apply) with a fake registry whose adapter's `HomeDir` returns a
  temp dir and a `written` map of files under it. Assert:
  - mode `"on"`, untracked dir → exactly one commit, contains the written files +
    notice, NOT unrelated files placed in the dir.
  - empty `written` → no repo created.
  - foreign dir (pre-`PlainInit` without marker) → no agentsync commits added.
  - `--no-git-backup` / mode `"off"` → no-op.
  - project scope → no-op.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/cli/ -run GitBackup -count=1` → `ok`

**Commit:** `feat(cli): checkpoint each user-scope agent dir after a writing apply`

## Task 2.3 — the opt-out init prompt

**Files**
- modify `internal/cli/gitbackup.go`
- modify `internal/cli/gitbackup_test.go`

**Interface**
```go
type promptResult int
const (
    promptYes promptResult = iota
    promptNo
    promptNever      // "no, and don't ask again"
    promptUnavailable // no TTY / --no-input — caller skips silently with a hint
)

func promptInitGitBackup(cmd *cobra.Command, p *ui.Printer, dir string) promptResult
```

**Steps**
- [ ] If `noInputFlag(cmd) || !stdinIsTerminal(cmd)` → `promptUnavailable`.
- [ ] Otherwise prompt with the `bufio.NewReader(cmd.InOrStdin())` idiom from
  `promptScopeChoice` (apply.go:504). Offer `[y] yes  [n] not now  [d] don't ask
  again`, re-prompting on invalid input up to 5 times; EOF → `promptNo`. Make the
  copy explicit that this creates a **local-only, never-pushed** history that may
  contain secrets.
- [ ] Tests: feed `cmd.SetIn(strings.NewReader("y\n"))` etc. — but note these run
  via the orchestration which gates on `stdinIsTerminal`; for unit-testing the
  prompt parsing, test `promptInitGitBackup` with an injected reader and a forced
  TTY shim, OR (simpler) test the three persisted outcomes through the
  orchestration with `--no-input` unset by stubbing the terminal check. Keep at
  least: "y" → repo created + mode persisted `"on"`; "d" → no repo + mode `"off"`;
  no-TTY → no repo, mode unchanged.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/cli/ -run GitBackupPrompt -count=1` → `ok`

**Commit:** `feat(cli): opt-out prompt to git-init a destination dir on first apply`

## Task 2.4 — wire the orchestration into the apply tail

**Files**
- modify `internal/cli/apply.go`

**Steps**
- [ ] After `_ = render.PruneBackups(home, render.DefaultBackupKeep)` (line ~222)
  and before the success print (line ~224), insert:
  ```go
  // Destination git backup (issue #118): checkpoint the user-scope agent dirs we
  // just wrote, so a bad apply is a `git revert` / `agentsync revert` away. Local-
  // only, never pushed. Best-effort — never fails an otherwise-successful apply.
  if err := runDestinationGitBackup(cmd, p, reg, agents, sc, projectRoot, home, userHome,
      c.Config.DestinationGitBackup, written, noGitBackup); err != nil {
      fmt.Fprintf(p.Err, "%s destination git backup: %v\n", p.Yellow("agentsync:"), err)
  }
  ```
  (`c` is the loaded canonical in scope; `c.Config.DestinationGitBackup` carries
  the merged config.)
- [ ] Full integration test in `internal/cli` (real FS, container): seed a minimal
  canonical with one enabled agent, `mode = "on"`, run the real `apply` command
  end-to-end, assert the agent dir is now an agentsync-owned repo with one
  checkpoint; run `apply` again with no source change → no second commit (the
  "apply changed nothing" path leaves `written` empty/unchanged). Use the existing
  apply integration-test harness/fixtures as the template.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/cli/ -run Apply -count=1` → `ok`

**Commit:** `feat(cli): run destination git backup in the apply tail`

---

# Milestone 3 — `agentsync revert`

## Task 3.1 — command skeleton + flags

**Files**
- create `internal/cli/revert.go`
- modify `internal/cli/root.go` (register `newRevertCmd()`)

**Steps**
- [ ] Model on `newVerifyCmd`/`newDoctorCmd`. Surface:
  ```
  agentsync revert <agent> [--to <ref>] [--all] [--dry-run]
  ```
  Flags: `--to` (string), `--all` (bool), `--dry-run` (bool). `Args:
  cobra.MaximumNArgs(1)`. Reject `--all` together with a positional `<agent>`
  (`return fmt.Errorf("--all reverts every managed dir; do not also pass an agent")`).
  Require exactly one of {positional agent, `--all`}.
- [ ] Add `newRevertCmd()` to the `cmd.AddCommand(...)` block in `root.go:42`.
- [ ] Wire `home`, `reg := registryFactory()`, `userHome`, and resolve target dirs
  via `VersionedHome.HomeDir(adapter.ScopeUser, "")` for the named agent (or all
  registered agents for `--all`).

**Test:** `go build ./... && agentsync revert --help` shows the surface; an unknown
agent name errors. Add a `internal/cli` test for arg validation.

**Commit:** `feat(cli): scaffold agentsync revert command`

## Task 3.2 — revert behavior (resolve target, restore, out-of-sync notice)

**Files**
- modify `internal/cli/revert.go`
- create `internal/cli/revert_test.go`

**Steps**
- [ ] For the target agent: `dir, ok := vh.HomeDir(...)`; `st, _ := agit.Detect(dir)`.
  - `st != StateAgentsyncOwned` → clear error: not an agentsync-managed repo;
    suggest enabling backups + running `apply` first.
- [ ] `repo, _ := agit.Open(dir)`. Resolve the target rev:
  - `--to` set → that rev.
  - default → `"HEAD~1"` (undo the most recent apply — decision #17). If the repo
    has only one commit, error: nothing earlier to revert to.
- [ ] `--dry-run` → `repo.Plan(target)` → print target short hash + subject (via
  `Log`) and the `[]FileChange`; exit without writing.
- [ ] Else `repo.Restore(target, fmt.Sprintf("agentsync revert: %s → %s", name, short), id)`.
  On a no-op (`("", nil)`) tell the user the dir already matches that checkpoint.
- [ ] Always print the out-of-sync notice on a real revert (decision #4):
  > `agentsync revert` completed. The destination directory `<dir>` is now out of
  > sync with the agentsync configuration. Please reconcile as needed (e.g.
  > `agentsync reconcile` / `agentsync import`) before your next `agentsync apply`
  > to avoid re-losing these changes.
- [ ] Tests (container): build a repo with 2 checkpoints (simulate two applies by
  writing+`Commit` directly, or by running apply twice). `revert <agent>`:
  - files match the earlier checkpoint; a NEW commit exists (append-only);
  - stdout carries the out-of-sync notice.
  - `--to <hash>` targets a specific checkpoint.
  - `--dry-run` writes nothing (worktree unchanged) and lists the changes.
  - un-inited dir / unknown `--to` → clear errors, no writes.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/cli/ -run Revert -count=1` → `ok`

**Commit:** `feat(cli): agentsync revert restores a destination dir to a checkpoint`

## Task 3.3 — `--all`

**Files**
- modify `internal/cli/revert.go`
- modify `internal/cli/revert_test.go`

**Steps**
- [ ] `--all`: iterate every registered agent; for each `StateAgentsyncOwned` dir,
  run the same default-`HEAD~1` revert; skip (with a faint note) dirs that are
  untracked/foreign or have only one checkpoint. Aggregate a summary; print the
  out-of-sync notice once, listing each reverted dir. `--to` is rejected with
  `--all` (a ref is per-repo and meaningless across dirs) — error clearly.
- [ ] `--dry-run --all` previews each.
- [ ] Test: two managed agent dirs + one foreign → `--all` reverts the two, skips
  the foreign, notice lists both.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/cli/ -run RevertAll -count=1` → `ok`

**Commit:** `feat(cli): agentsync revert --all across every managed destination dir`

---

# Milestone 4 — doctor status + docs (same PR — `CLAUDE.md` doc-sync rule)

## Task 4.1 — `agentsync doctor` "Destination git backup" section

**Files**
- modify `internal/cli/doctor.go`
- modify the doctor test (the existing `internal/cli` doctor test pins substrings).

**Steps**
- [ ] After the "Plugins" section, add:
  ```go
  fmt.Fprintln(p.Out, "")
  p.Section("Destination git backup")
  checkDestinationGitBackup(p, home)
  ```
- [ ] `checkDestinationGitBackup(p, home)`: load config (`source.Load`); print the
  effective mode (`okCheck`/`warnCheck` with label `mode       `). Then for each
  registered agent with a `VersionedHome` dir that exists on disk, print one line:
  `agentsync-versioned` (✓), `foreign source control` (ℹ), or `untracked` (ℹ),
  using `agit.Detect`. Informational only — never increments `fails`.
- [ ] Update the doctor test's expected substrings (it asserts contiguous
  `<label><status>`); add a case asserting the new section header + the mode line.

**Test:** `AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/cli/ -run Doctor -count=1` → `ok`

**Commit:** `feat(cli): doctor surfaces destination-git-backup status (read-only)`

## Task 4.2 — docs + changelog + scaffold

**Files**
- modify `docs/user-guide.md` — command reference: add `revert`, the `apply
  --no-git-backup` flag, the new `doctor` line, the
  `[destination_directory_git_backup]` table in the `agentsync.toml` layout block,
  and a short "destination versioning" concept blurb.
- modify `README.md` — quick-reference table: add `revert`; one line noting
  local-only auto-versioning.
- modify `website/src/content/docs/reference/cli.mdx` — "At a glance" table row +
  a `### revert` section + the `apply --no-git-backup` flag.
- modify `docs/concepts.md` — note destination dirs may carry a local-only git
  history, distinct from `.state/`.
- modify `docs/architecture.md` — where the checkpoint step sits in the apply tail;
  the never-push invariant; `.state/` untouched.
- modify `internal/cli/init.go` — add a commented
  `[destination_directory_git_backup]` block to `initialAgentsyncTOML` (mirrors the
  commented `[secrets]` block style).
- modify `CHANGELOG.md` — under `[Unreleased] / Added`:
  - `agentsync revert` — roll a destination agent dir back to a prior apply checkpoint.
  - Destination dirs are auto-versioned with a local-only git repo (opt-out;
    `[destination_directory_git_backup]`); `apply --no-git-backup` bypasses it.

**Steps**
- [ ] Make each edit; keep the matrices/contract pages untouched (this is not a
  per-agent component capability — the capability matrix does NOT change).
- [ ] Verify the website doc build still passes if the repo builds it
  (`website/` — only the authored pages above; the generated contract pages are
  untouched).

**Test:** `go build ./... && AGENTSYNC_TEST_IN_CONTAINER=1 just test-fast`; then the
container lint:
`GOTOOLCHAIN=go1.26.2 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...`
→ clean. Re-stage any files `just lint` rewrote.

**Commit:** `docs: document destination git backup + agentsync revert`

---

## Spec-coverage map (every requirement → task)

| Spec requirement | Task(s) |
|---|---|
| Per-destination repo, user scope | 1.3, 2.2 |
| Opt-out prompt + sticky "don't ask again" | 2.3, 1.4 |
| `mode = prompt/on/off` config table | 1.1, 1.2 |
| `--no-git-backup` CI bypass | 2.1, 2.2 |
| Commit each writing apply; no-op when in sync | 2.2, 2.4 |
| Whole-file commit scope; no unrelated files | 2.2 |
| Stray `$HOME` files excluded (gap) | 1.3 (Claude HomeDir=`~/.claude`) |
| Respect existing source control | 0.1 (Detect), 2.2 |
| `VersionedHome` git-root mechanism | 1.3 |
| Dedicated commit identity (overridable) | 0.3, 1.1, 2.2 |
| Auto-created marker + notice file | 0.2 |
| `.git` 0o700 / files 0o600 | 0.2 |
| Never push (no remote API) | M0 (no surface), 0.6 guard |
| `agentsync revert <agent>` default last | 3.1, 3.2 |
| Append-only restore | 0.5, 3.2 |
| `--to`, `--all`, `--dry-run` | 3.1, 3.2, 3.3 |
| Out-of-sync notice | 3.2, 3.3 |
| `doctor` status (no `git` command) | 4.1 |
| Docs in same PR | 4.2 |
| `.state/` untouched | (no task modifies it — verified by absence) |

## Risks / watch-items for the implementer

- **go-git `wt.Add` of a deletion:** confirm v5.18.0 stages a removed file via
  `wt.Add(rel)`; if not, use `wt.Remove(rel)` for the delete branch in `Commit`.
  Task 0.3's test must include a deletion to catch this.
- **Real FS required:** go-git won't operate on `afero.MemMapFs`; all M0/M2/M3
  tests use `t.TempDir()` + `testenv.RequireContainer`.
- **Generic adapter dir source:** verify `generic.Spec` truly exposes a single
  config dir before asserting `HomeDir`; if a Spec lacks one, return `("",false)`
  and let the registry guard (1.3) document the abstention rather than inventing a
  path (per the "cross-reference upstream harness docs" rule).
- **`apply` integration fixtures:** reuse the existing `internal/cli` apply test
  harness rather than hand-rolling a canonical; match its scope/target-root env
  setup so the new dirs resolve under the temp root.
