# Changelog

All notable changes to agentsync are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Until the first stable `1.0.0` tag, the project is in **beta**: the canonical
source layout, CLI surface, and state schema are stabilizing but may still change.

## [Unreleased]

### Added

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

### Fixed

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

[Unreleased]: https://github.com/spxrogers/agentsync/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/spxrogers/agentsync/releases/tag/v0.1.0
