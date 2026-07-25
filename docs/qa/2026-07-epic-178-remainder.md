# QA remainder — epic #178, issue #164 (real-harness verification)

An honest record of what issue #164 — the epic's real-world QA umbrella —
demanded, what was actually delivered (in-repo, verifiable), and what remains
genuinely un-executed, so the v1.0 tag decision can weigh the gap explicitly
instead of inferring completeness from the issue's closed state.

## What #164 demanded

- Exercise **git-backup / revert** against edge-case repositories (packed/gc'd
  history, permission drift, nested foreign repos, mode-stripped archives).
- **Launch the seven untested agent harnesses** (windsurf, roo, cline, continue,
  gemini, cursor, codex — beyond the round-trip-tested ones) against
  agentsync-rendered configs and record per-agent F5 re-read verdicts.
- Measure the **wall-clock cost** of the `HasNestedRepoBelow` full-tree walk on
  large IDE directories.
- Attach a **written QA report** to the issue.

## What was delivered in-repo (verifiable at HEAD)

Regression fixtures substituting for the git-backup/revert edge cases, all
container-run in the standard suite (`internal/git`, `internal/cli`):

- `TestRestore_PreservesUntrackedFiles`, `TestRevert_PreservesUntrackedUserFiles`
  — the #128 untracked-survival contract.
- `TestApply_GitBackupBaselineRevertsFirstApply`,
  `TestApply_GitBackupBaselineCoversOrphanDeletes` — first-apply and
  delete-only-apply recoverability (the latter added in this remediation
  follow-up, closing the planned-deletes baseline gap).
- `TestRevertRootSkipsForeignNestedRepo`, `TestHasNestedRepoBelowFollowsSymlink`
  — nested/symlinked foreign-repo safety.
- `TestGitPermLifecycle` — the `.git` 0700 permission lifecycle.
- `TestRestore_PackedHistory` — delta-based restore across a fully repacked
  object store (zero loose objects; non-vacuity asserted), the packed/gc'd
  history case #164 named.
- `TestRevert_Errors`' ancestor-validation cases — `revert --to` now refuses a
  hash outside the checkpoint history (this remediation follow-up; previously a
  skipped test documenting unsafe behavior).
- `BenchmarkHasNestedRepoBelow` — the walk cost is now measured (~10ms over a
  2,000-leaf-dir tree at depth 4, in-container) and recorded on the function's
  doc comment. In-container numbers only; no measurement was taken on a real
  user machine against a real IDE directory.

## What remains genuinely un-executed

- **No live harness was ever launched.** The hermetic test container cannot run
  the agent applications, so no agent re-read an agentsync-rendered config in
  this epic; fidelity evidence is artifact-anchored round-trip tests only. The
  one open upstream question this left behind is tracked in code as
  `TODO(#164)` (cursor remote-`type` reject-vs-ignore, `internal/adapter/cursor/mcp.go`).
- **No F5 re-read verdicts** exist for the seven agents named by the issue.
- **Wall-clock cost on real IDE dirs** (e.g. a populated `~/.config/zed`) was
  not measured — only the synthetic in-container benchmark above.

Running the live-harness pass requires a workstation with the agents installed;
it was substituted, not skipped silently. If v1.0 ships without it, this file is
the record of that decision.
