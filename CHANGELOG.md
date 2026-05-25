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

- **Canonical source model** in `~/.agentsync/` — hand-editable TOML + markdown
  for agents, MCP servers, marketplaces, plugins, memory, and skills.
- **Claude Code adapter** — full support for all seven components (MCP, memory,
  skill, subagent, command, hook, LSP) with per-key merge into `~/.claude.json`
  and `settings.json` that preserves foreign keys.
- **OpenCode adapter** — MCP, memory, skills, subagents, and commands via JSONC
  round-trip. Hooks and LSP are skipped with a warning.
- **Bidirectional drift** — a 3-way classifier (9 cases) at file and JSON-pointer
  granularity, surfaced via `status`/`diff` and resolved via an interactive
  `reconcile` loop with bulk hotkeys and `--auto-*` flags.
- **Marketplaces & plugins** — all five plugin sources (relative, github, url,
  git-subdir, npm), `strict`-mode conflict policy, `${CLAUDE_PLUGIN_ROOT}`
  substitution, per-component projection, translation report, manifest-SHA
  pinning, and update modes.
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
- **Safety primitives** — two-phase atomic writes, an apply lock, first-apply
  foreign-collision backups, symlinked-destination refusal, and a schema-versioned
  state file with portable (`${HOME}`-relative) keys.
- **Documentation set** — user guide, concepts, architecture, capability matrix,
  and component map under [`docs/`](docs/).

### Fixed

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
