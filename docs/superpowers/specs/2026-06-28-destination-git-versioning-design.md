# Destination auto-git versioning + `agentsync revert` — Design Spec

**Date:** 2026-06-28
**Status:** Approved in first review ([PR #119](https://github.com/spxrogers/agentsync/pull/119)). Implementation plan: `docs/superpowers/plans/2026-06-28-destination-git-versioning.md`.
**Issue:** [#118 — Auto-version the destination agent dirs with git so a bad `apply` is revertible](https://github.com/spxrogers/agentsync/issues/118)

---

## Summary

`agentsync apply` renders the canonical `~/.agentsync/` config into each agent's
native config dir (`~/.claude`, `~/.codex`, `~/.cursor`, …), overwriting real
files those tools read. Today the only destination-side safety net is the narrow,
ephemeral, foreign-collision-only backup under the gitignored
`~/.agentsync/.state/backups/<ts>/` (capped at the 20 most recent), and there is
no undo path at all.

This feature gives each destination agent dir its **own local-only git repo** so
that every `apply` leaves a clean, browsable checkpoint, and adds a first-class
`agentsync revert` command to roll a destination back to a prior checkpoint. The
repos are **never pushed to a remote** — they exist solely for local rollback —
which is what makes it acceptable for them to hold the cleartext secrets the
rendered files already contain.

Two user-visible surfaces ship together:

1. **Auto-versioning** — on the first write to an untracked destination dir,
   prompt (opt-out) to `git init` it; after each successful apply that changed
   managed files there, `git add` + commit a checkpoint.
2. **`agentsync revert <agent>`** — restore a destination dir to a prior
   checkpoint (last by default), append-only, then print an out-of-sync notice
   telling the user to reconcile before the next apply.

A new global `[destination_directory_git_backup]` table in `agentsync.toml` holds
the mode (`prompt` / `on` / `off`) and optional commit-identity overrides;
`apply --no-git-backup` bypasses everything for CI/scripting.

---

## Goals / Non-goals

**Goals**

- A durable, browsable, revertible **local** history of agentsync's writes into
  each destination agent dir, scoped per agent.
- A clean per-apply checkpoint commit — only when the apply actually changed
  managed files (apply is a no-op when in sync, and so is the commit step).
- A first-class `agentsync revert` that doesn't make users hand-drive git, and
  that is honest about leaving the destination out of sync with canonical.
- Zero new third-party dependencies — reuse the already-vendored
  `github.com/go-git/go-git/v5` (`v5.18.0`, used today in `internal/cli/init.go`).
- Keep `~/.agentsync/.state/` (hashes + foreign-collision backups + its pruning)
  exactly as-is; this complements it, it does not replace it.

**Non-goals**

- Pushing any destination repo to a remote (local-only by design — see Secrets).
- Versioning the canonical `~/.agentsync/` source dir (users already keep that in
  their own dotfiles repo, with secret *references* only).
- Snapshotting stray `$HOME`-level managed files (e.g. `~/.claude.json`) into the
  git net — see "Commit scope & git root" for the decided boundary and gap.
- Sub-file / per-JSON-pointer staging of shared files — shared files are committed
  whole (KISS, per the issue).

---

## Locked decisions

These are settled — from the issue's "Decisions" section plus the four design
questions resolved up front for this spec.

| # | Decision | Source |
|---|---|---|
| 1 | **Per-destination repo, not an umbrella repo.** Each agent dir gets its own independent repo (`~/.claude/.git`, `~/.codex/.git`, …). | Issue |
| 2 | **Opt-out** with a CLI bypass and a sticky "don't ask again." Default = prompt on first untracked write; CI/scripting flag skips it; a remembered decline is persisted in `agentsync.toml`. | Issue |
| 3 | **Commit scope = agentsync-managed changes; KISS on shared files.** For a wholly-owned file, just that file; for a shared file (e.g. `settings.json`) where agentsync owns only some keys, `git add` + commit the whole file as written — no per-pointer slicing. Don't sweep in entirely-unrelated files agentsync never touched. | Issue |
| 4 | **Ship a first-class `agentsync revert`** that rolls a destination back to a prior apply checkpoint and prints an out-of-sync reconcile notice on completion. | Issue |
| 5 | **Respect existing source control.** If a destination dir is already a git work tree (or nested in one — e.g. `~/.claude` kept in dotfiles), do **not** init or auto-commit; at most surface a hint. | Issue |
| 6 | **Local-only, never pushed.** Never `git remote add`, never `git push` from agentsync for these repos. Cleartext secrets in this local history are an accepted tradeoff. | Issue |
| 7 | **Leave `.state/` (incl. `.state/backups/` and its pruning) exactly as-is.** | Issue |
| 8 | **Revert is append-only.** `revert` re-materializes files from the chosen checkpoint and records a **new** commit; history is never rewritten, so the bad apply stays auditable and the revert is itself revertible. | Q (this spec) |
| 9 | **`agentsync revert <agent>` defaults to the last checkpoint**, with `--to <ref>`, `--all`, and `--dry-run`. | Q (this spec) |
| 10 | **Git root = the agent's own config dir only.** Stray `$HOME`-level managed files (e.g. `~/.claude.json`) stay out of git (we never init a repo at `$HOME`) and keep relying on `.state/backups`. Documented as a known gap. | Q (this spec) |
| 11 | **Dedicated agentsync commit identity** (`agentsync <agentsync@localhost>`), overridable via `[destination_directory_git_backup]`. Works even when no global git identity is set. | Q (this spec) |
| 12 | **Git root via the optional `VersionedHome` adapter extension** — adapters declare their versionable config dir; `noop` abstains. | Review #119 |
| 13 | **CI/scripting bypass flag = `apply --no-git-backup`.** | Review #119 |
| 14 | **Config table = `[destination_directory_git_backup]`, `mode = "prompt" \| "on" \| "off"`** (default `prompt`). | Review #119 |
| 15 | **No `agentsync git` command — config-edit only; `agentsync doctor` surfaces status read-only.** | Review #119 |
| 16 | **Auto-created marker = `AGENTSYNC_LOCAL_HISTORY.md` at repo root + `[agentsync] managed = true` in the repo's `.git/config`.** | Review #119 |
| 17 | **`revert` default = undo the most recent apply** (restore `HEAD~1` content, commit append-only). | Review #119 |

---

## Architecture

The feature lives almost entirely in the apply tail and a new `revert` command,
plus a thin git helper. It does **not** touch the render/classify/write core, the
drift classifier, the secret invariants, or `.state/`.

```
agentsync apply  ──► render.Apply(...) ──► written map[string]bool (abs dest paths)
                                              │
                          (existing pipeline) │  state saved, backups pruned
                                              ▼
                                   ┌────────────────────────┐
                                   │  NEW: destination git   │   internal/git (new)
                                   │  checkpoint step        │   + apply.go hook
                                   └────────────────────────┘
                                              │
              per agent that wrote files this run:
              1. resolve agent's git root dir (user scope)
              2. is it already a work tree?  ── yes ─► skip (decision #5), hint
              3. untracked + mode=prompt + TTY ─► prompt to init (opt-out)
              4. mode=on / accepted ─► git init (first time) + add changed
                 managed files under root + commit checkpoint
```

```
agentsync revert <agent> [--to <ref>] [--all] [--dry-run]
   └─► open agent's git root repo ─► resolve target checkpoint (default: prev)
        └─► restore worktree files from that checkpoint ─► commit "revert to <ref>"
             └─► print: destination now OUT OF SYNC with canonical; reconcile first
```

### Where it hooks in (concrete anchors)

- **Apply tail** — `internal/cli/apply.go`, after the successful state save
  (`state.Save(statePath, s)`, ~line 217) and backup prune
  (`render.PruneBackups(home, render.DefaultBackupKeep)`, ~line 222), before the
  final success message. The managed-file set is the `written map[string]bool`
  returned by `render.Apply` (`internal/render/pipeline.go`, `applyPlan` returns
  `reports, written, unchanged, err`). All paths in `written` are absolute. The
  checkpoint step is **skipped entirely** in `--dry-run` (no writes happen) and is
  a no-op when `written` is empty (apply changed nothing).
- **New command** — `internal/cli/revert.go`, registered in
  `internal/cli/root.go`'s `NewRoot()` `cmd.AddCommand(...)` list, structured like
  the existing simple commands (`newDoctorCmd` / `newVerifyCmd`).
- **New helper package** — `internal/git/git.go`, a thin wrapper over go-git:
  `IsWorkTree(dir) bool`, `Init(dir) error`, `Open(dir) (*Repo, error)`,
  `AddAndCommit(repo, paths, msg, author) (commitHash, error)`,
  `RestoreToCheckpoint(repo, ref) error`, `Log(repo, n) ([]Checkpoint, error)`,
  `IsClean(repo) (bool, error)`. Today go-git usage is a single inline
  `git.PlainClone` in `init.go`; this consolidates the new surface in one place
  with one author-signature path. (Note: go-git's `Worktree.Commit` requires an
  `object.Signature`; the dedicated identity lives here, decision #11.)

### Commit scope & git root

The unit of versioning is **the agent's own config directory at user scope**.

- **Git root resolution — `VersionedHome` adapter extension (decided, PR #119).**
  Each adapter already resolves its own destination paths via a per-adapter
  `ResolvePaths` returning an adapter-specific `Paths` struct (e.g. Cursor's
  `Paths.ConfigDir = ~/.cursor`). There is no shared accessor on the `Adapter`
  interface today, so we add a small **optional extension interface** (mirroring
  the existing `PluginIngester` / `WarnEmitter` idiom) so an adapter declares its
  versionable root:

  ```go
  // VersionedHome is an OPTIONAL Adapter extension: an adapter that writes into a
  // single coherent on-disk config dir declares it so the apply tail can git-init
  // and checkpoint that dir. Adapters that resolve no single root (noop) don't
  // implement it and are skipped.
  type VersionedHome interface {
      // HomeDir returns the absolute config-dir root to version for this scope,
      // or ("", false) if there is none (e.g. project scope, or no coherent root).
      HomeDir(scope Scope, project string) (string, bool)
  }
  ```

  All nine deep adapters and the data-driven `generic` tier implement it (the
  generic adapter reads the root from its `generic.Spec`); `noop` does not. This
  keeps the core `Adapter` contract unchanged and lets edge adapters abstain — the
  same pattern the codebase already uses for optional capabilities.

  *Alternative considered and rejected:* derive the root from the `written` paths
  or keep a central agent→dir table in `internal/git`. Deriving "the config dir"
  from a flat path set is ambiguous (`~/.claude` vs `~/.claude/skills`), and a
  central table duplicates path knowledge the adapters already own.

- **Which files get staged.** Only managed files written under that root this run
  — i.e. `written ∩ {files under HomeDir}`. For shared files (e.g.
  `~/.claude/settings.json`) the **whole file** is staged as written (decision #3),
  co-located user keys included. We do not `git add -A` the whole tree, so
  unrelated files the user dropped in the agent dir aren't swept in. (A `.git` that
  already tracks more is the user's existing-repo case → decision #5, we don't
  touch it.)

- **Stray `$HOME`-level files are out of scope (decided gap).** Claude writes
  `~/.claude/` *and* `~/.claude.json` directly in `$HOME`. We will **not** init a
  repo at `$HOME`. So `~/.claude.json` (and any other top-level managed file) is
  **not** captured in the git history; it continues to rely on the existing
  `.state/backups` foreign-collision backup exactly as today. This is documented as
  a known limitation in the user guide and the `revert` help text.

- **Scope.** This targets **user-scope** destinations. Project-scope rendering
  writes into the project tree (e.g. `<repo>/.cursor/…`), which is virtually
  always already a git work tree → decision #5 makes it a natural no-op. The
  checkpoint step only runs for user scope; `agentsync revert` operates on
  user-scope agent dirs.

### Detecting an existing work tree (decision #5)

Use go-git `PlainOpenWithOptions(dir, &git.PlainOpenOptions{DetectDotGit: true})`
so a destination **nested inside** an existing repo (dotfiles user) is detected,
not just a `.git` exactly at the root. If detected and the repo is **not** one
agentsync itself created (see "marker" below), agentsync neither inits nor
auto-commits; it may print a one-line hint that auto-versioning is disabled
because the dir is already under source control.

**Repo marker (decision #16).** To tell "a repo agentsync auto-created" from "the
user's own repo," the auto-init writes a `[agentsync] managed = true` key into the
repo's own `.git/config`, alongside the generated `AGENTSYNC_LOCAL_HISTORY.md`
notice file (below). Auto-commit and `agentsync revert` only operate on repos
carrying this marker. A user's pre-existing repo never gets the marker, so
agentsync stays out of it.

### Auto-init repo contents

On first init, agentsync writes a generated notice file at the repo root (e.g.
`AGENTSYNC_LOCAL_HISTORY.md`) making clear:

- this is a **local-only** agentsync rollback history,
- it **may contain cleartext secrets** and **must never be pushed**,
- how to roll back (`agentsync revert <agent>`).

The notice is itself committed in the initial checkpoint. Repo privacy: rendered
files are already `0o600` (`internal/iox/atomic.go`); the `.git` dir is created
`0o700` so the history is user-only.

---

## Config schema — the `[destination_directory_git_backup]` table

New global table in `~/.agentsync/agentsync.toml`, parsed into a
`DestinationGitBackupConfig` struct added to `source.Config`
(`internal/source/schema.go`, alongside `Agents`, `Updates`, `Secrets`,
`Memory`). The table name is deliberately explicit — these repos are a backup of
the *destination directories*, not general "git" config. Project overlay merges
like the others (`internal/project`), though it is a user-scope concern in
practice.

```toml
[destination_directory_git_backup]
# Behavior for git-backing the rendered destination dirs. Tri-state:
#   "prompt" (default) — on first write to an untracked agent dir, ask to init.
#   "on"               — init + checkpoint silently (set after the user says yes).
#   "off"              — never init/commit/prompt (set by "don't ask again").
mode = "prompt"

# Optional commit-identity overrides (defaults shown).
author_name  = "agentsync"
author_email = "agentsync@localhost"
```

```go
// internal/source/schema.go
type DestinationGitBackupConfig struct {
    Mode        string `toml:"mode,omitempty"`         // "prompt" (default) | "on" | "off"
    AuthorName  string `toml:"author_name,omitempty"`
    AuthorEmail string `toml:"author_email,omitempty"`
}
// added to Config as:
//   DestinationGitBackup DestinationGitBackupConfig `toml:"destination_directory_git_backup"`
```

State transitions:

| Event | Result |
|---|---|
| Table absent / `mode` unset | Treated as `"prompt"`. |
| User answers **yes** at the init prompt | Persist `mode = "on"`. |
| User answers **no** | Skip this run; leave `mode = "prompt"` (will ask again). |
| User answers **no + don't ask again** | Persist `mode = "off"`. |
| `apply --no-git-backup` (per run) | Force-skip init/commit for that invocation, regardless of `mode`. Never mutates config. |
| Re-enable later | User edits `agentsync.toml` (`mode = "prompt"`/`"on"`). |

Writing the persisted `mode` back into `agentsync.toml` goes through the existing
canonical TOML writer (comment- and order-preserving), the same path
`agentsync mcp add` etc. use — it must not clobber the user's hand-edits. The
current mode is also surfaced read-only by `agentsync doctor` (see CLI surface).

---

## CLI surface

### `agentsync apply` — one new flag

```
--no-git-backup   Skip destination git init/checkpoint for this run (CI/scripting).
                  Does not modify agentsync.toml.
```

Everything else about apply is unchanged. In `--no-git-backup` or non-interactive
(`--no-input` / no TTY) runs, agentsync never prompts; with `mode = "on"` it
still checkpoints silently (no prompt needed), with `mode = "prompt"` and no TTY
it skips init (can't ask) and prints a one-line hint.

### `agentsync doctor` — surface status (no new command)

There is **no** dedicated `agentsync git`/enable/disable command (kept config-only
given the triviality — decided, PR #119). Instead, `agentsync doctor`
(`internal/cli/doctor.go`) gains a small read-only check reporting the
destination-git-backup mode and, per managed agent dir, whether it is
agentsync-versioned, already under foreign source control, or untracked. Changing
the behavior is a one-line `agentsync.toml` edit.

### `agentsync revert` — new command

```
agentsync revert <agent> [flags]

  Restore an agent's destination dir to a prior apply checkpoint. Append-only:
  records a new commit; never rewrites history.

Flags:
  --to <ref>     Checkpoint to restore (commit hash / relative like HEAD~2).
                 Default: the previous checkpoint (undo the most recent apply).
  --all          Revert every agentsync-managed destination dir to its last
                 checkpoint. Mutually exclusive with a positional <agent>.
  --dry-run      Show what would change (files + target commit) without writing.
  --scope        user (default). Project dirs are left to their own repo.
```

Behavior:

1. Resolve the agent's git root (user scope). Error clearly if the dir isn't an
   agentsync-managed repo (no marker / never inited).
2. Resolve the target checkpoint (`--to` or the previous checkpoint by default).
3. Restore the worktree files to that checkpoint's content and record a new commit
   `agentsync revert: <agent> → <short-ref>`. Append-only (decision #8).
4. Print the **out-of-sync notice** (decision #4):

   > `agentsync revert` completed. The destination directory `<dir>` is now out
   > of sync with the agentsync configuration. Please reconcile as needed (e.g.
   > `agentsync reconcile` / `agentsync import`) before your next `agentsync
   > apply` to avoid re-losing these changes.

`--dry-run` prints the target commit and the file diff summary and exits without
committing.

### Commit message format

Per-apply checkpoint:

```
agentsync apply: <agent> (<scope>) — <n> file(s)

<rel/path/under/root/1>
<rel/path/under/root/2>
…
```

Revert checkpoint:

```
agentsync revert: <agent> → <short-target-ref>
```

Both authored by the dedicated identity (decision #11).

---

## Secrets — local-only guardrails (unchanged invariants)

The rendered destination files contain secrets resolved to **cleartext** at apply
time (the canonical source holds `${secret:…}`/`${env:…}` references; that is
unchanged). The destination git repos therefore hold cleartext secrets in history.
This is the deliberately accepted tradeoff from the issue, made safe by keeping the
repos **local-only**:

- **Never `git remote add`, never `git push`** from agentsync for these repos. The
  `internal/git` helper exposes no push/remote surface at all — there is no code
  path that could add a remote, so this can't regress by accident.
- The generated repo notice warns the history is local-only and may contain
  secrets and must never be pushed.
- `.git` dir is `0o700`; rendered files remain `0o600`.

This **does not touch** the agentsync secret invariants: no `secrets.Resolved` is
written back to canonical, `walkSecretFields` is unchanged, `capture.Capture`'s
fail-closed backstop is unchanged. The git step only commits files that already
exist on disk in the destination — it never round-trips through the canonical
source.

---

## Edge cases

- **Apply changed nothing** → `written` empty for that agent → no commit (clean
  history, no empty checkpoints).
- **Dir already a work tree / nested in one** → no init, no auto-commit
  (decision #5), one-line hint.
- **No TTY and `mode = "prompt"`** → can't ask → skip init, hint; never block.
- **`mode = "off"` or `--no-git-backup`** → skip entirely.
- **Multiple agents in one apply** → one checkpoint commit per agent repo (the
  per-agent grouping is natural: each agent has its own root + repo).
- **`revert` on an agent that was never inited** → clear error, suggest running an
  apply with auto-versioning enabled first.
- **`revert --to` an unknown ref** → clear error, no changes.
- **go-git unavailable feature / commit with no identity** → the dedicated identity
  removes the common "no git identity configured" failure; surface any go-git error
  wrapped (`fmt.Errorf("committing checkpoint for %s: %w", agent, err)`).
- **Stray `$HOME` file changed but agent dir unchanged** → no checkpoint (gap is
  documented; `.state/backups` still covers the foreign-collision case).

---

## Docs to update (same change, per `CLAUDE.md`)

A new command + a new `agentsync.toml` table is a CLI-surface + schema change, so
the same PR must update:

- `docs/user-guide.md` — command reference (`apply --no-git-backup`, new `revert`
  section, the new `doctor` status line), the `agentsync.toml` layout block
  (`[destination_directory_git_backup]` table), and the destination-versioning
  concept.
- `README.md` — quickstart / quick-reference table (add `revert`; note
  auto-versioning).
- `website/src/content/docs/reference/cli.mdx` — "At a glance" table row + a
  `### revert` section + the `apply --no-git-backup` flag.
- `docs/concepts.md` — the three-state model: note destination dirs may now carry
  a local-only git history (distinct from `.state/`).
- `docs/architecture.md` — where the checkpoint step sits in the apply tail; the
  never-push invariant; that `.state/` is untouched.
- `internal/cli/init.go` `initialAgentsyncTOML` constant — add a commented
  `[destination_directory_git_backup]` example so `agentsync init` scaffolds it.
- `CHANGELOG.md` — `[Unreleased] / Added`.

The capability matrix is **not** affected (this isn't a per-agent component
capability).

---

## Resolved in review (PR #119)

All previously-open decisions were settled in the first spec review — folded into
the body above and the locked-decisions table (rows 12–17):

1. **Git-root mechanism** → the optional `VersionedHome` adapter extension. ✅
2. **CI bypass flag name** → `apply --no-git-backup`. ✅
3. **Config table + mode values** → `[destination_directory_git_backup]` with
   `mode = "prompt" | "on" | "off"` (the opposite of `off` is `on`). ✅
4. **Re-enable ergonomics** → config-edit only; **no** `agentsync git` command;
   `agentsync doctor` surfaces the current status read-only. ✅
5. **Notice file + marker** → `AGENTSYNC_LOCAL_HISTORY.md` at the repo root plus a
   `[agentsync] managed = true` key in the repo's own `.git/config` as the
   "agentsync-created" marker. ✅
6. **`revert` default** → undo the most recent apply, i.e. restore the checkpoint
   *before* `HEAD` (`HEAD~1` content) and commit it append-only. ✅

---

## Test plan / success criteria

All FS-touching tests run in-container (`testenv.RequireContainer`) on
`afero`/`t.TempDir`, stdlib `testing`, table-driven.

- **Helper unit tests** (`internal/git`): init creates a `.git` (0o700) with the
  notice committed; add+commit stages only the given paths and authors with the
  dedicated identity; `IsWorkTree` detects both a `.git` at root and a nested one;
  restore-to-checkpoint reproduces prior bytes; `IsClean` correct.
- **Apply-tail integration** (`internal/cli`): first apply to an untracked dir with
  `mode = "on"` produces exactly one checkpoint containing the written managed
  files and **not** unrelated files; a no-op apply produces no new commit; a dir
  that's already a work tree is left untouched (no agentsync commits added);
  `--no-git-backup` and `mode = "off"` skip; stray `~/.claude.json` is absent from the
  claude repo history.
- **Prompt behavior**: TTY + `mode="prompt"` → prompts; "no" leaves `mode=prompt`;
  "no + don't ask again" persists `mode=off`; non-interactive never blocks.
- **`revert`**: default reverts the last apply (files match the prior checkpoint),
  records a new append-only commit, prints the out-of-sync notice; `--to`,
  `--all`, `--dry-run` behave; unknown ref / un-inited dir error clearly.
- **Never-push invariant**: a test (or the absence of any remote/push symbol in
  `internal/git`) asserts no remote is ever configured and no push API exists.
- **Secret invariants unchanged**: existing `internal/secrets` /
  `internal/capture` guards still pass; no `secrets.Resolved` reaches a source
  writer (this feature adds no dest→source path).
- **Config round-trip**: `[destination_directory_git_backup]` table parses;
  persisting `mode` preserves comments/key order in `agentsync.toml`.

---

## Out of scope (for now)

- Pushing any destination repo to a remote.
- Versioning the canonical `~/.agentsync/` source dir.
- Sub-file / per-pointer staging of shared files.
- Capturing stray `$HOME`-level managed files into git (documented gap).
- A scheduled/automatic prune of destination git history (git itself + the
  local-only scope make this low-priority; revisit if histories grow unwieldy).
