# Changelog

All notable changes to agentsync are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Until the first stable `1.0.0` tag, the project is in **beta**: the canonical
source layout, CLI surface, and state schema are stabilizing but may still change.

## [Unreleased]

### Fixed

- **`apply --dry-run` now previews convergence-time removals, and the apply
  headline counts key-removals and file-deletes distinctly.** A delete-only run
  previewed as "Plan: 0 ops … 0 to write" and then surprised with a removal
  headline on the real apply; the dry-run now prints the same removal counts
  the real run will report. The headline also no longer conflates per-key
  removals and whole-file skill deletes under one "ops" number: it reads
  `removed: N key(s), M file(s)` (and `applied: X ops, removed: …` for a
  mixed run).
- **`plugin upgrade|enable|disable|remove` refuse a ref whose marketplace
  qualifier doesn't match the installed plugin.** The lifecycle subcommands
  accepted `id@marketplace` but silently discarded the qualifier, so
  `agentsync plugin disable demo@wrong-mp` acted on the `demo` installed from
  `test-mp`. A qualified ref is now checked against the marketplace recorded
  in `plugins/<id>.toml` and refused on mismatch, naming both; a bare id (or
  matching qualifier) works as before. A plugin installed by **bare id**
  records the internal `default` sentinel rather than a real marketplace, so a
  later qualified ref refuses with an honest "cannot be verified; use the bare
  id" instead of claiming the plugin came from a marketplace named `default`.
  Relatedly, a bare-id install (and `upgrade` of a bare-id-installed plugin)
  of a plugin with a *relative* source now fetches against the marketplace
  cache that actually supplied the entry — previously the fetch resolved
  against a nonexistent cache dir and failed.
- **Continue adapter: an explicitly-typed stdio MCP server that also carries a
  `url` now reports the dropped url.** The single-transport drop report fired
  only for the untyped command+url ambiguity; a server with `type: "stdio"`
  plus a `url` dropped the url with no report at all — the same silent loss
  the ambiguity Skip exists to prevent. Both cases now surface the unused url
  via the same reduced Skip.
- **Gemini adapter: a structural error while walking the commands tree now
  fails ingest loudly instead of being demoted to a warning.** A mid-walk
  failure (an unreadable subdirectory, a file sitting at the commands-dir
  path) used to warn and return success with a silently short `Commands`
  slice — exactly the state drift/capture can misread as "the user deleted
  those commands" (#159's loud-dir policy, which every other adapter follows).
  Such errors now abort the ingest; an absent commands dir stays a clean
  no-op, and a genuinely per-entry failure (one corrupt `.toml`) still warns
  and continues so one bad file never hides the rest. The redundant double
  listing of the commands dir (a pre-check list plus the walk's own list) is
  also gone.
- **Codex no longer reports a deliberately disabled or non-targeted MCP server
  as a "drop" on every apply.** The codex adapter alone emitted a `SkipDropped`
  in the translation report for every MCP server the user disabled
  (`enabled = false`) or excluded via the `agents` allowlist — recurring noise
  for a setting agentsync was obeying, not data it failed to represent. The
  other eight adapters honor the same condition silently; codex now matches
  the fleet. Skips for content codex genuinely cannot render are unchanged.
- **Windsurf adapter: stripping a hand-authored leading fence from a workflow
  now warns instead of deleting the bytes silently.** Windsurf workflows honor
  no frontmatter, so ingest strips a hand-authored leading `---`…`---` block
  before capturing the body — but unlike the rules path, the workflow path
  said nothing, silently discarding user-authored bytes with no canonical
  home. The strip behavior is unchanged; ingest now emits a warning naming
  the workflow, symmetric with the rules-path warning.
- **Continue adapter: colliding prompt command names are refused on ingest
  instead of silently clobbering.** Continue keys a slash command off the
  frontmatter `name`, so two prompt files (say `foo.md` and `baz.md`) can both
  resolve to the same effective command name — and since the canonical model
  and every downstream write-back/render key off `Command.Name`, capturing both
  let one silently vanish. Ingest now keeps the first file, skips the later
  one, and warns naming both files and the colliding name.

- **The apply-time baseline snapshot now carries the revert snapshot's
  cleartext caution, and skips roots the apply doesn't touch.** When the
  pre-apply baseline commits something — typically a hand-typed edit to a
  tracked dest file since the last apply, the same class of content the revert
  snapshot warns may hold freshly-typed secrets — `apply` now prints the same
  "kept in the local-only history … may contain secrets in cleartext" caution,
  once per run. And a version root with no planned write or delete under it is
  no longer baseline-snapshotted at all: previously an apply could quietly grow
  "pre-apply baseline" commits in dirs it never touched whenever they held
  uncommitted tracked drift; that drift now stays on disk, uncommitted, exactly
  as a `--no-git-backup` run would leave it.
- **`revert --to` now validates the target against the checkpoint history.**
  Any revision whose object existed in the backup repo was accepted — including
  a commit from an orphaned or rewound lineage that was never one of the dir's
  checkpoints — and restored with undefined semantics. `revert` (and its
  `--dry-run` preview) now refuses a `--to` that is not the current checkpoint
  or one of its ancestors, naming the hash and pointing at
  `git -C <dir> log --oneline` to list the real checkpoints.
- **`revert --dry-run` warns when it cannot enumerate untracked files instead
  of silently dropping the note.** The preview swallowed a git-status error, so
  the "N untracked file(s) in this dir are left untouched" note could vanish
  with no indication anything went wrong — implying the dir held none. The
  preview now prints a warning naming the error and continues.
- **`revert` refuses a dangling symlink at a path it would create instead of
  silently deleting it.** The structural pre-flight used `os.Stat`, which
  follows links, so a dangling untracked symlink at a create path looked like
  "nothing there"; the restore then removed the user's symlink and committed a
  managed file over it. The pre-flight now uses `os.Lstat`: a symlink at a
  create path — dangling or not — is refused all-or-nothing like the other
  conflict classes, and the symlink survives untouched.
- **`revert` now prunes the directories its delete pass empties.** Rolling back
  past the apply that created a nested file (e.g. a skill's
  `skills/deep/SKILL.md`) removed only the file and left `skills/deep/` and any
  emptied ancestors behind as stray empty directories — unlike `git reset
  --hard`. The restore now removes each deleted file's emptied parent chain,
  stopping at the repo root and keeping any directory that still holds entries
  (your own untracked file in the same dir keeps it — and its ancestors — alive).
- **A namespaced Gemini command no longer aborts bulk `import`.** Gemini
  namespaces commands by subdirectory (`commands/git/commit.toml` is
  `/git:commit`), and ingest captures the subdir path into the command name
  (`git/commit`) — which the flat canonical namespace rejects. One such native
  file used to fail the entire `import gemini` run (commands, hooks, memory —
  nothing imported). A bulk import now skips the namespaced command with a
  warning naming it (preserved on disk, not captured) and imports everything
  else; a named single-item import still fails loudly.
- **`verify` now checks secret-reference shape in online mode too.** The
  malformed-reference check (`${secret:}` empty key, missing colon, illegal
  characters) ran only under `AGENTSYNC_ALLOW_OFFLINE_VERIFY=1`, while the
  online resolver silently passed the malformed token through as literal text —
  so a green local `verify` ("all references resolve") could contradict a red
  offline CI verify on the same config. Both modes now reject malformed
  references; online verify additionally checks resolvability, as before.
- **`import` now retires a stale canonical hook file when the native event can
  no longer be captured, closing the second-order `/hooks/<event>` clobber.**
  A Claude hook event captured while it was a clean command hook and *later*
  enriched natively (a `timeout` field, a non-`command` handler) is refused by
  ingest — but its stale canonical `hooks/<event>.toml` kept the next `apply`
  owning the whole per-event array, rewriting the user's enriched native entry
  without those fields, and no agentsync flow could preserve the edit short of
  hand-deleting the canonical file. `import` (full-agent or `:hook`; a named
  `:hook:<event>` import retires only that event) now asks the adapter which
  events it *semantically* refused (`adapter.HookIngestGuard` /
  `RefusedHookEvents` — a structurally-malformed native shape warns but never
  deletes canonical config) and removes each one's stale canonical file with a
  notice. Canonical hooks are shared across agents, so the retirement disowns
  the event's state key for **every** agent at exactly that scope — each
  agent's native entry is left frozen as-is, and no orphan cleanup fires
  against it on the next apply.
- **The pre-apply git baseline now covers planned deletions, so a delete-only
  first apply is recoverable.** The baseline staged only the paths the plan
  would *write*; a file the apply was about to *delete* (a skill dropped from
  source) was removed without a writer backup and its bytes landed in no
  baseline, no checkpoint, and no backup dir — unrecoverable. The baseline now
  stages the pre-apply content of every planned write *and* delete (including
  writer-derived skill orphan deletes), and on an already-owned root it also
  stages a planned path that is still untracked (created while backup was off
  or declined) before the apply overwrites or deletes it. Other untracked files
  remain untouched, as before.
- **Numeric passthrough values no longer flip to strings through the canonical
  source.** Adapter ingests decode native JSON/JSONC with `UseNumber` so large
  integers survive exactly, but a `json.Number` persisted into an MCP/LSP
  `extra` map marshaled as a TOML *string* (`timeout = '30'`), so an
  `import`→`apply` cycle silently rewrote every unmodeled numeric native field
  as a string (`"timeout": "30"`). The capture funnel now normalizes
  `json.Number` to `int64`/`float64` before writing the canonical source,
  keeping >2^53 integers exact and the native type intact.
- **Gemini adapter: hook group shape, empty `type`, and always-fire matchers
  (issue #166).** Rendering `.gemini/settings.json` `hooks` now reconstructs the
  native multi-handler shape from the flat canonical model — consecutive hooks
  sharing an (event, matcher) coalesce into one group with a multi-element `hooks`
  array, so an `import`→`apply` round-trip no longer explodes a hand-authored
  multi-handler group into N single-handler groups. An empty hook `type` is omitted
  instead of written as `"type":""`, and a matcher set on an always-fire Gemini
  event (`BeforeAgent`/`AfterAgent`/`SessionStart`/`SessionEnd`/`PreCompress`/
  `Notification` — only `BeforeTool`/`AfterTool` accept one) is dropped with a
  reported `SkipReduced` rather than emitted where Gemini ignores it.
- **Gemini adapter: namespaced subdirectory commands (issue #166).** A native
  `.gemini/commands/git/commit.toml` (Gemini `/git:commit`) now round-trips:
  ingest walks the commands tree recursively and encodes the subdirectory into
  `Command.Name` as a forward-slash relpath (`git/commit`), and render projects it
  back into the subdirectory layout. The guarantee is scoped to the adapter
  native→native round-trip — the flat canonical source cannot carry a `/`-bearing
  name — and the namespace is never silently truncated.
- **Gemini adapter: `$`-containing resolved secret values flagged, not silently
  corrupted (issue #166, theme C5).** Gemini expands `$VAR`/`${VAR}`/`${VAR:-default}`
  in every `settings.json` string (env, headers, and `url`/`httpUrl`) with no escape
  for a literal `$` (upstream `envVarResolver.ts`). agentsync now writes such a
  resolved value verbatim (a fabricated escape would itself corrupt it) and reports a
  `SkipReduced` naming the affected env/headers/url value, surfacing the data-dependent
  read-time corruption risk. The canonical source is unaffected — it still holds the
  `${secret:…}` reference — so this is destination corruption, not a cleartext leak.

- **Ingest now distinguishes an unreadable or corrupt native config file from an
  absent one, and fails loudly instead of silently treating it as a cleared
  component (issue #159).** Every deep adapter's `Ingest` (and the generic breadth
  tier) previously read native files with `if data, err := os.ReadFile(…); err ==
  nil { … }`, which swallowed *all* read errors — a permission error, an `EISDIR`,
  transient I/O, or a corrupt `settings.json` read identically to "file absent" and
  yielded an empty component. `status`/`diff` could then classify that empty
  component as "the user cleared their memory/commands/hooks," and a subsequent
  `reconcile`/`import` could write nothing back over the canonical
  `~/.agentsync/` source. Ingest now treats only `os.IsNotExist` as "component
  absent" (still a silent skip) and surfaces every other read/parse error, via the
  new shared `adapter.ReadFileOptional` / `adapter.ReadDirOptional` helpers. This
  also removes Claude's internal asymmetry where a corrupt `settings.json` failed
  loudly for MCP but silently for **hooks**. Read-side only: no change to the
  `Adapter`/`Ingest` signatures, the canonical schema, or any render/write-back
  path.
- **Codex adapter no longer renders a non-command hook handler silently
  (issue #151).** A Codex hook handler whose `type` is set but not `command` now
  surfaces a reduced Skip telling the user Codex will parse but never execute it
  (the handler still renders — Codex tolerates any type, so nothing that would
  make it reject `config.toml` was added). Also removed a dead hand-set
  `OwnedKeys` loop in `renderHooks` (the render pipeline populates `OwnedKeys`
  from persisted state, always overwriting the adapter-set value); hook
  orphan-cleanup is now covered directly against `MergeTOML`. (An earlier
  revision of this fix also reported disabled/non-targeted MCP servers as
  drops; that report was removed again before release — see the entry above.)
- **Continue & Cursor MCP round-trip and off-spec-key fixes (issue #172).** Three
  behavior changes: (1) **Cursor** no longer writes a `type` key on **remote**
  (`url`-bearing) MCP servers — Cursor's documented remote schema is `url`+`headers`
  with no `type`, so the key was off-spec; stdio servers still carry `type`, and
  ingest stays tolerant of a `type` key on read (a remote server's transport label
  now normalizes away on capture, as Cursor infers "remote" from the `url`). The
  reject-vs-ignore verdict for an unknown remote `type` is tracked upstream in #164.
  (2) **Continue** now round-trips a block's `name`/`version`/`schema` header:
  a hand-authored non-default `version`/`schema` is preserved instead of being
  silently regenerated to the `0.0.1`/`v1` defaults on the next apply. (3)
  **Continue** no longer silently drops the `url` of a server that carries both a
  `command` and a `url` with no explicit `type`; it renders as stdio (command wins)
  and reports the dropped `url` via a translation-report `Skip`.
- **Windsurf ingest can no longer clobber a project rule with the global-rules
  read, and its documented limits/canonicalization are now written down
  (issue #169).** The two memory reads in the Windsurf `Ingest` (project workspace
  rule + user global rules) were independent `if` blocks that both assigned
  `Memory.Body`; they are now a single guarded read (`readMemoryBody`) that reads
  the project rule when present and the global file ONLY otherwise, so a future
  path-resolution change can never let the global read overwrite a populated
  project body (the two sources are mutually exclusive per scope). No behavior
  change today. Alongside, three previously-undocumented facts are now recorded in
  code comments **and** the capability matrix: the MCP ingest canonicalizes a
  native `url` key to `serverUrl` on re-render (benign — Windsurf/Devin accepts
  both); workspace rules and workflows carry a documented **12,000-character**
  per-file limit (distinct from the 6,000-char global-rules limit) that agentsync
  leaves to Windsurf to enforce; and a near-`RulesDir` note tracks the upstream
  Windsurf → Devin Desktop rebrand (`.devin/rules/*.md` preferred, `.windsurf/rules/*.md`
  the still-honored legacy fallback agentsync targets). New artifact-anchored tests
  cover the ingest guard, the whole-file overwrite + foreign-collision backup of a
  hand-authored `global_rules.md`/workflow, a memory body that itself begins with a
  user-authored `---`/frontmatter block surviving round-trip byte-for-byte, and the
  `url`→`serverUrl` canonicalization. All figures verified against `docs.devin.ai`.
- **Adapter transport-normalization, Detect, and precision cleanups (issue #175).**
  A batch of low/nit adapter fixes plus the cross-cutting C5 matrix harmonization:
  (1) **Roo** untyped-url MCP servers now round-trip to a *stable* canonical type —
  a `type = ""` server with a url and no command normalizes to `http` (rendered
  `streamable-http`) and stays stable, and a hand-authored native `url`-only entry
  canonicalizes the same way instead of being misread as stdio and losing its url;
  (2) **Roo** `Detect()` no longer probes a nonexistent `roo` CLI binary (Roo is a
  VS Code extension) — detection is the `.roo/` config dir only; (3) **Cline**
  workflow ingest now captures only agentsync-owned workflows (each rendered
  workflow carries a reversible ownership marker) instead of over-capturing
  human-authored ones, matching memory's ownership scoping — upgrade note:
  workflows rendered by a pre-marker agentsync carry no marker and are treated
  as human-authored, so `import`/`reconcile` skip them and each shows one-time
  drift until the next `apply` re-stamps them; (4) **Claude** MCP and
  hooks ingest decode with `UseNumber`, so an unmodeled native key holding an
  integer larger than 2^53 (e.g. a nanosecond timeout) survives `import`/`reconcile`
  byte-exact instead of being rounded through `float64`; (5) the capability matrix
  gains a coherent per-row **MCP transport-normalization** note for Roo, Cline, and
  the generic breadth tier (documenting the `sse → http` flip for transport-keyless
  dialects), and flags **copilot**/**jetbrains** as not auto-detectable. The
  whole-IDE-dir VersionedDirs tradeoff, the dedup casing assumption, and the
  `HasNestedRepoBelow` full-tree-walk cost are now documented in code comments; the
  **noop** placeholder's package doc, `New` name guard, and `Detect`/`Apply` no-op
  behavior are documented and test-covered.
- **CLI write-path correctness batch (issue #171).** Five disjoint fixes: (1)
  `setDestinationGitBackupMode` now re-parses the spliced `agentsync.toml` before
  writing and **refuses** (leaving the file untouched) if the splice would no longer
  parse or would alter content outside its table — mirroring the `[agents]`
  splicer's backstop. (2) `reconcile`'s interactive EOF path now reaches `done:`, so
  a queued `[o]verride` and pruned state are flushed instead of silently dropped
  when the input stream ends. (3) offline `verify`
  (`AGENTSYNC_ALLOW_OFFLINE_VERIFY=1`) now validates `${secret:…}`/`${env:…}`
  reference **shape** (a malformed `${secret:}` fails) instead of skipping reference
  checks entirely and mis-claiming "all references resolve"; the docs now state
  offline mode checks shape, not resolvability. (4) a user-scope `agent disable
  --purge` is documented as an intentionally cross-scope machine-wide cleanup
  (project-scope `--purge --scope project` stays isolated). (5) the marketplace
  `head_sha`/`name` keys are documented as CLI fetch-cache metadata deliberately not
  modeled in the canonical schema (no silent drop).
- **`plugin upgrade/enable/disable/remove` now accept the `id@marketplace` ref that
  `install` accepts (issue #168).** They previously took `args[0]` verbatim and
  built a bogus `plugins/<id>@<marketplace>.toml` path, so copying the exact install
  ref failed — with a raw `read …: no such file or directory` for
  upgrade/enable/disable. All four now split the ref like `install`, operate on the
  bare id, and report a clean `plugin "<id>" is not installed` when absent.
- **A disabled Codex MCP server now round-trips into canonical `enabled` (issue
  #152).** Codex's per-server `[mcp_servers.<name>] enabled = false` is now ingested
  into the canonical `MCPServerSpec.Enabled` field (present-only capture; absent
  stays default-on) instead of landing in the `Extra` passthrough. `capture.Capture`
  was taught to preserve the source-only `enabled` **only when the ingest carried
  none** (`Enabled == nil`), so a native disable/enable now survives write-back of
  an **already-managed** server instead of being silently reset to the source value
  — completing the round-trip for `reconcile`/re-`import`, not just first import.
  Also adds
  artifact-anchored Codex fidelity tests: MCP+hooks coexistence in one `config.toml`,
  a bundled-skill round-trip asserting byte-for-byte survival + the script's `0o755`
  bit, and a spec-complete multi-paragraph `developer_instructions` body.
- **Release pipeline hardening: pinned goreleaser, reproducible archives, signed
  checksums, and a nfpms↔README URL guard (issue #138).** goreleaser is now pinned
  to the same version (`2.16.0`) across `ci.yml`, `release.yml`, and `just ci` (the
  last via `go run …@2.16.0`), with a CI guard that fails the build if the three
  ever diverge — so the toolchain that validates a release config can't differ from
  the one that ships it. Archive entry mtimes are pinned to the commit date, so
  re-cutting a release produces byte-identical archives and an identical
  `checksums.txt` (restoring the reproducibility scaffolding PR #115 dropped). The
  release `checksums.txt` is now signed with keyless cosign (Sigstore) — artifact
  integrity no longer rests on GitHub TLS alone — using the release job's ambient
  OIDC token, introducing no new signing secret. A new CI guard couples the nfpms
  version-less `file_name_template` to the README's `releases/latest/download/`
  install URLs so a divergence fails CI instead of silently 404ing.
- **The first `apply` is now revertible (issue #143).** Destination git backup used
  to record its first checkpoint only *after* `render.Apply` had already overwritten
  the dir, so a bad **first** apply into a previously-untracked dir had no prior state
  to roll back to and `revert` bailed with "only one checkpoint — skipping". `apply`
  now takes a **pre-apply baseline** checkpoint of each version root *before*
  rendering, so after a first apply there are ≥2 commits (baseline + apply, the apply's
  parent being the baseline) and `revert` restores the genuine pre-apply state. The
  baseline stages only the **managed files' pre-apply content** (plus the local-history
  notice) — deliberately **not** the whole dir: pre-existing siblings agentsync did not
  write (an agent's credentials file, conversation transcripts, your own scratch files)
  are left out of the versioned history so it never becomes a durable copy of your
  secrets, and are preserved untouched across a revert because they are untracked
  (issue #128). The baseline pass is
  best-effort **with a loud warning**: if it can't run (a nesting hazard, a declined
  prompt), the apply still proceeds but you're told that first apply won't be
  revertible; it honors `--no-git-backup`, `mode = "off"`, project scope, and dirs
  under your own source control exactly as the post-apply checkpoint does.

- **`revert`: the uncommitted-tracked-edit safety is now enforced in the engine,
  and partial-reset failures surface a recovery hint (issue #146).** `internal/git.Repo.Restore`
  previously relied on the CLI wrapper snapshotting uncommitted edits to tracked
  files before it ran; any other caller (or a refactor) silently lost them. The
  snapshot is now folded into `Restore` itself — it resolves the target to a
  concrete hash, then snapshots dirty tracked files, so the append-only "nothing is
  rewritten or lost" promise holds for *every* caller, not just `revert`. Untracked
  and gitignored files remain untouched (issue #128). If a rollback fails partway
  after that snapshot, the error now names the snapshot commit and the pre-revert
  checkpoint (`git reset --hard …`) instead of handing back a bare error after
  claiming the edits were preserved.

- **`revert`/`apply`/`doctor` now re-check for a foreign repo nested under a
  destination dir, not just at init (issue #149).** The `HasNestedRepoBelow` guard
  previously ran only when initializing a new backup repo, so a git repo cloned
  under an already-owned root *after* init (e.g. `~/.claude/skills/.git`) was
  invisible — and `agentsync revert`'s hard reset would clobber its checked-out
  files. Now `revert` re-probes before any destructive write and **skips** such a
  root with a warning (errors under `--strict`), including in `--dry-run`; the
  commit path **warns** when a foreign repo appears under an owned root (still
  recording the harmless append-only checkpoint); and `doctor` shows a **warn** for
  an untracked root that contains a nested `.git` instead of a clean `untracked`
  line. The scan is also **symlink-aware** now — a symlinked subdir pointing at a
  foreign repo (which `filepath.WalkDir` would not descend into) is detected via a
  shallow probe of the link target.

- **Windsurf: strip non-`always_on` trigger frontmatter on ingest so re-apply no
  longer produces a malformed double-fenced rule (issue #136).** When a user
  hand-edited a workspace rule's `trigger:` to a non-default value (e.g. `glob`
  or `manual`), ingest previously folded the whole `---`…`---` fence into the
  canonical memory body, so the next `apply` re-prepended agentsync's own
  `trigger: always_on` fence on top of it — a double-`---` block that breaks
  Windsurf activation. Ingest now strips ANY leading frontmatter fence regardless
  of trigger value (the exact agentsync block still round-trips byte-clean; a
  foreign trigger is stripped *and* warned, since its activation mode has no
  canonical home). A hand-authored leading `---`…`---` block in an ingested
  workflow file is likewise stripped from the captured command body.

- **Corrected two copy-paste-wrong docs examples (issue #129).** The onboarding
  and daily-loop pages told users to run `agentsync diff claude`, but `diff`'s
  argument is a filesystem path, not an agent name — `diff claude` matched nothing
  and silently printed "no diff"; the example is now bare `agentsync diff`. The
  configuration reference showed an `[[agents]]` array-of-tables snippet with
  `name = "…"`, but the loader unmarshals `agents` as a table keyed by name, so the
  documented TOML registered zero agents; it now shows the correct `[agents.<name>]`
  form. Also documented the previously-undocumented `[updates]` block and
  `[secrets].file` key.

- **An invalid `[destination_directory_git_backup]` `mode` now errors at config
  load instead of being silently ignored (issue #137).** The strict TOML decoder
  rejected unknown keys but accepted any value, so a typo like `mode = "On"`,
  `"yes"`, or `"true"` silently disabled git backup for untracked dirs (while
  still committing already-owned repos) — and only `doctor` warned. `source.Load`
  now rejects any `mode` outside `{prompt, on, off}` (case-sensitive; empty
  defaults to `prompt`) with a path-prefixed error, so `apply` and `doctor` agree.

- **The capture cleartext backstop now refuses short and empty-edge moved secrets
  (issue #135).** The fail-closed backstop (`secrets.ResidualSecretCleartext`)
  reused the re-reference value set, which drops any resolved secret shorter than
  4 characters — so a 1–3 character credential moved into a field whose source
  counterpart is a literal was written verbatim into the committed canonical
  source with no refusal. The backstop now builds its detection set without the
  length floor (keeping 1–3 char values, excluding only truly-empty ones), closing
  the leak while the re-reference fallback keeps its substring-safety floor
  unchanged. Security hardening.

- **`agentsync revert` no longer deletes your untracked or gitignored files
  (issue #128).** `Restore` used go-git's `HardReset`, which — unlike `git reset
  --hard` — enumerates and removes every untracked and gitignored file in the
  worktree, so a revert silently destroyed the user's own scratch files in the
  managed destination dir during the operation sold as safe recovery. Restore now
  applies only the tracked HEAD↔target delta file-by-file and never enumerates
  untracked entries, so they (and gitignored files) survive byte-for-byte and stay
  untracked. `revert --dry-run` now notes when untracked files are present (left
  untouched).

- **Claude hooks no longer drop unmodeled fields or corrupt non-command handlers
  on `import`→`apply` (issue #124).** The canonical hook model represents only
  command handlers, so an `import` that captured a `settings.json` hook event
  carrying an unmodeled field (e.g. `timeout`) or a non-`command` handler, then
  re-`apply`d it, silently rewrote the user's native `/hooks/<event>` array —
  dropping the extra fields and emitting a structurally invalid
  `{"type":"…","command":""}` entry. Ingest now leaves any hook event it cannot
  fully represent **uncaptured** with a warning (matching the Gemini adapter), so
  a never-captured event is never owned or overwritten by the next apply, and
  Render reports a dropped `Skip` for a non-`command` handler instead of emitting
  an empty-command entry. (An event captured while clean and *later* enriched
  natively still had a stale canonical file that apply kept rewriting; see the
  stale-hook retirement entry above for that half of the fix.)
- **Codex subagent `name` now survives a round-trip, and colliding effective
  names are caught (issue #144).** A Codex custom agent whose frontmatter `name`
  deliberately differs from its file stem (rendered into the TOML `name` field
  but written to `<stem>.toml`) had that divergent value silently erased on the
  next `import`/`reconcile`, because ingest reconstructed the name from the
  filename and never read the TOML `name` back. Ingest now re-populates
  `Frontmatter["name"]` from the TOML `name` field unconditionally on presence
  (lossless for a divergent name, a no-op for a matching one). Separately, two
  subagents with distinct file stems that resolve to the same effective Codex
  `name` — two TOMLs claiming one agent identity — are now reported as a Render
  error instead of being written silently.
- **Gemini subagent native frontmatter keys are preserved on apply (issue #134).**
  `agentsync apply` re-rendered a `.gemini/agents/<name>.md` with only
  `name`/`description`/`model` and a whole-file replace, silently stripping the
  Gemini-native keys (`kind`/`temperature`/`max_turns`/`timeout_mins`/`mcpServers`)
  that `import`/`reconcile` had already captured into canonical. Render now passes
  captured frontmatter through verbatim, dropping only Claude's `tools` (its tool
  vocabulary differs from Gemini's) and `color` (no Gemini agent field) with a
  reported `Skip` that lists exactly those keys — so a hand-authored Gemini agent
  file survives a round-trip instead of being degraded on the next apply.
- **Continue command import no longer silently renames foreign prompt blocks
  (issue #127).** Continue keys a slash command off its prompt block's
  frontmatter `name`, not the filename. Import (`import`/`reconcile`) now captures
  the canonical command identity from the frontmatter `name`, falling back to the
  filename only when it is absent — mirroring the MCP branch. Previously a
  hand-authored or third-party block at `.continue/prompts/foo.md` whose
  frontmatter was `name: bar` was captured as `foo` and rewritten as `/foo` on the
  next apply, silently destroying the user's `/bar`. Render is unchanged.
- **`plugin install`/`disable`/`enable` no longer silently widen or reset a
  plugin's `agents` allowlist (and `update`/`disabled`) (issue #140).** `disable`
  emptied the allowlist and the next `enable` re-materialized `["*"]`, so a
  `disable`→`enable` round-trip silently widened a plugin scoped to
  `agents = ["claude"]` back to *every* agent — fanning its credential-bearing
  MCP servers / hooks / skills out to agents the user deliberately excluded.
  Re-running `plugin install` on an already-registered plugin likewise hard-reset
  `agents`/`update`/`disabled` to the first-install defaults. `disable`/`enable`
  now flip only the `disabled` bit and a re-install carries forward the existing
  `agents`/`update`/`disabled` (refreshing only `id`/`version`/`manifest_sha`
  from the fetch), surfacing what it kept in the status line (e.g.
  `(kept agents=[claude], update=pinned)`). A genuine first install is unchanged
  and byte-identical, preserving the `install`/`import` shared-artifact contract.
- **`secrets set a.b=v` no longer silently destroys a pre-existing scalar secret
  at `a`, and the `set` path now validates the vault before saving (issue #142).**
  Nesting under a key that already holds a scalar value (e.g. `secrets set
  token.scope=repo` when `token` is a stored secret) previously overwrote the
  scalar with a fresh table — irreversibly dropping the cleartext from an often
  git-committed age vault — while still printing success. `secrets set` now
  **refuses** destructive type changes (nesting under a scalar parent, or a leaf
  assignment that would overwrite an existing table) with a clear error that
  never echoes a secret value, and leaves `secrets.age` byte-for-byte unchanged.
  It also runs the same flatten-contract validation (`ValidateVaultTOML`) the
  `secrets edit` path already applied, so a `set` that would yield a vault `apply`
  later rejects is refused at save time (`… (not saved): …`) instead of being
  encrypted with a cheerful success message. Legitimate cases — a new nested key
  under an absent or table parent, and an in-place scalar update — are unchanged.
- **OpenCode subagent/command render preserves supported frontmatter keys
  instead of silently dropping them (issue #125).** The OpenCode render
  functions copied only a hard-coded allowlist (`description`/`model`, plus
  `tools`/`color`/`argument-hint` handling) and dropped every other frontmatter
  key with no signal — including OpenCode-supported keys like `temperature`,
  `permission`, `steps` (agents) and `agent`, `subtask` (commands). Those legal
  OpenCode keys now pass through verbatim, and any key with no OpenCode home is
  surfaced as an `adapter.Skip` (reported in the translation report) rather than
  vanishing: no key is ever both absent from the rendered file and absent from
  the skips.

- **OpenCode agent `mode` is preserved and ingest no longer over-captures
  (issue #148).** OpenCode's `Ingest` previously deleted the agent `mode` key on
  every captured agent, so a native `primary`/`all` agent was silently demoted to
  `subagent` on the next `import`/`reconcile` → `apply` round-trip; the `mode`
  (`primary`/`all`/`subagent`) is now retained in the canonical frontmatter and
  re-emitted verbatim on render (a Claude-shaped subagent with no `mode` still
  defaults to `subagent`). `Ingest` also over-captured the *entire* native
  `agents/`/`commands/` directories — including hand-authored files agentsync
  never wrote — pulling unmanaged user config into the canonical source; it now
  captures only agentsync-owned files (ownership read from apply state).

- **Claude LSP capability corrected.** Claude Code reads LSP servers from plugin
  manifests, not `settings.json#/lspServers`; agentsync now reports canonical
  Claude LSP servers as skipped instead of writing or importing a key Claude
  silently ignores.

- **Chocolatey publishing is back to a single GoReleaser run — the structural fix
  for the v0.7.1–v0.7.3 checksum-mismatch saga.** The release had split the
  Chocolatey `.nupkg` onto a separate `windows-latest` job that *rebuilt* the
  Windows archive and baked the sha256 of its own copy into `chocolateyinstall.ps1`;
  choco then checked that against the archive the Linux job uploaded to the Release.
  Two independent builds are never byte-identical (per-runner module-cache paths,
  then Unix file-mode bits, then the zip's creator-OS byte…), so verification kept
  failing on a checksum mismatch. The chocolatey pipe now runs inside the one Linux
  `goreleaser release` invocation (`choco` runs there under Mono — packaging/push is
  all it needs), so the embedded checksum is computed over the *exact* archive that
  ships and the two can't disagree by construction. The now-unnecessary cross-runner
  reproducibility scaffolding (archive file-mode/mtime pinning) was dropped;
  `-trimpath` + commit-pinned build timestamps stay as general reproducible-build
  hygiene.

- **`ui.Sanitize` now also strips deceptive bidi / zero-width runes, not just
  terminal-control bytes.** Following up on the escape-injection fix below, the
  same untrusted-name display boundary now removes the printable-but-deceptive
  format runes: the explicit Unicode bidi controls (U+202A–U+202E, U+2066–U+2069
  — the "Trojan Source" / CVE-2021-42574 class, where a crafted U+202E could
  visually reorder a plugin id in `explain` output to read as a trusted name)
  and the zero-width / invisible runes (U+200B–U+200D, U+FEFF) that can hide or
  invisibly pad a name. Ordinary right-to-left scripts (Arabic, Hebrew) and CJK
  are preserved byte-for-byte — only the explicit override/isolate controls are
  removed, never the implicit direction of legitimate letters. Display *width*
  (combining marks, wide-width runes skewing `Pad`/`visibleLen` column
  alignment) remains an accepted, documented cosmetic limitation, not a spoofing
  vector.

- **agentsync sanitizes untrusted plugin/component names before rendering them
  to the terminal.** A fetched marketplace plugin's id, version, or a component
  name it supplies is attacker-influenced; rendered raw, a name carrying ANSI/
  escape sequences (CSI color, OSC title-set, `\r`/`\x1b`) could recolor the
  terminal, clear the screen, or spoof rows when a user ran `agentsync explain`,
  `apply`, `plugin`, `marketplace`, `update`, `status`, or `doctor`. The new
  `ui.Sanitize` strips C0/C1 control characters (incl. ESC, CR, LF, TAB, DEL) at
  the display boundary — applied to every site that renders fetched (or
  native-config-derived) plugin/marketplace metadata: `explain`'s skip
  itemization (`emitSkipDetails`), plugin header, and `--list` rows; the shared
  translation report's plugin label that `apply` prints (`render` `printText`);
  the `plugin` (install/list), `marketplace` (add/list), and `update`
  (pending-bump/upgrade) status lines; and the `status`/`doctor`
  undeclared-native-plugin note — before width/`Pad` so a stripped byte can't
  skew column alignment (and the `LF` strip stops an id forging an extra report
  line). Printable text (incl. non-ASCII) is
  untouched, and `explain --json` keeps ids/components raw (the machine contract,
  where the consumer owns escaping). The rendered Codex agent TOML was already
  safe (the marshaller escapes control bytes and the filename uses the canonical
  name).

- **Codex subagents no longer report a spurious "dropped name".** The Codex
  adapter writes the canonical `name` straight into the agent TOML's `name`
  field (Codex *requires* it), but `name` was omitted from the adapter's set of
  known frontmatter keys — so any subagent whose frontmatter carried `name`
  (every Claude-format agent does) was reported as dropping it. For an agent
  whose only otherwise-unmapped key was `name`, this surfaced a misleading `◐
  partial` / `(N skipped)` in `explain` even though the agent translated
  cleanly. `name` is now recognized as a carried-over key (and the frontmatter
  `name` is preferred over the filename, matching Codex's "name is the source of
  truth" rule); `tools` and `color`, which genuinely have no Codex target, are
  still reported as dropped.

- **Plugins now project the components they ship in their conventional default
  locations, not just the ones plugin.json lists.** Claude Code auto-discovers a
  plugin's components from default locations — `commands/*.md`, `agents/*.md`,
  `skills/*/SKILL.md`, `.mcp.json`, `.lsp.json`, `hooks/hooks.json` — whether or
  not `plugin.json` declares them (the manifest is optional; a listed
  commands/agents/mcp/lsp/hooks field *replaces* its default scan, while a listed
  `skills` field *adds* to the always-scanned `skills/` directory, per Claude's
  path-behavior rules). agentsync only convention-scanned
  `skills/`, so any plugin that shipped a command, subagent, MCP/LSP server, or
  hook **only** in its conventional location was silently dropped — `agentsync
  explain code-review@claude-plugins-official` reported `no components` for a
  plugin that plainly ships `commands/code-review.md`, and `code-simplifier`
  (which ships `agents/code-simplifier.md`) likewise. Projection now falls back to
  the default location for *every* component kind agentsync models when
  `plugin.json` does not list it — including when there is no `plugin.json` at all
  — so those components are tracked, rendered, and reported like any other. The
  manifest-SHA pin already hashed the whole plugin cache tree, so existing installs
  are unaffected. As part of this, plugin hooks now parse the canonical nested
  `{matcher, hooks:[{type, command}]}` shape (used by both `hooks/hooks.json` and
  inline `plugin.json` hooks), emitting one hook per command entry and dropping
  non-command hook types agentsync's command-only `Hook` model cannot represent
  rather than projecting an empty hook. Plugin component frontmatter is now read
  the way Claude Code reads it (`source.ParseFrontmatterWithReport`, as the
  adapter Ingest paths already do): a `description` containing bare colons —
  valid to Claude, rejected by strict YAML with *"mapping values are not allowed
  in this context"* — is recovered with a warning instead of aborting the whole
  projection. (Newly surfaced by agent discovery: the official `pr-review-toolkit`
  ships such an `agents/silent-failure-hunter.md`, which previously crashed
  `explain --all` for every plugin once agents were discovered.) Likewise, a
  malformed/unreadable `.mcp.json`, `.lsp.json`, or `hooks/hooks.json` — and a
  convention-discovered `commands/*.md`/`agents/*.md`/`skills/*/SKILL.md` whose
  frontmatter can't be parsed even leniently (e.g. an unterminated block) or whose
  name is a traversal attempt — now drops only that one component with a warning
  rather than aborting the projection, so one bad file in one plugin can't break
  `explain`/`apply`/`status` for every installed plugin. A component the plugin
  author *explicitly listed* in `plugin.json` still fails loudly (a named-but-broken
  component is a hard error); only proactively-discovered files are skipped.

- **`explain <plugin>` now reports only that plugin's components.** `explain`
  previously stamped the *global* translation result onto every plugin row: the
  MCP/command counts and the `(N skipped)` itemization were computed from the
  flattened union of every installed plugin, so e.g. `agentsync explain
  notion@…` listed skipped subagents and LSP servers that belonged to entirely
  different plugins. `explain` now re-projects each requested plugin in isolation
  (`marketplace.ProjectInstalled`) and builds its coverage row from only that
  plugin's own components, so each row — counts, coverage glyph, and skip
  details, in both text and `--json` — reflects exactly the plugin named.
  (`apply`'s end-of-run report still shows the per-agent summary across
  the whole model.)

- **Managed-file banner on rendered memory.** Every rendered memory file
  (`CLAUDE.md`, `AGENTS.md`, …) is now prepended with a short agentsync notice
  naming the file and pointing edits back at `.agentsync/memory/AGENTS.md` +
  `agentsync apply`, so an agent (or human) editing the native file is told it is
  agentsync-managed. The banner lives only in the rendered file — it is wrapped in
  reversible `<!-- agentsync:managed memory-banner -->` markers (the
  `agentsync:managed` namespace carries a per-marker identifier so future managed
  markers stay unambiguous), stripped on ingest and at the
  `import`/`reconcile` write-back funnels, and re-rendered each apply, so it never
  enters the canonical source, never compounds, and (being static) never shows as
  drift. Rendered through one shared helper (`source.RenderManagedMemory`) so it is
  byte-identical across all 31 agents. On by default; opt out with `[memory] banner
  = false` in `agentsync.toml` (the project overlay inherits the user setting).
  The marker namespace `agentsync:managed` is **reserved**: agentsync rejects (with
  a clear error, at load and at capture) any canonical memory whose body or a
  fragment contains it, and the capture strip matches agentsync's full rendered
  banner (not the bare markers) so a user-authored marker block is preserved
  verbatim — never silently deleted.
  **Behavior change:** the first `apply` after upgrading rewrites managed memory
  files to add the banner (a one-time `pending` in `status`).
- **Breadth tier — 22 more agents via a data-driven generic adapter
  (`internal/adapter/generic`).** One `Adapter` implementation serves a long tail
  of agents from a verified `Spec` table: **memory** (a rules file) for all, and
  **MCP** wherever the agent reads a JSON server-map agentsync can express (15 of
  the 22). Dialect knobs handle the variance — root key
  `mcpServers`/`servers`/`mcp`/`context_servers`/the flat namespaced `amp.mcpServers`,
  transport `type`/`transport`/inferred, stdio value `stdio`/`local` — with the
  documented universal `"stdio"` alias accepted on import and a native `"sse"`
  type preserved through capture/apply, remote URL key `url`/`httpUrl`/`serverUrl`,
  and Qwen's Gemini-lineage dual-URL split (`httpUrl` = streamable HTTP, `url` =
  SSE) — and the merge is JSONC-tolerant (hujson): a commented settings file's
  (Zed, Copilot, Amp) foreign keys and values are preserved rather than
  clobbered, with comments stripped on the first agentsync write (the file is
  re-emitted as plain JSON; the original is backed up — see Known limits). Agents added: amp, goose, qwen, warp, jules, junie,
  openhands, amazonq, zed, kilocode, kiro, trae, jetbrains, firebase, antigravity,
  augmentcode, copilot, copilot-cli, crush, factory, pi, mistral — taking agentsync
  to **31 agents** (9 deep + 22 breadth). Each spec's paths were cross-referenced
  against upstream docs and prior-art tools (ruler, rulesync); agents whose MCP is
  an array/YAML/TOML/app-storage get memory-only with MCP reported as a skip.
  Agent-name validation, `doctor` detection, and the `init` template are now
  derived from the deep list + `generic.Specs()`, so adding a breadth agent is a
  verified table row, and `agentsync agent list --all` prints the full supported
  set with each agent's registration state. (Aider and Firebender are deliberately deferred — see the
  capability matrix.)
- **Agent Skills in the breadth tier (18 of the 22 agents).** The generic adapter
  now projects **Agent Skills** — the open [agentskills.io](https://agentskills.io)
  `SKILL.md` directory spec — wherever the agent natively scans a skills directory,
  via a new per-scope `Skills` target on each `Spec`. Because the on-disk skill
  format is uniform (no dialect to model), the tier reuses the deep adapters' shared
  `claude.SkillFileOps` projection, so bundled `scripts/`/`references/`/`assets/`
  and executable bits round-trip byte-for-byte through apply/import/reconcile. Most
  agents target the cross-vendor `.agents/skills/` convention — byte-identical to
  Codex's, so the render pipeline dedupes the ops — while a few scan their own
  dir (`.qwen/skills/`, `.junie/skills/`, `.kiro/skills/`, `.factory/skills/`,
  Copilot's `.github/skills/`). Skills-capable: amp, goose, qwen, warp, junie,
  kilocode, kiro, trae, jetbrains, antigravity, augmentcode, copilot, copilot-cli,
  crush, factory, pi, zed, mistral. The four without are reported as a skip:
  **jules** and **firebase** publish skills *for other* agents, **amazonq**
  consumes skills only through an MCP server, and **openhands** loads skills
  programmatically with no auto-scanned directory. Each path was cross-referenced
  against the agent's upstream docs.
- **Cline adapter (`internal/adapter/cline`).** A new real adapter for Cline,
  scope-asymmetric (informed by competitor prior art — no config-sync tool writes
  the VS Code globalStorage MCP path): MCP renders at **user scope** into the Cline
  CLI's clean `~/.cline/mcp.json` (`mcpServers`, transport inferred — stdio
  command/args/env, remote `url` + `headers`; merge-by-server-name preserves
  foreign servers), while memory → `.clinerules/agentsync.md` (plain markdown rule)
  and slash commands → `.clinerules/workflows/<name>.md` (plain markdown workflows)
  render at **project scope**. The non-applicable scope reports a skip. Skills,
  subagents, hooks, and LSP have no Cline concept and are skipped. `agent add
  cline` / `import cline:…` work end-to-end.
- **Roo Code adapter (`internal/adapter/roo`).** A new real adapter for Roo Code,
  built on the clean filesystem `.roo/` paths two other config-sync tools
  (rulesync, ruler) independently converged on: MCP → project `.roo/mcp.json`
  (`mcpServers` with explicit `type: streamable-http`/`sse` for remote;
  merge-by-server-name preserves foreign servers), memory → `.roo/rules/agentsync.md`
  (plain always-applied rule) and slash commands → `.roo/commands/<name>.md`
  (markdown + frontmatter — keeps BOTH `description` and `argument-hint`; only
  `allowed-tools` drops), both at user and project scope. Roo's global MCP lives in
  VS Code globalStorage (OS/editor-specific), which agentsync intentionally does
  not target, so user-scope MCP is reported as a skip. Skills, hooks, LSP, and
  per-file subagents (Roo uses "custom modes") have no Roo target and are skipped.
  `agent add roo` / `import roo:…` work end-to-end.
- **Windsurf adapter (`internal/adapter/windsurf`).** A new real adapter for
  Windsurf (Cascade): MCP renders at **user scope** into the global
  `~/.codeium/windsurf/mcp_config.json` (JSON `mcpServers`; stdio
  command/args/env, remote `serverUrl` + `headers`; skipped + reported at project
  scope — Windsurf has no project MCP file). Memory renders at **both** scopes:
  project → `.windsurf/rules/agentsync.md` with the documented
  `trigger: always_on` activation frontmatter (stripped on import; byte-clean
  body), user → the global `~/.codeium/windsurf/memories/global_rules.md`
  (always-on, frontmatter-less). Slash commands render at **both** scopes as
  plain-markdown workflows (`.windsurf/workflows/`, global
  `~/.codeium/windsurf/global_workflows/`; command frontmatter dropped with a
  report). Skills, subagents, hooks, and LSP have no Windsurf concept and are
  skipped. `agent add windsurf` / `import windsurf:…` work end-to-end.
- **Continue adapter (`internal/adapter/continuedev`).** A new real adapter for
  Continue, projecting components as Continue "blocks" (one file per item under
  `.continue/`, so the adapter owns no shared key-merge file): MCP servers →
  `.continue/mcpServers/<id>.yaml` (stdio command/args/env; remote
  `streamable-http`/`sse` + `url` with auth headers under `requestOptions.headers`
  — full fidelity), memory → `.continue/rules/agentsync.md` (a frontmatter-less
  always-apply rule; byte-clean round-trip), and slash commands →
  `.continue/prompts/<name>.md` prompt blocks (`name`/`description`/`invokable`;
  `argument-hint`/`allowed-tools` dropped). Skills, per-file subagents, hooks, and
  LSP have no Continue concept and are skipped with a report. `agent add continue`
  / `import continue:…` work end-to-end. (Package is `continuedev` because
  `continue` is a Go keyword; the agent name is still `continue`.)
- **Gemini CLI adapter (`internal/adapter/gemini`).** A new real adapter for
  Google's Gemini CLI: MCP servers and lifecycle hooks both merge into
  `.gemini/settings.json` with a **JSONC-tolerant merge** — Gemini itself reads
  settings.json as JSONC, so a commented file's foreign keys are preserved
  rather than clobbered (comments are stripped on the first write, like
  `opencode.json`) — (MCP under `mcpServers` with Gemini's `url`/`httpUrl`
  transport split; hooks under `hooks` in the same nested shape as Claude, with
  events remapped to `BeforeTool`/`AfterTool`/`BeforeAgent`/`AfterAgent`/… and
  unmapped events dropped with a report; import leaves hook events with
  unrepresentable fields uncaptured, with a warning), memory → `GEMINI.md`
  (`~/.gemini/GEMINI.md` user / repo-root `GEMINI.md` project), slash commands →
  `.gemini/commands/<name>.toml` (`description` + `prompt`; `argument-hint`/
  `allowed-tools` dropped), and subagents → `.gemini/agents/<name>.md` (Claude's
  `tools` vocabulary differs from Gemini's, so `tools`/`color` are dropped with a
  report). Skills (Gemini uses extensions) and LSP have no Gemini concept and are
  skipped. `agent add gemini` / `import gemini:…` work end-to-end.
- **Cursor adapter (`internal/adapter/cursor`).** Cursor graduates from a
  registered no-op to a full adapter: MCP servers → `.cursor/mcp.json` (the same
  `mcpServers` shape as Claude, full fidelity), memory → repo-root `AGENTS.md`
  (project scope only — Cursor keeps user-level rules in app-local storage),
  skills → `.cursor/skills/<name>/` (whole-directory fidelity), subagents →
  `.cursor/agents/<name>.md` (`tools`/`color` dropped with a report), slash
  commands → `.cursor/commands/<name>.md` (plain markdown — frontmatter dropped),
  and hooks → `.cursor/hooks.json` (Claude's lifecycle events remapped to Cursor's
  camelCase names with the required top-level `version` always emitted; events
  with no Cursor equivalent dropped with a report). LSP is unsupported (Cursor has
  no LSP concept). `agent add cursor` / `import cursor:…` now work without
  `AGENTSYNC_ALLOW_UNIMPLEMENTED`. Plugin discovery (`PluginIngester`) is deferred
  — Cursor's native enable-state location is undocumented — but Cursor still
  receives plugin-projected components on `apply` like every adapter.
- **`docs/comparison.md` — "How agentsync compares."** A new canonical doc
  surveying the AI coding-agent config landscape (gaal, agentsmesh, rulesync,
  ruler, ai-rulez, the MCP managers, the skills tools, the AGENTS.md standard),
  with a feature matrix across the multi-agent / bidirectional / component-coverage
  / secrets axes and an honest read on where agentsync is differentiated.
  Mirrored to the site at `/comparison/` via
  `sync-docs.mjs` (sidebar: **Start here → How agentsync compares**).
- **Context7 AI chat widget on the docs site.** The published site at
  [agentsync.cc](https://agentsync.cc) now loads the Context7 chat widget on
  every page (an async floating chat button, wired to the
  `/spxrogers/agentsync` Context7 source) via a `script` tag in Starlight's
  `head` config (`website/astro.config.mjs`).

### Documentation

- **The epic #178 QA remainder is recorded in-repo.**
  [`docs/qa/2026-07-epic-178-remainder.md`](docs/qa/2026-07-epic-178-remainder.md)
  states what issue #164's real-harness QA demanded, which regression fixtures
  were delivered instead (now including `TestRestore_PackedHistory` across a
  fully repacked object store and the measured `BenchmarkHasNestedRepoBelow`
  walk cost), and what remains genuinely un-executed (live harness launches,
  real-machine wall-clock numbers) — so the v1.0 tag decision weighs the gap
  explicitly.
- **The Gemini subagent frontmatter passthrough is documented as a deliberate
  secret-machinery exception.** Subagent frontmatter (including a command/env-shaped
  `mcpServers` block) is never secret-resolved and never re-referenced; the
  capability matrix, the adapter doc comment, and the project memory's
  secret-invariants section now say so explicitly.
- **Build-time link/anchor checker for the docs website (issue #170).** New
  `website/scripts/check-links.mjs` walks every `.md`/`.mdx` under
  `website/src/content/docs/` and fails the build on any broken in-site link —
  a site-absolute `/route` that resolves to no page, or a `#anchor` that is not
  a real heading slug on the target page (computed with `github-slugger`, the
  GitHub-style slugger Starlight uses). It runs from `predev`/`prebuild` after
  `sync:docs`, is runnable standalone via `bun run check:links`, and skips
  external/`mailto:`/GitHub-blob links. Script unit tests
  (`website/scripts/check-links.test.mjs`, `bun run test` → `node --test`) pin
  the real slugs and exercise the checker over a temp fixture tree. A single
  pre-existing `concepts.md` → `/getting-started/introduction/#command-reference`
  anchor breakage (owned by #145) is parked in an explicit `ALLOWLIST`.
- **The website link checker now runs in CI on every PR and fails on stale
  allowlist entries.** Previously the checker only ran from the site's
  `predev`/`prebuild` hooks, which CI never invoked — a broken in-site link
  merged cleanly and only failed at docs-publish time. A new `docs-links` job
  in `ci.yml` (bun, matching `docs-publish`) regenerates the mirrored contract
  pages, runs the checker, and runs its unit tests on every PR. The checker
  itself now also fails when an `ALLOWLIST` entry matched zero violations in a
  run: a stale exception means the underlying link was fixed, and it must be
  removed rather than linger to mask a future regression.
- **New "Rolling back a bad apply" website guide (issue #170).** Added
  `website/src/content/docs/guides/rollback.mdx` (mirroring the
  `docs/user-guide.md` rollback section): the destination git-backup enable
  prompt and `[destination_directory_git_backup]` modes, `apply --no-git-backup`,
  `agentsync doctor` status, the append-only `revert` (`--to`/`--all`/`--dry-run`),
  the reconcile-after-revert step, the shared-dir caveat, and the
  never-pushed/cleartext-history note. Wired into the `Guides` sidebar and
  cross-linked from the `revert` reference in `reference/cli.mdx`.
- **Empirically verified release reproducibility and corrected the
  `.goreleaser.yaml` claim to match (issue #173).** Building two snapshot releases
  from the same commit with the release-pinned goreleaser `2.16.0` and diffing the
  `dist/` trees showed the binaries and the six tar.gz/zip archives are
  byte-for-byte identical run-to-run (the archive-mtime pinning restored in #138
  works), but the nfpms deb/rpm — and therefore `checksums.txt`, which hashes
  them — are **not**: the packages embed the wall-clock build time (deb
  directory-entry mtimes and the rpm `BUILDTIME` header), not the commit date. The
  `release`/`archives`/`checksum` comments claiming "(reproducible, identical)"
  assets and "an identical checksums.txt" were corrected (comment text only, no
  behavioral config change — pinning `nfpms.mtime` belongs with #138). Added
  `internal/release/reproducibility_test.go`, a guard that reads the on-disk
  `.goreleaser.yaml` and fails if the archive-entry mtime pins are stripped (the
  PR #115 regression class), keeping the reproducible-archives claim
  self-certifying.
- **The #173 two-snapshot reproducibility diff is now a committed, re-runnable
  script.** `scripts/reproducibility-diff.sh` builds two goreleaser snapshots
  (release-pinned 2.16.0, sleeping across a second boundary between runs),
  byte-compares the tar.gz/zip archives — any difference fails — and reports
  the known-nonreproducible deb/rpm and `checksums.txt` informationally.
  Deliberately not wired into CI: a full artifact diff would be permanently
  red on the nfpms half (declined in #173).
- **Synced the v1.0 git-backup feature's contract docs (issue #141).** The
  components map now carries `internal/git` (the leaf go-git wrapper) and
  `internal/ui`, the full adapter set (9 deep adapters + generic breadth tier +
  noop), each deep adapter's `homedir.go`, and the `revert` / `version` commands;
  the architecture §10 package-layering graph gains a `git` node (no push surface)
  and a new `### VersionedDirs (optional)` §3 subsection; the `doctor` reference
  (CLI + user-guide command tables and the `### doctor` body) documents its
  Destination-git-backup section (mode + per-dir repo status); and the README
  gains a "Known limits" bullet for the local-only, never-pushed backup history.
  Also corrected the stale `internal/project` `.agentsync.toml`-marker description
  to the current `.agentsync/`-directory overlay, added the
  `comparison/index.md` ↔ `docs/comparison.md` mirrored-pages row to
  `website/README.md`, and refreshed a few `Files` lists (secrets `leakscan.go`/
  `runtime.go`, marketplace `treehash.go`).
- **Cleared the carried-over doc-vs-code drift cluster (issue #145).** Corrected six
  "plugin import is Claude-only" claims (Codex implements `PluginIngester` too),
  the Windsurf `components.md` scope/`WarnEmitter` description (memory + commands
  render at both scopes; it *does* implement `WarnEmitter`), Gemini's
  `merge-json-keys`→`merge-jsonc-keys` source comments, a broken `concepts.md`
  architecture anchor, and copy nits (agent count `30+`→`31`, "three axes"→"four").
  Acknowledged Windsurf's separate 12,000-char workspace-rule/workflow limit in the
  capability matrix (verified against upstream docs).
- **Install & git-backup docs: macOS Gatekeeper guidance, arm64 download URLs, and
  qualified safety promises (issue #132).** The README and the website install page
  now tell raw-archive macOS users that the prebuilt binaries are unsigned/
  un-notarized (Gatekeeper blocks first run) and how to clear the quarantine
  attribute (`xattr -dr com.apple.quarantine`), noting Homebrew users don't need it;
  both now show the `arm64` `.deb`/`.rpm` download URLs alongside `amd64`.
  `docs/concepts.md` records that the local git-backup `.git` `0700` hardening is
  POSIX-only (a Windows no-op → ACLs are the boundary), and the user-guide's revert
  "nothing is lost" promise is qualified to **tracked files only** (untracked scratch
  files are outside the snapshot).

### Security

- **Render-time path-traversal guard at the adapter dispatch layer (issue #156).**
  Every deep adapter joined a component id straight into a destination filename,
  with no Render-time check — a subagent/command/skill `Name`, and (for adapters
  that write one file per server, e.g. continuedev's
  `filepath.Join(MCPDir, id+".yaml")`) an MCP/LSP server id. An id like
  `../../../tmp/x` would render a `FileOp.Path` that escapes the agent's config dir
  (a write-anywhere primitive on apply); a marketplace-projected MCP id is an
  especially untrusted source (a raw manifest map key). `render.Plan` (the single
  dispatch waist) now validates **every** such id — subagent/command/skill `Name`
  **and** MCP/LSP server id, project overlay included — against the **same**
  `source.ValidateComponentID` the dest→source write boundary already used, so
  **all** adapters — current and future — inherit the guard uniformly, closing the
  recurring Gemini/Windsurf (and all-adapter) unsanitized-name gap. The id set is
  model-wide, so it is validated **once up front** (not per-agent) and a
  traversal-, separator-, absolute-, bare-`.`, all-whitespace-, or
  control/deceptive-rune-bearing id refuses the whole plan (never a silent `Skip`)
  with an agent-agnostic error naming the component kind and id; a `filepath.Clean`
  containment backstop additionally rejects any emitted write whose cleaned path
  still traverses upward (covering bundled skill-file paths too). `Plan` reads only
  the id strings (a string-only `secrets.Resolved.ComponentIDs()` accessor), so the
  guard never unwraps the resolved model and the secrets lint fence is unchanged.
- **Offline `verify` no longer prints unsanitized malformed-reference candidates
  (issue #171 hardening).** `agentsync verify` in offline mode
  (`AGENTSYNC_ALLOW_OFFLINE_VERIFY=1`, the documented CI path) reports each
  `${secret:…}`/`${env:…}`-shaped token whose reference shape is invalid. Those
  tokens are config-derived — agentsync configs are shareable dotfiles — and the
  loose shape-matcher's tail can capture raw ESC / C1 / newline bytes, so a crafted
  config could smuggle terminal escapes into the CI log through the error message.
  `secrets.MalformedSecretRefs` now returns `[]untrusted.Text` and `verify` renders
  them via `untrusted.Join`, so every displayed candidate is sanitized by
  construction. A **systematic sweep of the whole `#93/#171` class** (a
  config-/native-config-derived string reaching the terminal raw) then closed
  every remaining site the review surfaced across the CLI: `verify`'s
  `[agents.<name>]` validation error (now `%q`); `verify`'s and `doctor`'s
  `[secrets]` `identity_file`/`age_file` path errors (which print even for a
  non-existent path); `doctor`'s schema-invalid line (which echoes the raw
  offending config source line via go-toml's strict-decode error);
  `marketplace list`'s `url`/`head_sha` columns; `agent list`'s `[agents.<name>]`
  key and display-only `scope` value; `mcp list`'s server-id (filename-stem)
  column; the `status`/`diff`/`apply` path dashboards and interactive
  `reconcile`'s item-label + mcp-server-id; and `secrets`'s age-backend path
  errors (`internal/secrets/age.go`) — each sanitized at its display boundary via
  `ui.Sanitize`/`untrusted.Text`, with `reconcile`'s ignore-pattern write kept
  raw so the persisted pattern is exact. A follow-up verification pass then closed
  the *indirect* leaks of the same class — the config-derived path surfacing
  through an **error value** or a **derived path** rather than a direct print:
  `render.CollisionReport.String()` (printed by `reconcile` override + `update
  --apply`); `reconcile`'s orphan backup/remove errors, write-back error, and the
  `filepath.Rel` conflict path; `import`'s dry-run marketplace preview (a
  plain-string native marketplace id, ungated by `ValidateComponentID`) and its
  undeclared-native-items warning; and `revert --dry-run`'s change list. New
  `escape_sweep_test.go` + `render/collision_report_test.go` drive each command
  with a hostile fixture and assert no raw ESC/bidi byte reaches stdout
  (representative sites break-verified). The offline shape check itself was also corrected to
  match the WHOLE candidate (an anchored check), so a malformed outer ref that
  merely embeds a well-formed nested ref (`${secret:${env:FOO}}`) is now flagged
  instead of silently accepted. Separately, broadened the direct-`os.*`
  destination-write guard (issue #163) to cover `os.OpenFile`/`os.Rename`/`os.Truncate`
  in addition to `Remove`/`RemoveAll`/`WriteFile`/`Create`, closing the
  truncate-write / rename bypass of the `DestWriter` foreign-collision backup.
- **The local `.git` rollback history's secret-at-rest control is now re-asserted on
  every apply (issue #126).** Destination git-backup repos deliberately persist
  *resolved cleartext secrets* into a **local-only** `.git` history; that exception is
  made acceptable by two controls — the repo is never pushed, and `.git` is tightened
  to `0o700`. The `0o700` control used to be set **once at init** (with a silently
  swallowed error) and never re-checked, so a history whose perms later drifted looser
  (a `git gc`, a restore-from-tar, an admin `chmod`) stayed world/group-readable while
  every apply wrote fresh secret blobs into it. `.git` perms are now stat-checked and
  best-effort re-tightened to `0o700` on every apply's Open/commit path (a POSIX no-op
  when already tight; a warning to stderr when it had drifted); the init-time chmod
  failure is surfaced the same way instead of swallowed. `Init` is now atomic-ish — the
  local-history NOTICE is written **before** the managed marker (and the half-created
  `.git` is rolled back on failure), so a repo that `Detect`s as agentsync-owned always
  carries the "do not push / may contain cleartext" notice. A `mode = "on"` apply that
  auto-inits a new dir now prints a one-time-per-run caution that the local-only history
  may hold cleartext secrets, and `revert`'s dirty-tracked snapshot prints the same
  caution before it commits possibly-just-typed cleartext. All of this stays
  best-effort: a chmod failure never aborts the apply, and no push surface is added.
- **Secret-invariant edges hardened (issue #163).** `capture.Capture` is now
  fail-closed under indeterminacy: when the secrets backend can't resolve a
  `${secret:…}` the source references (vault locked/unavailable), the leak-check
  value prong is blind, so Capture **refuses** the write-back instead of degrading
  to a warning. `EnvBackend` now uses presence semantics (`os.LookupEnv`) so a
  set-but-empty env var resolves to `""` like `AgeBackend`. The `DestWriter`
  write-ban is now fenced for the destructive `os.*` family
  (`os.Remove`/`os.RemoveAll`/`os.WriteFile`/`os.Create`) via a forbidigo rule +
  a source-scanning test (previously only `iox.AtomicWrite` was fenced). Plus
  smaller edge hardening: the age identity-permission stat-bypass is documented +
  directly tested, the drift classifier's deleted-destination rows are pinned, and
  the capture residual warning is reworded to describe what it actually checks.

### Changed

- **Carried-over core/git nits: fail-safe `git.Detect`, cheaper re-applies, and a
  visible sticky-skip note (issue #176).** `apply` no longer runs the full per-root
  git-status round-trip on an agentsync-owned destination dir that had nothing
  written and no tracked deletions this run (clean re-applies are faster). When you
  answer "yes" to the git-backup prompt for a directory that is then skipped for a
  nested-repo conflict, agentsync now prints a one-line note that auto-backup was
  still enabled globally (previously the `mode=on` switch flipped silently).
  Internally, `git.Detect` now returns the fail-safe `foreign` state (never the
  init-eligible `untracked`) alongside any open error, the hook `Event` field is
  modeled as sanitizing `untrusted.Text`, and the ten identical adapter `Apply`
  dispatchers collapse to one shared helper — no user-visible behavior change from
  those.
- **`agentsync secrets set` refuses an empty value by default (issue #165).** An
  empty or whitespace-only value (a fat-fingered paste, an empty `pbpaste`/
  `1password` pipe) across any input mode (`--stdin`, interactive prompt, legacy
  `key=`) is now refused with a value-free error rather than silently stored as a
  broken secret that resolves to `""` at apply time. Pass the new `--allow-empty`
  flag to store an empty value deliberately.
- **`Detect()` is now wired into `doctor`; the dead `Capabilities()` bitmask is
  removed from the `Adapter` interface (issue #177).** Both methods previously had
  no production consumer. `doctor`'s adapter-detection section now calls each
  adapter's richer `Detect()` (config-dir stat + PATH fallback) instead of a
  PATH-only lookup — reported informationally, never failing the check. The
  per-agent `Capability` bitmask, which nothing in the pipeline consumed and which
  could silently drift from real `Render`/`Skip` behavior, is deleted along with the
  `Capability` type and `Cap*` consts; component support is (and was) expressed by
  `Render` returning `[]Skip`, and `docs/architecture.md`'s false "the pipeline
  reports those components as skipped [via the bitmask]" claim is corrected.
- **Capability matrix: the Claude Hook cell is corrected from `✓` to `◐` (issue
  #147).** agentsync models only `command` hooks (`matcher` + `command`); those
  round-trip losslessly, but a non-`command` handler type or an unmodeled field
  (e.g. `timeout`) is now *reported* (a render Skip / an ingest warning that leaves
  your native entry untouched) rather than claimed as full-fidelity. The ◐ is
  backed by the artifact-anchored `TestIngest_HookArtifactRoundTrip`.
- **Capability matrix: the Codex subagent `name` claim is corrected (issue #150).**
  The matrix now states that a Codex subagent's `name`/`description`/`model`
  round-trip in **both** directions — a frontmatter `name` diverging from the file
  stem survives ingest (re-populated from the TOML `name`) instead of being
  silently rederived from the filename — and that colliding effective names are
  refused at render, matching the shipped fix.
- **Each adapter's key-merge strategy is now a machine-checked, load-bearing
  invariant (issue #157).** A new central guard
  (`TestKeyMergeStrategy_MatchesEmittedOps`) renders a real MCP+hook fixture
  through every registered adapter and asserts `KeyMergeStrategy()` — the single
  static value `orphanCleanupOps` trusts for its destructive cleanup writes —
  equals the `MergeStrategy` on every key-merge `FileOp` the adapter emits (and
  `""` ⇒ no key-merge ops). The interface + architecture docs now record the
  single-strategy-per-adapter constraint explicitly, and correct a stale
  architecture-doc line that listed Gemini under `merge-json-keys` (it emits
  `merge-jsonc-keys`).
- **A project-scope `[destination_directory_git_backup]` override is no longer copied
  into the merged config (issue #126).** Git backup is a user-scope-only feature
  (`VersionRoots` returns nil at project scope), so a project override could never take
  effect; the overlay is dropped and the merge comment made honest, rather than
  silently accepting an inert value.

- **Chocolatey publishing is temporarily paused (issue #188).** The `chocolateys`
  block in `.goreleaser.yaml` is commented out while the `agentsync` package
  waits in Chocolatey's community moderation queue — `choco push` returns 403
  until it clears review, and that failure sank the tail of the v0.10.0 publish
  after every other channel had shipped. Homebrew, Scoop, deb/rpm, and the
  GitHub Release archives are unaffected. The block will be re-enabled verbatim
  once the package is approved.
- **BREAKING: project scope now requires the project to declare its own agents
  (issue #183).** A project tree's `[agents]` table in
  `<root>/.agentsync/agentsync.toml` is now **authoritative**: project-scope
  rendering targets exactly the agents the project declares, and an empty or
  missing table is a **hard error** on every scope-aware render path —
  `apply`/`status`/`diff`/`reconcile`/`update --apply`/`verify` — instead of
  silently inheriting the current user's enabled agents.
  Inheritance made the committed project tree render differently on each
  collaborator's machine (whoever ran apply decided which `.claude/`, `.codex/`,
  … trees existed); now identical source always produces an identical render.
  `import --scope project` is deliberately exempt so a tree can still be
  bootstrapped from native config before agents are declared. **Migration:**
  projects that relied on an empty `[agents]` must now declare their agents —
  run `agentsync agent add <name> --scope project` (or edit the `[agents]`
  table) for each agent the project should render to.
- **All `agent` subcommands are scope-aware.** `agent
  add|remove|list|enable|disable` now take `--scope project` / `--project
  <path>` and edit the project tree's own `[agents]` declaration (project
  entries are written without the redundant `scope` key). At project scope,
  `agent disable --purge` removes only that project's rendered files and state
  — never the user's machine-wide destinations or another repo's files. (At
  user scope, `--purge` keeps its historical semantics: it cleans up that
  agent's rendered files across every scope and project.) `agent add/enable/…`
  rewrites of the `[agents]` table are now also validated fail-closed: if the
  regenerated file would not re-parse, or would alter ANY data outside the
  `[agents]` table (e.g. an exotic TOML construct the rewriter cannot splice,
  like a multi-line string containing an `[agents]`-shaped line), the command
  refuses and leaves the file untouched.

- **Docs website: new "drafting table" visual identity.** agentsync.cc gets its
  own look instead of stock Starlight: Space Grotesk / JetBrains Mono
  (self-hosted), a warm graphite + amber palette (manila drafting-paper light
  mode), gruvbox code themes, square-cornered chrome, and an animated
  source-to-agents fan-out hero on the landing page. Styling only — no content
  or navigation changes. The `og.png` social card is regenerated to match
  (amber-on-graphite, same typefaces), so link previews carry the new brand;
  `og-light.png` is a manila-paper variant of the same card (an alternate for
  surfaces like GitHub's repo social preview — `og:image` still points at
  `og.png`), and the favicon is recolored to the amber palette.

### Added

- **CLI honesty & CI-usability batch (issue #155).** New and corrected user-visible
  behavior across `status`/`diff`/`apply`/`reconcile`/`mcp`/`doctor`:
  - **`status --exit-code` / `diff --exit-code`** turn those commands into CI gates:
    exit `2` when drift (status) or any hunk (diff) exists, `0` when clean. Exit `2`
    is distinct from the generic error exit `1`, and the sentinel prints no extra
    error line. Without the flag both still exit `0`.
  - **`diff --agents <list>`** narrows the diff to an agent allowlist, mirroring
    `status --agents` exactly (same `*`/validation and empty-rejection message).
  - **`mcp add --header "Name: Value"`** (repeatable, http/sse only; rejected for
    `stdio`) sets request headers — the usual remote-auth secret site. Values may
    reference secrets (`--header "Authorization: Bearer ${secret:TOKEN}"`), which
    resolve at apply and re-reference on capture (no schema change — `Headers` was
    already a secret-resolving field).
  - **`apply` delete-only summary**: a run that only removes a component from source
    now reports `removed: N ops` (mixed runs `applied: X ops, removed: Y ops`)
    instead of the misleading `up to date` / `applied: 0 ops`. A genuine clean
    re-apply still reads `up to date`.
  - **`diff <path>`** now reports a typo'd/unmanaged path (`path <p> is not managed
    by agentsync …`) distinctly from a clean managed path (`no diff`).
  - **`reconcile`** shows the actual differing source/destination **values** (a
    masked text diff — resolved secrets are shown as their `${secret:…}`
    placeholder, never cleartext) in the destructive prompt and `[d]iff`, instead of
    bare SHA-256 prefixes; can now **persist a destination-side MCP-server deletion**
    into the canonical source on `[w]rite-back` (through the approved deletion
    funnel, guarded against multi-agent fan-out); and its bulk-confirm count now
    equals the true blast radius of the chosen action.
  - **`doctor`** now actually resolves every `${secret:…}`/`${env:…}` reference and
    flags an unresolvable/typo'd one (a bad `${secret:…}` fails, an unset `${env:…}`
    warns) instead of reporting all-green; resolved values are never printed.
  - Interactive prompts reachable during a `--json` run (the scope menu) now write
    to **stderr**, so a `--json` payload piped from stdout is never corrupted.

- **Destination dirs can be git-versioned for one-command rollback (issue #118).**
  `agentsync apply` now optionally keeps each rendered destination dir in its own
  **local-only** git repo, recording a checkpoint commit after every apply that
  changes managed files there. The unit is the **directory**: every agent's config
  dir (`~/.claude`, `~/.codex`, …) plus shared cross-agent dirs (e.g.
  `~/.agents/skills`, which Codex and several agents all write to) are versioned,
  with shared dirs de-duplicated to a single repo and nested dirs collapsed (no
  repo inside a repo). A new first-class **`agentsync revert <agent>`** rolls a
  dir back to a prior checkpoint (append-only — it records a new commit, never
  rewrites history; uncommitted hand-edits to tracked files are preserved as a
  snapshot first, so nothing is lost; `--to` picks a checkpoint, `--all` covers
  every managed dir, `--dry-run` previews) and warns you to reconcile before the
  next apply. Behavior is controlled by a new global
  `[destination_directory_git_backup]` table in `agentsync.toml`
  (`mode = "prompt"` (default) `| "on" | "off"`, plus optional `author_name` /
  `author_email`); the first apply to an untracked dir **prompts** (opt-out), and
  `apply --no-git-backup` bypasses it for CI/scripting. `agentsync doctor` shows
  the current mode and per-dir status. These repos are **never pushed** — the
  history may hold the cleartext secrets the rendered files already contain, which
  is acceptable precisely because it stays local (the canonical `~/.agentsync/`
  source you push still carries only secret *references*). An existing user-managed
  repo in a destination dir is detected and left untouched.
- **Windows distribution via Scoop and Chocolatey is wired and ready to ship
  (issues #74, #75).** `.goreleaser.yaml` now carries fully-configured `scoops`
  and `chocolateys` blocks, and the whole release — GitHub Release, archives,
  deb/rpm, Homebrew cask, Scoop bucket, and the Chocolatey `.nupkg` — is produced
  by one `goreleaser release` run on a single Linux job (`choco` runs there under
  Mono, so no separate Windows runner is needed). Scoop pushes
  `bucket/agentsync.json` to `spxrogers/scoop-bucket` with the
  `SCOOP_BUCKET_GITHUB_TOKEN` secret (a plain `{{ .Env.… }}` token, like the
  Homebrew cask). Chocolatey is gated on the *presence* of `CHOCOLATEY_API_KEY`:
  the pipe is skipped (`--skip=chocolatey`) and the choco CLI isn't even set up
  unless the secret is set — adding it is the whole "go live on choco" step. The
  CI snapshot job explicitly skips chocolatey (the CI runner has no `choco`).
- **Releases can now be cut from the GitHub UI / mobile app, no laptop
  required.** The `release` workflow grew a `workflow_dispatch` trigger with a
  `version` input alongside the existing `push: tags` one: "Actions → release →
  Run workflow → enter `vX.Y.Z`" validates the version (same `v`+semver check as
  the `just release` recipe), creates and pushes the annotated tag at the
  dispatched ref's HEAD, then publishes — all in a single run. Tagging and
  publishing happen in the same job on purpose: a tag pushed with the default
  `GITHUB_TOKEN` does not start a new workflow run, so a "push the tag and let
  the tag trigger fire" design would tag but never publish (and conversely, that
  same rule is what stops the manual run from kicking off a duplicate release).
  The laptop path (`just release vX.Y.Z`) is unchanged. Both paths now share a
  single validator, `scripts/release-tag.sh` (CI exercises it via `--self-test`),
  so the `v`+semver rule lives in exactly one place; the workflow also gained a
  `concurrency` guard so a dispatch and a tag push can't race to publish the same
  version.
- **Untrusted plugin/marketplace metadata is now sanitized by type, not by
  convention (`internal/untrusted`).** Issue #93 / PR #100 sanitized ~24 terminal
  print sites by hand-wrapping each fetched id/version/marketplace-name in
  `ui.Sanitize`, but nothing stopped the *next* `Fprintf` from printing one raw
  and silently reintroducing the escape-injection class. Those fields are now the
  defined string type `untrusted.Text`, whose `String()` sanitizes — so printing
  one through `fmt` is safe **by construction**, and obtaining the raw bytes
  requires the explicit, greppable `Unverified()` (filesystem/lookup use only).
  The canonical TOML / marketplace.json wire format is unchanged (`Text` is a
  string kind: `omitempty` still elides an empty value, and `--json` surfaces
  still emit the raw value — the machine contract). Reflection-based
  `TestUntrustedFieldGuard`s in `internal/{source,marketplace,render}` fail the
  build if a new string field is added to the plugin/marketplace identity or
  report-row structs without being classified untrusted-or-trusted, and the
  established carve-outs (hex SHAs, `%q` URLs, user-supplied CLI args, enum modes)
  stay plain strings. `ui.Sanitize` is unchanged in behavior (it now delegates to
  `untrusted.Sanitize`).
  - **Extended to native-ingested plugin names (#104).** The one untrusted source
    left out of the by-type pass above — the plugin name an adapter's
    `PluginIngester` reports, which `status`/`doctor` print in their "undeclared
    native plugins" notes — is now carried as `untrusted.Text` end to end
    (`adapter.NativePlugin.Name` → `undeclaredNativePlugins` → the print sites,
    joined via the new `untrusted.Join`). The previous per-site `ui.Sanitize`
    wrappers at those sinks are gone (the type sanitizes by construction), and a
    new `TestUntrustedFieldGuard` over `adapter.NativePlugin` fails the build if a
    future native-config string ships unclassified. No behavior change — the
    notes still strip terminal escapes from a hostile plugin name.
  - **`ValidateComponentID` now rejects control / deceptive-format runes.** The
    single dest→source write boundary (reached by `import`/`reconcile` with
    ids taken from a native config) previously rejected only path separators,
    traversal, and degenerate ids — a separator-free name carrying an ESC byte or
    a Trojan-Source bidi override (e.g. `good␛[31m`) passed, becoming a
    pathological filename and leaking raw bytes when later echoed in an
    `import` skip diagnostic. It now also rejects any id the display sanitizer
    would alter (tied to `untrusted.Sanitize`'s rune set), so such an id is
    skipped with a sanitized warning and never installed. Legitimate non-ASCII
    ids (e.g. `naïve`, `日本語`) are unaffected.

- **`explain` describes every component kind a plugin hosts, not just MCP +
  commands.** Each agent row's count tail previously read `N mcp · N commands`
  even for a plugin that ships only skills, subagents, hooks, or an LSP server —
  so an LSP-only plugin reported a misleading `0 mcp · 0 commands`. The tail now
  lists every non-zero kind it hosts for that agent (`mcp`, `commands`, `skills`,
  `subagents`, `hooks`, `lsp`), e.g. `1 mcp · 2 skills · 1 lsp`; a plugin that
  contributes nothing to an agent reads `no components`. The counts describe the
  inventory (what the plugin hosts, MCP/LSP honouring each server's
  `enabled`/`agents` targeting) — a hosted component the agent cannot translate is
  still counted and reported under `(N skipped)`. `explain --json` rows gain the
  matching `skills`, `subagents`, `hooks`, and `lsp` integer fields alongside the
  existing `mcp`/`commands` (`render.PluginRow`). Coverage now derives `partial`
  vs `none` from whether the adapter actually rendered anything for the agent
  (rather than from `mcp`/`commands` alone), fixing a latent case where a plugin
  whose skills rendered but whose hook was skipped was mislabeled `none`.

- **`explain` itemizes what each agent couldn't fully translate, split into
  "reduced" vs "dropped".** A `◐ partial` row is no longer a dead-end `(N
  skipped)` tally that reads as if N whole components were discarded. The
  trailing note now breaks down by kind — `(N reduced · M dropped)` — and each
  part is listed beneath the agent row under a framing header
  (`→ <agent> couldn't fully translate — reduced = rendered without some fields;
  dropped = not emitted:`), tagged `reduced` (the component still rendered, just
  without fields the agent has no home for — e.g. a subagent's Claude-only
  `tools`/`color`) or `dropped` (the whole component had no native target and was
  not emitted — e.g. an LSP server on an agent with no LSP concept), with the
  reason. The label names the component kind plainly (`subagent reviewer`). The
  structured surface gains a `skipDetails` array (`{component, name, reason,
  kind}`) on every `explain --json` row. The translation report carries the
  detail end-to-end (`render.PluginRow.SkipDetails`) rather than collapsing skips
  to a bare count, and the counts/skips are scoped to the named plugin (see the
  `explain` fix below).

- **Skip severity is a typed field, not a `-frontmatter` string convention
  (#98).** The reduced-vs-dropped distinction `explain` shows was previously
  derived at the presentation layer by string-matching a `-frontmatter` suffix on
  the skip's `component` (e.g. `subagent-frontmatter`) — load-bearing for
  user-facing output yet enforced only by an undocumented naming convention with
  no compile-time guard. It is now a typed `adapter.Skip.Kind`
  (`SkipDropped`/`SkipReduced`) the adapter sets at each skip site, carried
  through `render.SkipDetail` to `explain`. `internal/cli/explain.go` reads
  `Kind` directly (the `isReducedSkip`/`HasSuffix` heuristic is gone), and a new
  reflective exhaustiveness guard (`TestEveryAdapterClassifiesSkips`) renders
  every registered adapter at both scopes and fails if any skip ships with `Kind`
  unset — so a new adapter or skip site can no longer silently misclassify.
  - **Breaking change to `explain --json`.** Each `skipDetails` entry gains an
    explicit `kind` field (`"reduced"`/`"dropped"`), and `component` is now the
    plain kind (`subagent`, `command`, …) — it no longer carries the
    `-frontmatter` suffix machine consumers had to parse. Read `kind` instead.
- **`status` now collapses skill directories and gains `--agents` / `--verbose`.**
  A skill bundling scripts/references/assets used to print one row per file, so a
  handful of asset-heavy skills could push `status` past a thousand lines. By
  default each skill directory now renders as a single row — the skill dir, its
  *most-severe* drift class (so a drifted `SKILL.md` hiding among clean assets
  still shows red), and a faint `SKILL.md + N files` count with a per-class
  breakdown when the bundled files don't all share one class. A directory is
  recognized as a skill by an actual `…/skills/<name>/SKILL.md` (not a bare
  `skills` path segment), so an unrelated ancestor dir named `skills` can't sweep
  non-skill files into a bogus group. Pass `-v`/`--verbose` to expand every skill
  back to one row per file (the previous view). A new `--agents <list>` flag
  (comma-separated allowlist, `*` = all enabled — matching `mcp add --agents`)
  scopes the report to specific agents; orphaned-state warnings still consider the
  full enabled set so narrowing never mislabels a deselected agent as an orphan.
  `status --json` is
  unchanged and **never collapsed** — it still carries every tracked file, so the
  machine contract is stable.
- **`agentsync version` is now a subcommand alias for `agentsync --version`.**
  The subcommand delegates to cobra's own version renderer — it re-dispatches
  the root with its `--version` flag set, so the same precompiled,
  `FuncMap`-bearing template function renders the same data against the same
  flag-owning (root) command. Its output is therefore byte-identical to
  `--version` by construction and can never drift, even if the version template
  later uses a cobra template function such as `rpad` or `trim`. (Previously the
  subcommand hand-parsed the template with a bare `text/template` that had no
  `FuncMap`, so such an edit would have silently broken `agentsync version`
  while `--version` kept working; a raw-byte parity test now pins the guarantee.)

## [0.7.3] — 2026-06-20

### Fixed

- **Chocolatey packages now build reproducibly across runners (fixes the v0.7.1
  verification failure).** The release pipeline builds the Windows `.zip` twice —
  once on the Linux job that uploads it to the GitHub Release, and again on the
  windows-latest `chocolatey` job, which bakes the sha256 of its *local* rebuild
  into `chocolateyinstall.ps1`. choco downloads the Release `.zip` and checks it
  against that sha256, so the two builds must be byte-identical; they weren't, and
  v0.7.1 failed automated verification with `Checksum … did not meet …`. The two
  builds diverged on three non-deterministic inputs, now all pinned to the commit:
  `-trimpath` strips the per-runner GOPATH/module-cache path baked into the binary,
  `-X main.date` is set from `{{ .CommitDate }}` instead of the wall-clock build
  time, and `builds[].mod_timestamp` plus the archive's `files[].info.mtime` pin
  every in-archive mtime (binary and bundled `LICENSE`/`README`) to the commit so
  `actions/checkout`'s per-runner timestamps no longer change the `.zip` bytes.
  Re-cutting the release regenerates the Release archive and the Chocolatey package
  together, so their checksums agree.
- **Reproducible builds, take two: pin archive file-modes and the GoReleaser
  version (the v0.7.2 verification fix).** The first pass pinned the build's
  embedded path/date and the bundled files' mtime — enough to make the archive
  reproducible *on one host*, but not across the Linux Release job and the
  `windows-latest` Chocolatey job. A `.zip` also records each entry's Unix mode
  (`os.FileInfo.Mode()`): `0755`/`0644` on Linux vs `~0666` (no exec bit) on
  Windows. That lone metadata difference made the two builds' archives disagree, so
  v0.7.2 *passed validation but failed verification* on a checksum mismatch between
  the Linux Release archive and the Windows-built `.nupkg`. The archive now pins
  every in-archive Unix mode (`builds_info.mode` for the binary, `files[].info.mode`
  for the bundled docs), making the `.zip` byte-identical regardless of build-host
  OS (the binary itself was already host-independent via `-trimpath` + a pinned Go
  toolchain). Both release jobs additionally pin GoReleaser to an exact version
  (`2.16.0`) instead of `latest`, so a version skew between the two independently
  run jobs — or a later single-job re-run — can't reintroduce the divergence.
  Verified locally: forcing the archive modes to `0666` changes the `.zip` hash,
  while with the modes pinned the hash is invariant to on-disk permissions.

## [0.7.2] — 2026-06-19

### Fixed

- Release/packaging fix superseded by [0.7.3]; the checksum-mismatch story
  (archive file-mode + GoReleaser-version pinning) is documented in full under
  [0.7.3]. Tagged release: `v0.7.2`.

## [0.7.1] — 2026-06-19

### Fixed

- Release/packaging fix superseded by [0.7.3]; see [0.7.3] for the full
  reproducible-build / checksum-mismatch narrative. Tagged release: `v0.7.1`.

## [0.1.0] — 2026-06-05

The first public release (beta). Functional end-to-end (green under
`just test-release`): Claude Code, OpenCode, and Codex adapters plus the full
apply / status / diff / reconcile / import pipeline and an age-encrypted secret
vault. Distributed as GitHub Release binaries (linux/darwin/windows ×
amd64/arm64), checksums, deb/rpm packages, and a Homebrew cask
(`brew tap spxrogers/tap && brew install agentsync`). Remaining package-manager
channels and a few documented trade-offs are tracked in
[issue #13](https://github.com/spxrogers/agentsync/issues/13); see also
[Known limits](README.md#known-limits).

### Added

- **Project scope for `init` and `import` + a project _source tree_.** Project
  config now lives in a `<root>/.agentsync/` directory with the same on-disk
  layout as `~/.agentsync/` (so every loader/writer/capture path works unchanged
  by pointing `home` at it), replacing the M5 single-file `.agentsync.toml`
  marker. `agentsync init --scope project [--project <path>]` scaffolds the tree
  (defaults to the current directory); `agentsync import <agent> --scope project
  [--project <path>]` captures native **project-scope** config (e.g.
  `<root>/.claude/`) into it, seeding state with the project scope + root so the
  next apply doesn't foreign-collide. `apply`/`status`/`diff`/`reconcile` load the
  tree and overlay it on the user canonical (`project.Merge`): entries merge by
  id/name (project wins), project memory is appended, an empty project `[agents]`
  inherits the user's enabled agents, and a project `plugins/<id>.toml` with
  `disabled = true` suppresses that plugin's components in the repo. Plugin import
  stays user-scope (plugins are a user-scope concept across the harnesses).
- **Project scope is an explicit opt-in.** Commands default to **user** scope.
  Project scope requires `--scope project` (walks up from cwd to the tree) or
  `--project <path>`. Running with no scope *inside* a project tree is ambiguous,
  so agentsync prompts project-vs-user (no default); a new global `--no-input`
  flag — and a non-TTY stdin — makes it fail closed instead. `--scope
  project`/`--project` with no `.agentsync/` tree is a hard error pointing at
  `init --scope project`, never a silent downgrade to user scope.
- **`verify --scope project` / `--project <path>`.** `verify` now takes the same
  scope flags as `status`/`diff`/`apply`, so a project `.agentsync/` tree can be
  schema-linted and have its `${secret:…}`/`${env:…}` references validated.
  Project references resolve against the inherited user secrets backend exactly
  as `apply` does (so the two never disagree), and the existing missing-home /
  half-initialized guards now report the scope-appropriate `init` command.
  Default stays user scope.

### Fixed

- **Adapters fail loud on a project-scope call with no project root.** Every
  adapter's `ResolvePaths` falls through to *user*-scope paths when the project
  root is empty, so a `(ScopeProject, "")` reaching an adapter would silently
  write the project overlay into the user's global config (or read it from
  there). Every scope-resolving adapter method — `Render` and `Ingest` (claude,
  opencode, codex), plus `IngestPlugins` (claude, codex) — now calls the shared
  `adapter.RequireProjectRoot` first and returns `ErrProjectRootRequired`
  instead. The CLI already guarantees a non-empty root for project scope, so
  this is defense-in-depth against a future or non-CLI caller — turning a silent
  wrong-scope I/O into a loud error.
- **Claude project-scope MCP servers now target `<root>/.mcp.json`, not
  `<root>/.claude/settings.json`.** Per the upstream Claude Code MCP-scope docs,
  project-scope servers live in a repo-root `.mcp.json` (the file `claude mcp add
  --scope project` writes and the team checks in); `settings.json` holds
  hooks/LSP/permissions, never project MCP. The Claude adapter previously both
  rendered and ingested project MCP at `settings.json`, so `apply --scope
  project` wrote to a file Claude Code does not read project MCP from, and
  `import claude:mcp --scope project` missed servers added via Claude's own
  `--scope project` flow. Render and ingest now use `.mcp.json` at project scope
  (top-level `mcpServers`, `merge-json-keys` so a hand-authored file's foreign
  keys and unmodeled per-server fields like `timeout` survive); user scope
  (`~/.claude.json`) is unchanged. If a prior version already wrote project MCP
  into a project `settings.json`, the next `apply --scope project` of an
  in-place upgrade removes that stale `mcpServers` block automatically via
  orphan-key cleanup — agentsync still owns those keys in state, and foreign
  keys in the file are preserved; if the state was not carried over (e.g. a
  fresh clone), remove the block by hand.
- **OpenCode project-scope config now targets `<root>/opencode.json`, not
  `<root>/.opencode/opencode.json`.** Per the upstream OpenCode config docs, a
  repo's JSON config is `opencode.json` at the project **root**; OpenCode does
  not read `.opencode/opencode.json` (the `.opencode/` directory holds only the
  `agents/`/`commands/`/`skills/` subdirs, which were already correct). The
  adapter previously rendered/ingested project-scope MCP servers (the only
  structured config it writes) at `.opencode/opencode.json`, so `apply --scope
  project` wrote them where OpenCode never looks and `import`/`reconcile --scope
  project` read the wrong file. Same class of bug as the Claude project-MCP fix
  above; user scope (`~/.config/opencode/opencode.json`) is unchanged.
- **`apply --scope project` now renders only project-scope items.** Previously
  `project.Merge` never populated `Canonical.Project`, so all three adapter
  `Render` methods wrote the full merged canonical (user + project items) into
  the project directory. `apply --scope project` now correctly writes only the
  project-overlay items (`<root>/.agentsync/` content) to `<root>/.claude/`,
  mirroring how Claude Code, OpenCode, and Codex each read user-scope config
  from their own global directories at runtime. This also fixes `status`,
  `diff`, and `reconcile` at project scope, which use the same render path.

### Changed

- **`apply --dry-run` now distinguishes already-synced destinations from pending
  writes.** Previously every planned op was listed as `→ write`, even when the
  destination already held exactly the bytes apply would write — so a dry-run on
  an in-sync tree read like a wall of pending work. The preview now runs the real
  apply pipeline through non-writing writers (so each merge is performed against
  the on-disk destination, matching the eventual apply exactly) and labels each op
  `✓ synced` or `→ write`; the summary line gains a `— N to write, M already
  synced` tally. Nothing is written, exactly as before.
- **The M5 single-file `.agentsync.toml` project marker is retired** (one project
  schema, not two). It is no longer read; `agentsync` surfaces a migration error
  pointing at `init --scope project` when it finds one. The marker's
  `memory.import` field is dropped — author project memory directly in
  `<root>/.agentsync/memory/AGENTS.md` (or `import … --scope project`), which also
  removes the committed-marker memory-import path-traversal surface entirely.

### Added (continued)

- **`adapter.WarnEmitter` extension interface** — formalises the optional
  `SetStderr(io.Writer)` setter the claude / opencode / codex adapters
  added alongside the `import` styling work, so future Ingest-using
  commands can redirect adapter warnings through the styled `ui.WarnWriter`
  (bold-yellow `⚠️ warning:`) without duplicating the type-assertion
  boilerplate. Named for the implementor (a *source* of warnings that
  accepts a sink, mirroring `PluginIngester`), not the parameter.
  The contract has four load-bearing pieces, all tested and break-verified:
  - **`SetStderr(nil)` resets to the default (`os.Stderr`)** and MUST NOT
    panic. Pinned by per-adapter `TestSetStderr_NilResetsToDefault`
    tests (claude / opencode / codex) that capture `os.Stderr` via a
    pipe and assert the warning actually lands there — a faulty
    `SetStderr(nil)` routing to `io.Discard` would not pass.
  - **Configure stderr BEFORE Ingest.** Adapters snapshot the writer at
    Ingest entry, so dynamic switching mid-Ingest is ignored. Documented
    on the interface.
  - **Compile-pin against the interface.** Each adapter's identity test
    carries `var _ adapter.WarnEmitter = a`; dropping the method fails
    the build, not a runtime no-op.
  - **Caller owns lifetime via a restore handle.** `ui.WarnWriter.RouteTo`
    is now `func (s *WarnWriter) RouteTo(a any) func()` — it wires the
    writer immediately and returns a restore closure. Idiomatic
    `defer warnW.RouteTo(a)()` evaluates the inner `RouteTo(a)` now and
    defers the returned restore (which calls `SetStderr(nil)`).
    `ui.WarnWriter` gains a `Flush()` method;
    `internal/cli/import.go` pairs `defer warnW.Flush()` with the
    routed-restore so partial lines drain on return.
- **Routing primitive safety guards.**
  `ui.WarnWriter.RouteTo` is a silent no-op for: untyped-`nil`,
  typed-nil pointers (caught via `reflect.IsNil` so the type-assert
  doesn't lead to a `SetStderr` deref panic), and any value whose
  dynamic type doesn't implement the setter. The restore closure is
  always callable, so `defer warnW.RouteTo(a)()` is safe regardless of
  what `a` is. Behaviour pinned by `TestWarnWriter_RouteTo` in
  `internal/ui/routeto_test.go` (happy path + nil + typed-nil +
  non-implementor + repeated-restore).
- **End-to-end same-line regex anchor.** `TestImport_StyledAdapterWarnings`
  is now table-driven across claude AND opencode and asserts that the
  styled `⚠️ warning:` prefix appears on the SAME line as the
  adapter-specific `"frontmatter is not strict YAML"` phrase. The
  prior assertion looked for the styled prefix anywhere in the
  output, which the CLI's own `importIO.warn` could satisfy; the
  same-line anchor catches both the os.Stderr-fallback and the
  WarnWriter-bypass regressions. Break-verified.
- `ui.WarnWriter` is documented as not safe for concurrent use (one
  writer per command invocation, the existing pattern).
- **`captureOsStderr` cleanup is panic-safe.** The per-adapter
  `os.Stderr`-pipe-swap helpers used by the nil-reset tests now
  defer `w.Close()` and `os.Stderr = orig` BEFORE invoking `fn`, so
  a `t.Fatalf` inside `fn` (which calls `runtime.Goexit`) no longer
  leaks the read goroutine or leaves `os.Stderr` swapped for later
  tests in the same package. Each helper also carries a "do not
  `t.Parallel`" comment naming the global-state hazard.
- **`Flush()` coverage.** New `TestWarnWriter_Flush` in
  `internal/ui/routeto_test.go` covers the partial-line drain
  path the `importRun` defer relies on: a `"warning: "` Write with
  no trailing newline is asserted to (a) sit in the buffer until
  Flush is called, (b) get styled with the bold-yellow prefix on
  Flush, and (c) a non-warning partial drains verbatim. Without
  this, Flush could silently become a no-op since every current
  emitter terminates with `\n`.
- **Mid-Ingest snapshot contract pinned per-adapter.**
  `TestSetStderr_SnapshotAtIngestEntry` in each implementing
  adapter's package (claude / opencode / codex) uses a custom
  writer that, on first emission, swaps the adapter's `Stderr` to
  a sibling buffer; the snapshot taken at Ingest entry (`warn :=
  a.stderr()`) means subsequent warnings still land in the
  original sink. A future refactor in ANY adapter's Ingest that
  re-reads `a.stderr()` per warning would silently violate the
  documented "configure stderr BEFORE Ingest" promise on
  `WarnEmitter`; the per-adapter tests catch it independently
  rather than relying on a single lead test + grep.
- **Shared `internal/adapter/adaptertest` test helper package.**
  Centralises `CaptureOsStderr(t, fn)` (the `os.Stderr`-pipe-swap
  helper with the defer-cleanup invariants that round-3 demonstrated
  must hold) and `SwapOnFirstWriteBuffer` (the writer that fires a
  callback on first write, used by the snapshot tests). Follows the
  stdlib `httptest` precedent: an ordinary package that takes
  `*testing.T`. Eliminates three byte-identical 30-line copies
  across the adapter test packages — a maintenance trap that
  round-3 surfaced when the deferred-cleanup fix had to be applied
  three times in lockstep.
- **`explain` accepts multiple plugins, `--all`, and `--list`** —
  `agentsync explain` now takes a space-separated list of plugin ids
  (`agentsync explain notion@official superpowers@obra`), and gains two flags:
  `--all` renders coverage for every installed plugin, and `--list` prints just
  the set of installed ids (a quick reminder of what you can pass without
  jumping to `agentsync plugin list`). The text output is also rendered through
  the styled UI: a bold header summarises how many plugins are being explained,
  each plugin gets a `▸` section header (with version + a yellow `(disabled)`
  marker when applicable), and per-agent rows use the same semantic glyph +
  color vocabulary as `apply` and the capability matrix
  (`✓ full` / `◐ partial` / `✗ none`). `--json` is unchanged in shape for the
  default rows mode; with `--list` it emits a `plugins` array.
- **Styled CLI output and a `--color` flag** — `agentsync status`, `diff`,
  `doctor`, `apply`, and `import` now render through a single presentation layer
  (`internal/ui`) with a curated semantic palette (green=synced, cyan=pending,
  red=drift, yellow=needs-decision) and the same `✓ ◐ ✗ → •` glyph vocabulary
  the capability matrix already uses. `status` gains a one-line summary footer
  ("`5 clean · 2 drift`"). Color is TTY-gated by default — `--color=auto`
  enables it only when stdout is a terminal and `NO_COLOR` is unset, while
  `--color=always|never` overrides. Glyphs are unconditional Unicode (matching
  the existing report output); piped output is byte-stable and never leaks raw
  ANSI. The `apply` translation report is rendered through the same Printer:
  bold "plugin:" labels, semantic color on the coverage marks (green=full,
  yellow=partial, red=none), faint trailing counts. With color disabled the
  output is byte-identical to before, so existing fixtures hold.
- **`import` joins the styled-output set** — per-item lines carry a green `✓`
  (real) or cyan `→` (dry-run) prefix; the full-agent walk groups items under
  faint section headers (`mcp servers`, `skills`, …) printed lazily so an empty
  component is invisible; the summary line is bold (`imported N items from
  claude`) with a faint per-component breakdown underneath. Every `warning:`
  label — whether emitted by `import` itself, by an adapter's `Ingest` (the
  lenient-YAML notices that used to lead the screen as plain text), or by
  `capture`'s re-reference path — is restyled to a bold-yellow `⚠️ warning:`
  by a single line-buffered writer (`ui.WarnWriter`) wrapping stderr; adapters
  pick it up via a new optional `SetStderr(io.Writer)` setter so the routing
  is invisible to non-CLI callers. Wording is preserved (`imported X` /
  `would import X`, `warning: …`) so scripts grepping the output keep working.
- **`status` explains its drift classes inline** — the formatted dashboard
  now prints a brief "What `apply` will do:" legend after the summary footer,
  with one action-focused line per drift class actually present (`new` → will
  be created; `pending` → will be updated to match source; `drift` → will be
  overwritten, use `reconcile` to keep the dest edit; `foreign-collision` →
  will be backed up and overwritten; etc.). Each line uses the same glyph and
  color as the per-item rows above so you can scan from a row to its meaning
  by shape and color. Suppressed entirely when only `clean` items exist (the
  word is self-evident) and excluded from `--json` (the class field is the
  machine contract).
- **Spinners on slow network ops** — `agentsync update` and `agentsync
  marketplace add` animate a braille-frame spinner on stderr while
  marketplace fetches and plugin-manifest pulls are in flight. The spinner is
  a complete no-op on a non-terminal (CI logs, piped stderr, captured-output
  tests) — no animation, no static fallback line — so byte-stable fixtures
  stay byte-stable and grep'd output stays clean; the success line each
  command already prints carries the result.
- **`status --json` and `diff --json`** — emit machine-readable structured
  output (per-agent drift items + summary tally; per-hunk source/dest with
  pointer) for CI gates, dashboards, and scripts. Advisory diagnostics still
  go to stderr so the JSON payload stays cleanly parseable.  `diff --json`
  reuses the same redaction the formatted diff does, so resolved secrets are
  masked in both modes.
- **Canonical source model** in `~/.agentsync/` — hand-editable TOML + markdown
  for agents, MCP servers, marketplaces, plugins, memory, and skills.
- **Full Agent Skills directory support** — a skill is treated as a *directory*
  per the [Agent Skills](https://agentskills.io) spec, not just its `SKILL.md`.
  Bundled `scripts/`, `references/`, `assets/`, and nested files are carried
  verbatim (binary included, executable bit preserved) end-to-end: across the
  canonical loader/writer, every adapter's render + ingest (Claude, OpenCode,
  Codex), plugin/marketplace projection, and `apply`/`import`/`reconcile`.
  Removing a skill (or one bundled file) from the source reclaims it from the
  destination on the next `apply` — a hand-edited orphan is backed up first, and
  now-empty skill directories are pruned up to the skills root.
- **Unmodeled native MCP/LSP fields preserved (`Extra`)** — native server fields
  agentsync doesn't model (e.g. `timeout`, `disabled`, `cwd`) are captured into a
  passthrough `[server.extra]` table on import/reconcile and rendered back
  verbatim, instead of being silently dropped (and then erased from the
  destination on the next apply). `Extra` is verbatim only — values are never
  secret-resolved (`${secret:…}` there is written literally) and never visited by
  the secret walker; the capture leak backstop scans it and refuses any write that
  would persist a live secret value through it.
- **Bidirectional memory fragments** — `apply` wraps each inlined fragment in
  HTML-comment boundary markers (`<!-- agentsync:fragment <name> -->` …) in the
  native memory file, so `import`/`reconcile` can **reverse** the expansion: a
  native edit inside a fragment block is captured back into that fragment file
  with the `@import` structure preserved, instead of flattening `AGENTS.md` and
  orphaning the fragments. When markers are absent (a fragment containing the
  marker token disables them) or hand-mangled into an unbalanced/ambiguous state,
  the write-back is refused rather than guessed; reverse paths are
  traversal-checked. Drift still surfaces in `status`/`diff`.
- **Claude Code adapter** — full support for all seven components (MCP, memory,
  skill, subagent, command, hook, LSP) with per-key merge into `~/.claude.json`
  and `settings.json` that preserves foreign keys.
- **OpenCode adapter** — MCP, memory, skills, subagents, and commands via JSONC
  round-trip. Hooks and LSP are skipped with a warning.
- **Codex CLI adapter** — MCP, memory, skills, subagents, slash commands, hooks,
  and plugin import. MCP servers (`[mcp_servers.*]`) and hooks (inline `[hooks.*]`)
  both merge into the TOML `~/.codex/config.toml` via a new `merge-toml-keys`
  strategy that preserves the user's foreign keys (`model`, `sandbox_mode`,
  `[plugins.*]`, …) — config.toml is the adapter's single key-merge file. Memory
  lands at `~/.codex/AGENTS.md` and skills in the shared `~/.agents/skills/` (both
  full-fidelity). Subagents project to Codex's TOML agent format (dropping the
  unsupported `tools`/`color`), slash commands to global-only custom prompts
  (`~/.codex/prompts/`), and hooks to the events Codex recognizes — every
  projection loss is reported in the apply translation report. Implements
  `PluginIngester`: `import codex:plugin` captures the
  `[plugins."<name>@<source>"]` enable-state. `agent add codex` now works (no
  longer gated behind `AGENTSYNC_ALLOW_UNIMPLEMENTED`); Cursor remains the only
  no-op adapter. The `merge-toml-keys` strategy and the CLI's shared
  `render.IsKeyMerge` predicate + TOML-aware dest decoder also extend
  `status`/`diff`/`reconcile`/`import` to TOML destinations.
- **Bidirectional drift** — a 3-way classifier (9 cases) at file and JSON-pointer
  granularity, surfaced via `status`/`diff` and resolved via an interactive
  `reconcile` loop with bulk hotkeys and `--auto-*` flags.
- **Marketplaces & plugins** — all five plugin sources (relative, github, url,
  git-subdir, npm), `strict`-mode conflict policy, `${CLAUDE_PLUGIN_ROOT}`
  substitution, per-component projection, translation report, manifest-SHA
  pinning, and update modes. `marketplace add` treats every marketplace
  identically — no name is reserved. `marketplace remove` errors on an
  unregistered or invalid name and points at `marketplace list`.
- **Project-local overlays** — `.agentsync.toml` walk-up discovery, overlay
  merge, project-scope state tracking.
- **age-encrypted secrets** — `${secret:…}`/`${env:…}` resolution at apply time,
  `secrets {edit,get,set}`, redaction in `diff`. Resolved cleartext is never
  captured back into the source (compile-, value-, and lint-enforced).
- **CLI**: `init`, `agent`, `doctor`, `verify`, `apply`, `status`, `diff`,
  `reconcile`, `import`, `mcp`, `plugin`, `marketplace`, `update`, `secrets`,
  `explain`.
- **Bulk import** — the `import` selector now widens as parts are dropped:
  `import <agent>` captures the agent's full native config, `<agent>:<component>`
  captures every entry of a component, and the original `<agent>:<component>:<name>`
  still captures one item. Empty scopes report a notice and exit cleanly.
  `import --dry-run` previews which source files an import would write without
  touching `~/.agentsync/`.
  Plugin import resolves a marketplace from agentsync's own registered
  marketplaces first, then the agent's native config, so `marketplace add` then
  re-import captures plugins from any marketplace (including Claude built-ins
  such as `claude-plugins-official`).
- **Plugin import** — `import` now captures the agent's installed plugins and
  their marketplaces (Claude only in v1) via the new `plugin` component, so a
  full `import claude` reproduces a plugin-heavy setup in one pass. It reads
  Claude's native `enabledPlugins` / `extraKnownMarketplaces` and re-fetches each
  marketplace + plugin into the agentsync cache (pinning a manifest SHA),
  producing the same artifacts as `marketplace add` + `plugin install` — so a
  real plugin import needs network access. Plugins from an unregistered or
  auto-available marketplace (e.g. the built-in `claude-plugins-official`, which
  Claude does not list in `extraKnownMarketplaces`) are reported and skipped.
- **Plugin nudge in `status` / `doctor`** — both now surface plugins installed
  natively in an agent (Claude in v1) that aren't declared in your source,
  pointing at `import <agent>:plugin`. Informational only: agentsync still treats
  natively-installed plugins as foreign-managed, so this never blocks an apply or
  auto-imports anything.
- **Safety primitives** — two-phase atomic writes, an apply lock, first-apply
  foreign-collision backups, symlinked-destination refusal, and a schema-versioned
  state file with portable (`${HOME}`-relative) keys.
- **Documentation set** — user guide, concepts, architecture, capability matrix,
  and component map under [`docs/`](docs/).
- **Documentation website** (`website/`) — an Astro Starlight site published to
  [agentsync.cc](https://agentsync.cc) via GitHub Pages. Expands the user guide
  into task-shaped getting-started, guides, recipes, and reference sections with
  full-text search and rendered Mermaid diagrams. The four contract docs
  (concepts, architecture, components, capability matrix) are mirrored from
  `docs/*.md` at build time so the site can never drift from the in-repo source.

### Changed

- **Capability matrix** — Cursor's planned projection now covers **skills**
  (✓ native, `.cursor/skills/`) and **subagents** (◐ projected, `.cursor/agents/`),
  reflecting Cursor's new skill and subagent support (both previously skipped). No
  code change — Cursor remains a no-op adapter until it's implemented. The matrices
  also drop the per-agent version suffixes (the target version isn't material) and
  footnote Codex/Cursor as planned.
- **Capability matrix** — corrected Codex **skills** from ◐ to ✓: Codex reads the
  same `SKILL.md` format (no translation loss), matching the design spec. (Codex
  skills are on by default — they install under `~/.agents/skills/`; there is no
  `[features] skills = true` master flag.)
- **Capability matrix** — full sweep against each agent's current docs:
  Codex/Cursor **MCP** and Codex **memory** are now ✓ (full-fidelity transforms);
  Codex and Cursor gained real **slash commands** (◐ — Codex via deprecated,
  global-only custom prompts; Cursor via frontmatter-less `.cursor/commands/`); and
  Codex now mirrors Claude's declarative **hooks** JSON (◐ — ~11-event subset)
  while Cursor added a declarative `.cursor/hooks.json` (◐ — event remap). Every ◐
  cell now spells out its specific projection loss.
- **Docs site** — folded the standalone "Multi-agent fan-out" guide (it duplicated
  the matrix) into the capability matrix page, which gains a "Reading the report"
  section.

### Fixed

- **`diff` no longer leaks raw ANSI into pipes** — the previous implementation
  called `diffmatchpatch.DiffPrettyText` unconditionally, so `agentsync diff |
  grep` (or any redirect to a file) accumulated `\x1b[31m…\x1b[0m` escape
  sequences instead of readable text. The new color-aware renderer emits ANSI
  only when stdout is a TTY (and `NO_COLOR` is unset), and falls back to
  `[-…-]` / `{+…+}` text markers in plain mode so a piped diff is still legible
  and `grep`-friendly.
- **`import` no longer silently drops skills/subagents/commands with loose
  frontmatter** — a component `.md` whose `description:` carried an unquoted
  `Triggers on: X, Y` colon-space sequence broke `sigs.k8s.io/yaml` ("mapping
  values are not allowed in this context"), and the `continue` in every
  adapter's Ingest dropped the whole component without warning. The parser now
  falls back to a line-oriented "key: rest-of-line" read on strict-YAML failure,
  matching how Claude Code itself reads these files; the canonical write-back
  re-emits a quoted, strict-YAML form, so the next `apply` round-trips cleanly.
  A `warning: ... frontmatter is not strict YAML; parsed leniently` line is
  printed to stderr for each lenient parse so the source of the leak is visible.
  A structurally-broken file (e.g. unterminated fence) is still skipped, but now
  with an explicit `warning: skipping skill "<name>": <reason>` instead of a
  silent drop.
- **Plugin skill discovery now caps depth + leaf count** — `discoverSkillDirs`
  recursed unboundedly looking for `SKILL.md`, which was fine for real plugins
  (≤ 2 levels, few dozen skills at most) but left the host exposed to a
  malformed or hostile plugin tarball: a deeply-nested tree could stretch the
  goroutine stack and a wide tree could balloon the canonical projection in
  memory. Two sanity caps now fail loudly with a deliberately disparaging
  banner: `maxSkillDepth = 32` (refuse a `SKILL.md` more than 32 directories
  deep) and `maxSkillLeaves = 256` (refuse a plugin shipping more than 256
  skills). Both wrap an `errSkillSanityCap` sentinel so the convention-
  discovery caller — which normally `slog.Warn`-and-skips transient filesystem
  errors — propagates a cap violation instead of swallowing it.
- **Post-import warning scoped to in-scope sections, no false collision claim**
  — the post-import "unimported destination items" warning walked *every*
  second-level pointer in the dest file and predicted each one would "trigger
  ForeignCollision on next apply" — including pointers under top-level sections
  agentsync doesn't model at all (Claude Code's `skillUsage`, `tipsHistory`,
  `oauthAccount`, `cachedGrowthBookFeatures`, runtime/telemetry state). For
  merge-keys ops the prediction was factually wrong: the writer's per-pointer
  OwnedKeys check fires only on keys the op claims, so foreign keys are
  preserved untouched — they cannot collide. The warning now lists only
  pointers under sections the canonical actually renders, and reads as a note
  with accurate wording (no false collision claim, no unactionable "re-run
  import" hint when the user just did).
- **Plugins pinned to an older commit sha now fetch** — the git fetcher
  shallow-cloned (`depth 1`) the branch tip, so a marketplace entry pinning a
  `sha` that lagged the head failed to check it out with `object not found`
  (seen on `chrome-devtools-mcp`). A pinned sha now triggers a full clone so the
  commit object is present.
- **Plugins that group skills a level deeper now import** — plugin skill
  discovery only scanned one level (`skills/<name>/SKILL.md`), so a plugin that
  nests skills under a grouping directory (e.g. `notion`'s
  `skills/notion/<category>/SKILL.md`) hit a grouping dir with no `SKILL.md` and
  hard-failed the whole projection with `is a directory` — which bricked
  `apply`/`status`/`diff`/`import` for that plugin, not just a warning. Discovery
  now recurses to the leaf skills, and a `SKILL.md`-less directory is skipped
  rather than read as a file.
- **In-tree plugin symlinks no longer reject the whole plugin** — the git
  fetcher refused *any* committed symlink, so a plugin shipping a harmless
  in-tree link (e.g. `superpowers`' `AGENTS.md -> CLAUDE.md`) was skipped
  entirely. A symlink whose resolved target stays inside the plugin tree is now
  allowed; only one escaping the tree is refused (fail-closed on an unresolvable
  link). The plugin content pin hashes such a symlink by its target path, so a
  swapped target is still detected. The npm/relative fetchers still reject all
  symlinks (their copy mechanism cannot preserve a link).
- **`agent disable --purge` validates the name; `doctor` names a half-init** —
  `disable <bogus> --purge` reported a misleading "purged 0 files" success for
  any string; it now rejects an unknown agent like the other subcommands (a
  removed-but-valid agent still purges). `doctor` now flags a home missing
  `agentsync.toml` instead of calling the schema "ok", matching `verify`.
- **`apply` is a true no-op when nothing changed** — the writer now skips the
  atomic write when a destination already holds the exact bytes apply would
  write, so a clean re-apply no longer churns file mtimes (which misled
  mtime-watching tooling and made a no-op look like real work). `apply` reports
  `up to date: N ops, no changes` instead of `applied: N ops` in that case.
- **`verify` rejects a half-initialized home** — a home with component files but
  no `agentsync.toml` (an authoring command run before `init`) reported a false
  `ok: schema valid`; `verify` now requires the config marker.
- **`agent add`/`disable` preserve section spacing** — regenerating the
  `[agents]` block dropped the blank line before the next section, collapsing the
  file's formatting over repeated edits; a single separator is now kept.
- **Shared MCP/LSP write-back no longer silently last-writer-wins** — a server
  fanned out to multiple agents (`agents = ["*"]`) and edited *differently* in
  each native config produced two `reconcile` write-back items targeting one
  source file; the second silently clobbered the first and left the first agent
  stuck in `conflict`. The run now detects the divergence, keeps the first
  write, refuses the conflicting one with a clear message, and exits non-zero;
  identical edits across agents still write cleanly.
- **Deregistered agents no longer orphan native config silently** — after
  `agent remove` (or `agent disable` without `--purge`), the agent's rendered
  native config and state keys lingered with no diagnostic (`status`/`apply`
  only iterate enabled agents), and `disable --purge` was unreachable once the
  agent was deregistered. `status` now surfaces an orphaned agent's leftover
  state, and `agent disable <name> --purge` works for an already-removed agent
  (purge reads state, so it doesn't need the agent registered). Removal stays
  non-destructive by default.
- **Write-back refuses to persist a moved or rotated secret (security)** — secret
  re-reference matches by value, so it could not restore a `${secret:…}`
  reference when the user *moved* a secret into a field whose source counterpart
  is a literal (e.g. inlining a token onto a `command`), or *rotated* a
  secret-bearing field to a value the vault no longer knows — leaving the live
  credential as cleartext in the canonical (committed) source. `capture.Capture`
  (`import`/`reconcile` write-back) now runs a fail-closed backstop: if a
  resolved secret would still be written, or a `${secret:K}` a captured server's
  own field rotated/edited away from, it refuses the write-back and tells the
  user to update the vault or edit the source directly. The check is per-field
  and shape-aware: a single-item `import`/reconcile isn't blocked by an
  unrelated server's secret; an unchanged literal that merely contains a secret
  value, a trimmed-out secret, and a removed credential field (none leave
  cleartext) are allowed; only a value moved into a field or a secret slot
  rotated to cleartext is refused.
- **`secrets edit` no longer panics on a blank `$EDITOR`** — the `$EDITOR`
  word-split introduced above indexed an empty `strings.Fields` result, so a
  whitespace-only `EDITOR` (e.g. `EDITOR="   "`) crashed with an index panic;
  it now falls back to `vi`.
- **`secrets edit` honors a `$EDITOR` with flags** — `EDITOR="code --wait"`
  (or `vim -u NONE`, `emacsclient -c`, …) was treated as a single executable
  path and failed; `$EDITOR` is now word-split. This is the command that steers
  users away from the argv-leaking `set key=value` form, so its breakage
  mattered.
- **`secrets get`/`edit` match apply's vault contract** — `get` now errors on a
  non-string leaf instead of printing a Go-formatted value apply would refuse,
  and `edit` validates the saved buffer with the same `flatten` check apply uses
  (string-only leaves, no quoted-key-vs-table collisions) so it can't encrypt a
  vault that every later apply would reject. `secrets set --stdin` now trims a
  single trailing newline (not all of them), preserving a secret that
  legitimately ends in newlines.
- **Partial `import` seeds the items it already wrote** — when importing a whole
  component (e.g. `import claude:command`) or a whole agent, a write that failed
  partway left the *already-written* items unseeded in state, so the next
  `apply` saw them as foreign-collision and backed up / overwrote the very file
  they were just imported from. The written items are now seeded even on a
  partial failure (and the command still exits non-zero).
- **`[updates] default_mode` is honored** — the config knob (written into the
  generated `agentsync.toml` by `init`) was parsed but never consulted:
  `update` hardcoded `track` for any plugin without an explicit `update` mode,
  so a user who set `default_mode = "pinned"` still had default-mode plugins
  auto-bumped. The canonical default now flows into bump computation.
- **`reconcile` exits non-zero when a write-back fails** — a `[w]rite-back` that
  errored (e.g. an unsupported `/hooks/*` pointer) printed a `write-back error`
  line but `reconcile` still exited 0, so `reconcile --auto-writeback && deploy`
  proceeded as if the dest edit had been captured (the next apply would then
  clobber it). A failed write-back now exits non-zero.
- **Plugin manifest pin covers component bodies (tree hash)** — the integrity
  pin recorded in `plugins/<id>.toml` previously hashed only
  `.claude-plugin/plugin.json`, so a tampered or re-uploaded component body
  (`SKILL.md`, command/subagent markdown) with an unchanged `plugin.json` passed
  verification and was projected. The pin is now a `tree:v1:` hash over the
  whole plugin cache tree (excluding `.git/`), computed and verified by one
  shared function. **Migration:** existing pre-tree-hash (bare-hex) pins keep
  verifying under the prior `plugin.json`-only scheme, so existing installs are
  not broken; re-installing or `agentsync plugin upgrade <id>` rewrites the pin
  as a tree hash that covers the bodies going forward.
- **Nested memory fragments load** — `@import ./fragments/<name>` accepts a
  nested path (e.g. `sub/frag.md`), but fragments were read non-recursively and
  keyed by basename, so a nested fragment never loaded and its directive stayed
  literal in the rendered memory. Fragments are now walked recursively and keyed
  by their slash path under `memory/fragments/`.
- **Cross-plugin MCP/LSP server collisions no longer silently clobber** — two
  plugins (or a plugin and your own config) declaring the same MCP/LSP server id
  with *different* content were unioned and rendered into an id-keyed map
  last-wins, letting a later/untrusted plugin silently repoint a trusted
  server's `command`/`url`/`headers`. Projection now refuses such a divergent
  collision on mutating loads (apply/reconcile/import/update) and warns on
  read-only ones (status/diff/explain); identical duplicates still dedup. (Skill/
  command/subagent name collisions were already surfaced as a hard apply error.)
- **Frontmatter parser accepts a closing fence at EOF / empty frontmatter** — a
  skill/subagent/command markdown file whose closing `---` sits at end-of-file
  with no trailing newline (common: editors strip it; frontmatter-only files),
  or an empty `---`/`---` block, previously returned `unterminated frontmatter`.
  In `source.Load` that aborted the entire load — bricking *every* command — and
  in the Claude adapter's ingest it silently dropped the component. Both
  `ParseFrontmatter` implementations now accept these shapes.
- **OpenCode MCP servers render in OpenCode's native schema** — the adapter
  previously wrote `type` verbatim (`stdio`/`http`), `command` as a bare string
  with a separate `args` array, and `env`, none of which match OpenCode's config
  schema — so a rendered `opencode.json` could fail to load the server. MCP
  servers now project to `type: "local"|"remote"`, `command` as a string array,
  and `environment`, with ingest/reconcile inverting the translation through one
  shared helper. (`reconcile` write-back of an OpenCode MCP edit previously also
  crashed unmarshaling the array `command`.)
- **Secret never leaks on a structural native edit (security)** — write-back
  (`import` / `reconcile`) re-referenced `${secret:…}` strictly by field
  position, so a native edit that shifted structure — prepending an MCP/LSP arg,
  renaming an env/header key, or renaming a server id — moved the resolved
  cleartext to a location with no source counterpart and silently persisted the
  live credential into `~/.agentsync` (often a committed dotfiles repo). A
  value-based fallback now restores the placeholder for any source-referenced
  secret whose cleartext survives the positional pass, while still leaving a
  value that coincidentally equals a non-templated source literal untouched.
- **Foreign-collision backup covers explicit `null`** — a hand-authored
  destination holding an explicit `null` at a JSON pointer agentsync is about to
  write (e.g. `mcpServers.github = null` in `~/.claude.json`) is now backed up
  before being overwritten, instead of being silently replaced. Previously an
  absent pointer and a present-`null` pointer were indistinguishable to the
  collision check.
- **Marketplace slug never empty** — `marketplace add` derives its slug by
  sanitising *before* applying the `"marketplace"` fallback, and only adopts a
  declared `marketplace.json` name when it sanitises to something usable. A
  punctuation-only source or name (e.g. `...`) no longer authors
  `marketplaces/.toml` and a stray `marketplaces/_` cache directory.
- **Degenerate component ids rejected** — `mcp add .`, `mcp add " "`, and any
  write of a component whose id is a bare `.` or all-whitespace now error
  instead of authoring a confusing `mcp/..toml` / `mcp/ .toml` in the canonical
  source. Enforced centrally in `source.ValidateComponentID` (covering MCP, LSP,
  hook events, plugins, skills, subagents, commands) and mirrored in the CLI
  `mcp add` gate.

### Known limitations

Documented 0.x trade-offs rather than bugs — see the
[README](README.md#known-limits) and [capability matrix](docs/capability-matrix.md):
the Cursor adapter is a no-op (planned); Codex projections drop fields Codex has
no target for (reported in the apply translation report); OpenCode hooks/LSP are
skipped; TOML/JSONC comments are not preserved across write-back.

[Unreleased]: https://github.com/spxrogers/agentsync/compare/v0.10.1...HEAD
[0.7.3]: https://github.com/spxrogers/agentsync/compare/v0.7.2...v0.7.3
[0.7.2]: https://github.com/spxrogers/agentsync/compare/v0.7.1...v0.7.2
[0.7.1]: https://github.com/spxrogers/agentsync/compare/v0.7.0...v0.7.1
[0.1.0]: https://github.com/spxrogers/agentsync/releases/tag/v0.1.0
