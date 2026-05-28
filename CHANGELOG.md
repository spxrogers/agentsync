# Changelog

All notable changes to agentsync are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Until the first stable `1.0.0` tag, the project is in **beta**: the canonical
source layout, CLI surface, and state schema are stabilizing but may still change.

## [Unreleased]

The v1.0 beta. Functional end-to-end (green under `just test-release`); remaining
work before the `1.0.0` tag is distribution plumbing and a few documented
trade-offs (see [Known limits](README.md#known-limits-in-v1x)).

### Added

- **Styled CLI output and a `--color` flag** — `agentsync status`, `diff`,
  `doctor`, and `apply` now render through a single presentation layer
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

Documented v1.x trade-offs rather than bugs — see the
[README](README.md#known-limits-in-v1x) and [capability matrix](docs/capability-matrix.md):
Codex/Cursor adapters are no-ops (planned v1.1/v1.2); OpenCode hooks/LSP are
skipped; TOML/JSONC comments are not preserved across write-back.

[Unreleased]: https://github.com/spxrogers/agentsync/commits/main
