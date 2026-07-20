# Component map

A package-by-package index of the codebase. Each entry lists the package's
responsibility, its key exported symbols, and which internal packages it depends
on. For *how the pieces fit together*, read the [architecture](architecture.md);
this page is the directory.

```
cmd/agentsync/        # main(): inject version ldflags, call cli.Execute()
internal/
├── cli/              # cobra command tree (entry layer)
├── source/           # the canonical model + loaders/writers   ← the schema
├── secrets/          # ${secret:}/${env:} resolve · re-reference · mask
├── project/          # .agentsync/ tree overlay discovery + merge
├── adapter/          # the per-agent Adapter interface + registry
│   ├── claude/ opencode/ codex/       # 9 deep adapters (agent-specific,
│   ├── cursor/ gemini/ continuedev/   # often bidirectional; claude is
│   ├── windsurf/ roo/ cline/          # the reference implementation)
│   ├── generic/                       # data-driven breadth tier (22 agents, specs.go)
│   └── noop/                          # placeholder for unimplemented agents
├── render/           # the apply pipeline: plan · write · report
├── capture/          # the single dest▶source write-back funnel
├── drift/            # the 3-way classifier (pure, no IO)
├── state/            # targets.json (last-applied hashes)
├── marketplace/      # fetch marketplaces/plugins · project components
├── git/              # leaf go-git wrapper: local dir rollback history (issue #118)
├── iox/              # atomic write + file lock
├── jsonkeys/         # per-key JSON-pointer merge (preserve foreign keys)
├── paths/            # AGENTSYNC_HOME / TARGET_ROOT / HOME resolution
├── ui/               # presentation: Printer · color · glyphs · WarnWriter
├── log/              # slog setup
└── testenv/          # hermetic-container test guard
```

---

## Entry layer

### `cmd/agentsync`
The binary's `main`. Injects `Version`/`Commit`/`Date` via `-ldflags` and calls
`cli.Execute()`. Nothing else lives here.

### `internal/cli`
Wires every cobra subcommand into the root tree and dispatches to handlers; this
is the only package that depends on nearly all the others.
- **Key:** `NewRoot() *cobra.Command`, `Execute() error`, `Version`/`Commit`/`Date`.
- **Commands:** `init`, `agent {add,remove,list,enable,disable}`, `apply`,
  `revert`, `status`, `diff`, `reconcile`, `import`, `doctor`, `verify`,
  `mcp {add,remove,list}`, `plugin {install,upgrade,enable,disable,remove,list}`,
  `marketplace {add,remove,list}`, `update`, `secrets {edit,get,set}`, `explain`,
  `version`.
- **Depends on:** adapter, source, state, secrets, paths, render, marketplace,
  project, drift, git, ui, log.
- **Files:** `root.go` + one file per command group.

---

## Core model

### `internal/source` — *the schema*
Loads and represents `~/.agentsync/`. The TOML-tagged structs here *are* the
canonical model that adapters render from; also provides write-back helpers and
memory-fragment expansion.
- **Key:** `Canonical` (the root model: `Config`, `MCPServers`, `Skills`,
  `Subagents`, `Commands`, `Hooks`, `LSPServers`, `Plugins`, `Marketplaces`,
  `Memory`, `Project`); `Load(fs, home)`; `ParseFrontmatter`; the `Write*`
  family (`WriteMCP`, `WriteLSP`, `WritePlugin`, `WriteMarketplace`, `WriteSkill`,
  `WriteSubagent`, `WriteCommand`, `WriteHooks`, `WriteMemory`); `ReadMCP`/`ReadLSP`
  (carry source-only fields); `ExpandMemoryImports`; `RenderManagedMemory` /
  `StripManagedBanner` (inject / strip the managed-file banner — see
  `docs/architecture.md`).
- **Depends on:** iox, jsonkeys.
- **Files:** `schema.go`, `loader.go`, `writer.go`, `memory.go`.

### `internal/secrets`
Resolves `${secret:dotted.key}` and `${env:NAME}` at apply time; re-references
cleartext back to `${secret:…}` for write-back; masks resolved values for display.
The `Resolved` wrapper type is the load-bearing leak guard.
- **Key:** `Resolver` (interface); `Resolved` (resolved-model wrapper);
  `SubstituteCanonical` (→ `Resolved`); `ReReferenceCanonical`; `CollectResolved`;
  `UnresolvedSecretRefs`; `MaskResolved`; `AgeBackend`/`EnvBackend`/`NopResolver`;
  `SelectBackend`; and the single field list `walkSecretFields` (in `walk.go`).
- **Depends on:** source, iox.
- **Files:** `secrets.go`, `age.go`, `resolved.go`, `substitute.go`,
  `rereference.go`, `mask.go`, `walk.go`, `secretpaths.go`, `leakscan.go`
  (the `ResidualSecretCleartext` backstop), `runtime.go`.

### `internal/project`
Discovers a repo's project-scope source tree — a `.agentsync/` **directory**
(the same on-disk layout as the user-scope `~/.agentsync/`) found by walking up
from the cwd — and overlays its canonical (project agents, MCP/LSP/skills/
subagents/commands/hooks, extra memory) onto the base user canonical. The retired
M5 single-file `.agentsync.toml` marker is no longer read: `Discover` surfaces a
**migration error** if it finds one with no `.agentsync/` tree.
- **Key:** `DirName` (`.agentsync`); `LegacyMarkerFile` (`.agentsync.toml`,
  migration-only); `Home(root)`; `Discover(start) (root, found, err)`;
  `Merge(base, proj) source.Canonical`.
- **Depends on:** source.
- **Files:** `project.go`.

---

## Translation layer

### `internal/adapter`
Declares the per-agent `Adapter` contract and a registry; the `DestWriter`
interface funnels all destination writes through the foreign-collision backup.
An **optional** `VersionedDirs` extension lets an adapter declare the on-disk
directories the apply tail should git-back-up for local rollback — an adapter
implements `VersionRoots(scope, project)` to return its config dir plus any
shared cross-agent dir it writes into, and MUST return nil at project scope (see
[architecture § VersionedDirs](architecture.md#versioneddirs-optional)).
- **Key:** `Adapter` (interface); `DestWriter` (interface);
  `VersionedDirs` (optional interface, `VersionRoots`); `NonEmptyDirs` (helper);
  `Scope` (`ScopeUser`/`ScopeProject`); `FileOp`; `Skip` (with `SkipKind`);
  `Registry` (`NewRegistry`, `Register`, `Lookup`, `Names`). Component support is
  expressed by what `Render` emits — an unsupported component yields a `Skip`,
  not an absent capability flag.
- **Files:** `adapter.go`, `registry.go`.

### `internal/adapter/claude`
The reference adapter — MCP, memory, skills, subagents, commands, and hooks, with per-key merge
into shared JSON files (`~/.claude.json`, `settings.json`, and a project's
repo-root `.mcp.json` for project-scope MCP servers) that preserves foreign
keys. `IngestPlugins` reads `enabledPlugins` / `extraKnownMarketplaces`
to discover plugins on `import`; Render projects each plugin's components to
Claude's native paths (`~/.claude/skills/<name>/`, `mcpServers` in
`.claude.json`, …) and deliberately leaves the enablement keys themselves
untouched. The asymmetry is the cross-adapter rule, not a Claude quirk — see
[architecture.md § PluginIngester (read-only)](architecture.md#pluginingester-read-only).
Hook fidelity: the canonical `Hook` models only command handlers, so (like
Gemini) Ingest leaves a `settings.json` hook event uncaptured with a warning if
it carries an unmodeled definition/handler field (e.g. `timeout`) or a
non-command handler, and Render reports a dropped `Skip` for any non-command
hook rather than emitting an empty-command entry — an import→apply round-trip
never rewrites the user's native `/hooks/<event>` array lossily.
- **Key:** `New(Options) *Adapter`; the `Adapter` + `PluginIngester` methods;
  `ParseFrontmatter`/`EncodeFrontmatter`; `MergeKeys`.
- **Depends on:** adapter, secrets, source, paths, iox, jsonkeys.
- **Files:** `claude.go`, `homedir.go`, `render.go`, `ingest.go`, `ingest_plugins.go`,
  `apply.go`, `paths.go`, `frontmatter.go`, `skill.go`, `command.go`,
  `subagent.go`, `hook.go`, `lsp.go`, `memory.go`, `settings.go`.

### `internal/adapter/opencode`
The OpenCode adapter — MCP, memory, skills, subagents, commands via JSONC
round-trip (`tailscale/hujson`). Skips Hook and LSP (reported with a warning).
- **Key:** `New(Options) *Adapter`; the `Adapter` methods.
- **Depends on:** adapter, secrets, source, paths, iox.
- **Files:** `opencode.go`, `homedir.go`, `render.go`, `ingest.go`, `apply.go`, `paths.go`,
  `skill.go`, `subagent.go`, `command.go`, `memory.go`, `settings.go`.

### `internal/adapter/codex`
The Codex CLI adapter — MCP, memory, skills, subagents, slash commands, and
hooks. MCP servers (`[mcp_servers.*]`) and hooks (inline `[hooks.*]`) both merge
into the TOML `~/.codex/config.toml` via the `merge-toml-keys` strategy
(`MergeTOML` in `settings.go`, which preserves the user's foreign keys) — so
config.toml is the adapter's single key-merge file; skills land in the shared
`~/.agents/skills/`; subagents project to Codex's TOML agent format and commands
to global-only custom prompts.
Implements `PluginIngester` (parses `[plugins."<name>@<source>"]` enable-state
on `import`); Render does **not** re-emit those tables on `apply`, matching the
cross-adapter invariant — see
[architecture.md § PluginIngester (read-only)](architecture.md#pluginingester-read-only).
Skips LSP (Codex has no LSP concept).
- **Key:** `New(Options) *Adapter`; the `Adapter` + `PluginIngester` methods;
  `MergeTOML`; `IngestMCPSpec`.
- **Depends on:** adapter, adapter/claude (frontmatter helpers), secrets, source,
  paths, iox, jsonkeys, go-toml/v2.
- **Files:** `codex.go`, `homedir.go`, `render.go`, `mcp.go`, `ingest.go`, `ingest_plugins.go`,
  `apply.go`, `paths.go`, `skill.go`, `command.go`, `subagent.go`, `hook.go`,
  `memory.go`, `settings.go`.

### `internal/adapter/cursor`
The Cursor adapter — MCP, memory, skills, subagents, slash commands, and hooks.
MCP lands in `.cursor/mcp.json` (the same `mcpServers` shape as Claude) and hooks
in `.cursor/hooks.json` (`{ "version": 1, "hooks": { … } }`) — both JSON, so the
adapter's single key-merge strategy is `merge-json-keys`. The required hooks
`version` is injected post-merge in `applyWrite` (never rendered into `op.Content`,
so it is never an orphan-strippable owned key). Memory projects to the repo-root
`AGENTS.md` at project scope only (user-level rules live in Cursor's app-local
storage); skills to `.cursor/skills/`; subagents to `.cursor/agents/<name>.md`
(`tools`/`color` dropped); commands to `.cursor/commands/<name>.md` (plain
markdown — frontmatter dropped). Skips LSP (Cursor has no LSP concept).
Implements no `PluginIngester` yet — Cursor's native plugin enable-state location
is undocumented, so plugin discovery on `import` is deferred; `apply` still fans
out plugin components like every adapter.
- **Key:** `New(Options) *Adapter`; the `Adapter` methods; `IngestMCPSpec`.
- **Depends on:** adapter, adapter/claude (frontmatter/skill/extra helpers),
  secrets, source, paths, iox, jsonkeys, afero.
- **Files:** `cursor.go`, `homedir.go`, `render.go`, `mcp.go`, `ingest.go`, `apply.go`,
  `paths.go`, `skill.go`, `command.go`, `subagent.go`, `hook.go`, `memory.go`.

### `internal/adapter/gemini`
The Gemini CLI adapter — MCP, memory, slash commands, subagents, and hooks. MCP
(`mcpServers`, with Gemini's `url`/`httpUrl` transport split) and hooks (`hooks`,
the same nested shape as Claude) both merge into `.gemini/settings.json` via
`merge-jsonc-keys` — settings.json is the adapter's single key-merge file, so the
user's other keys (`theme`, `model`, …) are preserved. Memory projects to
`GEMINI.md` (`~/.gemini/GEMINI.md` user / repo-root `GEMINI.md` project); commands
to `.gemini/commands/<name>.toml` (`description` + `prompt`); subagents to
`.gemini/agents/<name>.md`. Skips Skill (Gemini uses extensions, not Agent
Skills) and LSP (no LSP concept) — both ✗ skip. No `PluginIngester` (no
native plugin enable-state agentsync models).
- **Key:** `New(Options) *Adapter`; the `Adapter` methods; `IngestMCPSpec`.
- **Depends on:** adapter, adapter/claude (frontmatter helpers), secrets, source,
  paths, iox, jsonkeys, go-toml/v2.
- **Files:** `gemini.go`, `homedir.go`, `render.go`, `mcp.go`, `ingest.go`, `apply.go`,
  `paths.go`, `command.go`, `subagent.go`, `hook.go`, `memory.go`.

### `internal/adapter/continuedev`
The Continue adapter (package `continuedev` — `continue` is a Go keyword; the
agent name is still `continue`). MCP, memory, and slash commands, projected as
Continue "blocks" — one file per item, so there is **no key-merge**
(`KeyMergeStrategy()` returns `""`): MCP → `.continue/mcpServers/<id>.yaml`
(stdio command/args/env; remote `streamable-http`/`sse` + `url` +
`requestOptions.headers`); memory → `.continue/rules/agentsync.md` (a
frontmatter-less always-apply rule); commands → `.continue/prompts/<name>.md`
prompt blocks. Skills/subagents/hooks/LSP have no faithful Continue target and
are skipped with a report (Skill/Subagent/Hook/LSP). No
`PluginIngester`.
- **Key:** `New(Options) *Adapter`; the `Adapter` methods; `IngestMCPSpec`.
- **Depends on:** adapter, adapter/claude (frontmatter/Extra helpers), secrets,
  source, paths, iox, sigs.k8s.io/yaml.
- **Files:** `continue.go`, `homedir.go`, `render.go`, `mcp.go`, `ingest.go`, `apply.go`,
  `paths.go`, `command.go`, `memory.go`.

### `internal/adapter/windsurf`
The Windsurf (Cascade) adapter — MCP, memory, and slash commands, **scope-
asymmetric** to match Windsurf's layout: only **MCP** is user-scope-only
(`~/.codeium/windsurf/mcp_config.json`, JSON `mcpServers` via `merge-json-keys`;
remote uses `serverUrl`), skipped (reported) at project scope. **Memory** and
**commands** render at **both** scopes — project → `.windsurf/rules/agentsync.md`
(workspace rule) / `.windsurf/workflows/<name>.md`, user → the global rules file
`~/.codeium/windsurf/memories/global_rules.md` / `~/.codeium/windsurf/global_workflows/`
— all plain markdown. Skills/subagents/hooks/LSP have no Windsurf concept and are
skipped. It **implements `WarnEmitter`**: `Ingest` emits a warning when a workspace
rule lacks the agentsync-rendered `trigger: always_on` frontmatter. No
`PluginIngester`.
- **Key:** `New(Options) *Adapter`; the `Adapter` methods; `IngestMCPSpec`.
- **Depends on:** adapter, adapter/claude (Extra helpers), secrets, source,
  paths, iox, jsonkeys.
- **Files:** `windsurf.go`, `homedir.go`, `render.go`, `mcp.go`, `ingest.go`, `apply.go`,
  `paths.go`, `command.go`, `memory.go`.

### `internal/adapter/roo`
The Roo Code adapter — MCP, memory, and slash commands via clean filesystem
`.roo/` paths. MCP → `.roo/mcp.json` (project-level, `mcpServers` via
`merge-json-keys`; remote uses explicit `type: streamable-http`/`sse`); memory →
`.roo/rules/agentsync.md` (plain markdown rule) and commands →
`.roo/commands/<name>.md` (markdown + frontmatter — keeps `description` +
`argument-hint`), both at user *and* project scope. Roo's global MCP is VS Code
globalStorage (not targeted — user-scope MCP is reported as a skip). Skips
Skill/Subagent/Hook/LSP. No `PluginIngester`.
- **Key:** `New(Options) *Adapter`; the `Adapter` methods; `IngestMCPSpec`.
- **Depends on:** adapter, adapter/claude (frontmatter/Extra helpers), secrets,
  source, paths, iox, jsonkeys.
- **Files:** `roo.go`, `homedir.go`, `render.go`, `mcp.go`, `ingest.go`, `apply.go`, `paths.go`,
  `command.go`, `memory.go`.

### `internal/adapter/cline`
The Cline adapter — MCP, memory, and slash commands, **scope-asymmetric**: MCP
renders at user scope into the Cline CLI's clean `~/.cline/mcp.json`
(`merge-json-keys`; transport inferred, no `type` — remote uses `url`+`headers`),
while memory (`.clinerules/agentsync.md`, plain markdown) and commands
(`.clinerules/workflows/<name>.md`, plain markdown) render at project scope; the
non-applicable scope reports a skip. Cline has no project MCP file (its VS Code
extension uses OS/editor-specific globalStorage agentsync does not write) and its
global rules live in `~/Documents/Cline/` (also not targeted). Skills/subagents/
hooks/LSP have no Cline concept and are skipped. Emits no Ingest warnings
(rules/workflows are plain markdown), so it does not implement `WarnEmitter`. No
`PluginIngester`.
- **Key:** `New(Options) *Adapter`; the `Adapter` methods; `IngestMCPSpec`.
- **Depends on:** adapter, adapter/claude (Extra helpers), secrets, source,
  paths, iox, jsonkeys.
- **Files:** `cline.go`, `homedir.go`, `render.go`, `mcp.go`, `ingest.go`, `apply.go`,
  `paths.go`, `command.go`, `memory.go`.

### `internal/adapter/generic`
The **breadth-tier** adapter: one data-driven `Adapter` implementation that serves
many agents from a table of verified `Spec`s (`specs.go`) rather than a package
each. Covers **memory** (a rules/instructions file, plain markdown), **MCP** where
the agent reads a JSON server-map agentsync can express, and **Agent Skills**
(`SKILL.md` directories) where the agent natively scans a skills directory — every
other component is reported as a skip. A `Spec` declares per-scope memory/MCP/skills
paths plus MCP "dialect" knobs that capture the tail's variance (top-level key
`mcpServers`/`servers`/`mcp`/`context_servers`/the flat namespaced `amp.mcpServers`;
transport field `type`/`transport`/inferred; stdio value `stdio`/`local`; remote
URL key `url`/`httpUrl`/`serverUrl`). The MCP merge is JSONC-tolerant (hujson), so a
commented settings file (Zed/Copilot/Amp) is preserved, not clobbered (re-emitted
as plain JSON, like OpenCode). Skills need no dialect — the on-disk format is
uniform — so the tier reuses the deep adapters' shared `claude.SkillFileOps`
projection; an agent's `Skills` target is usually the cross-vendor `.agents/skills/`
(byte-identical to Codex, so the render pipeline dedupes the ops). Breadth agents
register through the normal registry and flow through apply/import (drift, secrets,
capture). Adding an agent is a verified table row, not a package.
- **Key:** `Spec`, `New(Spec, Options) *Adapter`; the `Adapter` methods; `Specs()`.
- **Depends on:** adapter, adapter/claude (Extra + SkillFileOps helpers), secrets,
  source, paths, iox, jsonkeys.
- **Files:** `generic.go`, `homedir.go`, `render.go`, `ingest.go`, `apply.go`, `specs.go`.

### `internal/adapter/noop`
Placeholder adapter that detects true and renders nothing. Used as a registry
stand-in in tests; no production agent is registered as a noop today (every valid
agent has a real adapter). `agent add`/`import` still reject any future
noop-registered agent unless `AGENTSYNC_ALLOW_UNIMPLEMENTED=1`.
- **Depends on:** adapter, secrets, source. **Files:** `noop.go`.

---

## Pipeline & state

### `internal/render`
Orchestrates apply: canonical + registry → per-agent `FileOp`s/`Skip`s, runs
collision detection and backups, records state, synthesizes cleanup ops for
orphaned owned keys, and builds the translation report. `Plan` also validates
every text-component id (subagent/command/skill `Name`) against
`source.ValidateComponentID` before any adapter joins it into a destination
filename — the Render-time path-traversal guard, symmetric with the dest→source
write boundary (see architecture §7).
- **Key:** `Plan`; `Apply`; `PreviewApply` (dry-run: collision preview +
  synced/would-change verdict); `Writer`
  (`NewWriter`/`NewPreviewWriter`); `TranslationReport` (`PrintText`/`PrintJSON`);
  `BuildReport`; `RecordOpsState`; `OrphanFiles`; `PruneStaleState`;
  `BackupFile`/`PruneBackups`; `CollisionReport`.
- **Depends on:** adapter, secrets, source, state, paths, iox, drift.
- **Files:** `pipeline.go`, `writer.go`, `state_apply.go`, `report.go`.

### `internal/capture`
The single dest→source write-back path: re-references secrets, preserves
source-only fields, writes via `source.Write*`. Used by `import` and reconcile.
- **Key:** `Capture(home, ingested, opts) (Result, error)`; `Opts`; `Result`.
- **Depends on:** source, secrets, paths, iox.
- **Files:** `capture.go`, `leak_fixture.go` (compile-time leak guard).

### `internal/drift`
Pure 3-way classifier — no IO.
- **Key:** `Class` (`Clean`, `Pending`, `Drift`, `Converged`, `Conflict`, `New`,
  `ForeignCollision`, `Orphan`, `OrphanDrifted`); `Classify(hsrc, happlied, hdest)`;
  `SafeForAutoApply(c)`.
- **Files:** `classifier.go`.

### `internal/state`
Persists last-applied hashes and plugin/marketplace pins to
`.state/targets.json`; schema-versioned with migrators.
- **Key:** `SchemaVersion`; `Targets` (`Files`, `Keys`, `Marketplaces`,
  `Plugins`); `FileEntry`; `KeyEntry`; `Load`/`Save`; `migrate`.
- **Depends on:** iox. **Files:** `schema.go`, `store.go`, `migrate.go`.

### `internal/marketplace`
Models the Claude marketplace/plugin format, fetches sources, and projects plugin
manifests into canonical components.
- **Key:** `Marketplace`, `PluginEntry`, `Source`, `PluginManifest`;
  `ProjectionResult`; `Project`/`ProjectWithReader`; `ProjectInstalled`
  (one installed plugin in isolation — lets `explain <id>` attribute coverage to
  the named plugin rather than the flattened union); `Fetcher` (interface) with
  `GitFetcher`/`NPMFetcher`/`RelativeFetcher`; `LoadProjected`/
  `LoadProjectedLenient`/`LoadProjectedExcluding`.
- **Depends on:** source, log.
- **Files:** `manifest.go`, `treehash.go` (the `tree:v1:` content hash),
  `projection.go`, `loadprojected.go`, `fetcher.go`, `fetch_git.go`,
  `fetch_npm.go`, `fetch_relative.go`, `update.go`.

---

## Infrastructure & presentation

Leaf packages with no internal dependencies, plus the thin `ui` presentation
layer (which builds only on `untrusted`).

### `internal/git`
The **only** `go-git` surface in the codebase: a local-only, directory-level
rollback history for destination git backup (issue #118). Each managed
destination dir becomes its own repo carrying an `[agentsync] managed = true`
marker so agentsync only ever auto-commits into repos it created; a checkpoint is
recorded after each apply and `revert` rolls a dir back append-only. It exposes
**no** remote/push API — enforced by the source-scanning `TestNoPushSurface`
guard — so a backup can never leave the machine (the history may hold the
cleartext secrets the rendered files already contain).
- **Key:** `Detect`/`State` (`StateUntracked`/`StateAgentsyncOwned`/`StateForeign`;
  `State.String()` → `agentsync-versioned` / `foreign source control` / `untracked`);
  `Init`/`Open`/`OwnsExactly`/`HasNestedRepoBelow`; `Stage`/`StageTrackedDeletions`/
  `CommitStaged`/`SnapshotDirtyTracked`/`IsClean`; `Log`/`Resolve`/`Plan`/`Restore`;
  `Identity`; `NoticeFile`.
- **Depends on:** nothing internal (leaf).
- **Files:** `git.go`, `init.go`, `commit.go`, `log.go`, `restore.go`, `perms.go`.

### `internal/iox`
Atomic file IO and locking.
- **Key:** `AtomicWrite(dest, data, mode)`; `Lock`/`AcquireLock`/
  `AcquireLockTimeout`; `ErrSymlinkDest`; `AllowSymlinkDestEnv`.
- **Files:** `atomic.go`, `lock.go`.

### `internal/jsonkeys`
Per-key JSON-pointer merge that preserves foreign keys and uses `json.Number`
(no float64 rounding).
- **Key:** `DecodeObject`; `DecodeYAML`; `MergeKeys(existing, ours, ownedPointers)`.
- **Files:** `jsonkeys.go`.

### `internal/paths`
Resolves `AGENTSYNC_HOME`, `AGENTSYNC_TARGET_ROOT`, and `$HOME`; converts between
absolute and `${HOME}`-relative forms for portable state.
- **Key:** `Env` (interface), `OSEnv`, `MapEnv`; `HomeDir`; `AgentsyncHome`;
  `HomeRelative`/`FromHomeRelative`.
- **Files:** `paths.go`.

### `internal/log`
slog setup. **Key:** `New(w, verbose) *slog.Logger`. **Files:** `log.go`.

### `internal/untrusted`
The display trust boundary for fetched/native metadata. Owns `Sanitize` (strip
terminal-control + deceptive bidi/zero-width runes) and the `Text` defined string
type whose `String()` sanitizes — so a plugin/marketplace id, version, or name
typed `untrusted.Text` is safe to print through `fmt` by construction; the raw
value is reachable only via the explicit `Unverified()`. `ui.Sanitize` delegates
here. See [architecture §7](architecture.md#7-safety-primitives) and `SECURITY.md`.
- **Key:** `Text` (`.String()` / `.Unverified()` / `.Empty()`); `Wrap`; `Sanitize`.
- **Files:** `untrusted.go`.

### `internal/ui`
The presentation layer — every command renders styled output through a `*Printer`
so color, glyph, and spacing decisions live in one place. Owns the curated glyph
vocabulary (`✓`/`◐`/`✗`/`⚠`/`•`/`→`), the `--color` mode resolution, and a
`WarnWriter` that restyles `warning:` line prefixes. `Sanitize` delegates to
`internal/untrusted`, so untrusted metadata printed through `ui` is stripped of
terminal-control and deceptive-format runes by construction.
- **Key:** `Printer` (`New`, `Color`, `Section`, colour helpers); the
  package-level `Pad` helper; `ColorMode`/`ParseColorMode`; the `Glyph*`
  vocabulary; `WarnWriter` (`NewWarnWriter`, `RouteTo`, `Flush`); `Sanitize`.
- **Depends on:** untrusted.
- **Files:** `ui.go`, `spinner.go`.

### `internal/testenv`
Guards FS-touching tests so they only run in the hermetic container.
- **Key:** `RequireContainer(t)`; `MustRunInContainer()`; `InContainer() bool`;
  `EnvVar` (`AGENTSYNC_TEST_IN_CONTAINER`).
- **Files:** `container.go`.

---

## Dependency direction at a glance

`cli` sits on top of everything. `render`, `capture`, and the adapters depend on
`source` + `secrets`. `source`/`secrets`/`state` depend only on the leaf infra
packages (`iox`, `jsonkeys`, `paths`, and — for the canonical plugin/marketplace
identity fields typed `untrusted.Text` — `untrusted`). `git` (destination
rollback history, reached only from `cli`) is likewise a leaf. `drift`, `git`,
`iox`, `jsonkeys`, `paths`, `log`, and `untrusted` depend on nothing internal —
they're the foundation; `ui` (presentation) builds only on `untrusted`. See the
rendered dependency graph in
[architecture §10](architecture.md#10-package-layering).
