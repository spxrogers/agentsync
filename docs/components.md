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
├── project/          # .agentsync.toml overlay discovery + merge
├── adapter/          # the per-agent Adapter interface + registry
│   ├── claude/       #   full adapter (reference implementation)
│   ├── opencode/     #   adapter (hooks/LSP skipped)
│   └── noop/         #   placeholder for unimplemented agents
├── render/           # the apply pipeline: plan · write · report
├── capture/          # the single dest▶source write-back funnel
├── drift/            # the 3-way classifier (pure, no IO)
├── state/            # targets.json (last-applied hashes)
├── marketplace/      # fetch marketplaces/plugins · project components
├── iox/              # atomic write + file lock
├── jsonkeys/         # per-key JSON-pointer merge (preserve foreign keys)
├── paths/            # AGENTSYNC_HOME / TARGET_ROOT / HOME resolution
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
  `status`, `diff`, `reconcile`, `import`, `doctor`, `verify`,
  `mcp {add,remove,list}`, `plugin {install,upgrade,enable,disable,remove,list}`,
  `marketplace {add,remove,list}`, `update`, `secrets {edit,get,set}`, `explain`.
- **Depends on:** adapter, source, state, secrets, paths, render, marketplace,
  project, drift, log.
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
  (carry source-only fields); `ExpandMemoryImports`.
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
  `rereference.go`, `mask.go`, `walk.go`, `secretpaths.go`.

### `internal/project`
Discovers a repo's `.agentsync.toml` marker by walking up from the cwd and merges
its overlay (project MCP servers, plugin enable/disable, extra memory) onto the
base canonical model.
- **Key:** `MarkerFile` (`.agentsync.toml`); `Marker`; `Discover(cwd)`;
  `Merge(base, m) source.Canonical`.
- **Depends on:** source.
- **Files:** `project.go`.

---

## Translation layer

### `internal/adapter`
Declares the per-agent `Adapter` contract and a registry; the `DestWriter`
interface funnels all destination writes through the foreign-collision backup.
- **Key:** `Adapter` (interface); `DestWriter` (interface); `Capability`
  (bitmask: `CapMCP`, `CapMemory`, `CapSkill`, `CapSubagent`, `CapCommand`,
  `CapHook`, `CapLSP`); `Scope` (`ScopeUser`/`ScopeProject`); `FileOp`; `Skip`;
  `Registry` (`NewRegistry`, `Register`, `Lookup`, `Names`).
- **Files:** `adapter.go`, `registry.go`.

### `internal/adapter/claude`
The reference adapter — all seven components including LSP, with per-key merge
into shared JSON files (`~/.claude.json`, `settings.json`) that preserves
foreign keys. `IngestPlugins` reads `enabledPlugins` / `extraKnownMarketplaces`
to discover plugins on `import`; Render projects each plugin's components to
Claude's native paths (`~/.claude/skills/<name>/`, `mcpServers` in
`.claude.json`, …) and deliberately leaves the enablement keys themselves
untouched. The asymmetry is the cross-adapter rule, not a Claude quirk — see
[architecture.md § PluginIngester (read-only)](architecture.md#pluginingester-read-only).
- **Key:** `New(Options) *Adapter`; the `Adapter` + `PluginIngester` methods;
  `ParseFrontmatter`/`EncodeFrontmatter`; `MergeKeys`.
- **Depends on:** adapter, secrets, source, paths, iox, jsonkeys.
- **Files:** `claude.go`, `render.go`, `ingest.go`, `ingest_plugins.go`,
  `apply.go`, `paths.go`, `frontmatter.go`, `skill.go`, `command.go`,
  `subagent.go`, `hook.go`, `lsp.go`, `memory.go`, `settings.go`.

### `internal/adapter/opencode`
The OpenCode adapter — MCP, memory, skills, subagents, commands via JSONC
round-trip (`tailscale/hujson`). Omits `CapHook`/`CapLSP` (skipped with a warning).
- **Key:** `New(Options) *Adapter`; the `Adapter` methods.
- **Depends on:** adapter, secrets, source, paths, iox.
- **Files:** `opencode.go`, `render.go`, `ingest.go`, `apply.go`, `paths.go`,
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
Omits `CapLSP` (Codex has no LSP concept).
- **Key:** `New(Options) *Adapter`; the `Adapter` + `PluginIngester` methods;
  `MergeTOML`; `IngestMCPSpec`.
- **Depends on:** adapter, adapter/claude (frontmatter helpers), secrets, source,
  paths, iox, jsonkeys, go-toml/v2.
- **Files:** `codex.go`, `render.go`, `mcp.go`, `ingest.go`, `ingest_plugins.go`,
  `apply.go`, `paths.go`, `skill.go`, `command.go`, `subagent.go`, `hook.go`,
  `memory.go`, `settings.go`.

### `internal/adapter/noop`
Placeholder adapter for unimplemented agents (Cursor): detects true,
renders nothing. `agent add` rejects these unless `AGENTSYNC_ALLOW_UNIMPLEMENTED=1`.
- **Depends on:** adapter, secrets, source. **Files:** `noop.go`.

---

## Pipeline & state

### `internal/render`
Orchestrates apply: canonical + registry → per-agent `FileOp`s/`Skip`s, runs
collision detection and backups, records state, synthesizes cleanup ops for
orphaned owned keys, and builds the translation report.
- **Key:** `Plan`; `Apply`; `PreviewCollisions`; `Writer`
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
  `ProjectionResult`; `Project`/`ProjectWithReader`; `Fetcher` (interface) with
  `GitFetcher`/`NPMFetcher`/`RelativeFetcher`; `LoadProjected`/
  `LoadProjectedLenient`/`LoadProjectedExcluding`.
- **Depends on:** source, log.
- **Files:** `manifest.go`, `projection.go`, `loadprojected.go`, `fetcher.go`,
  `fetch_git.go`, `fetch_npm.go`, `fetch_relative.go`, `update.go`.

---

## Infrastructure (leaf packages, no internal deps)

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

### `internal/testenv`
Guards FS-touching tests so they only run in the hermetic container.
- **Key:** `RequireContainer(t)`; `MustRunInContainer()`; `InContainer() bool`;
  `EnvVar` (`AGENTSYNC_TEST_IN_CONTAINER`).
- **Files:** `container.go`.

---

## Dependency direction at a glance

`cli` sits on top of everything. `render`, `capture`, and the adapters depend on
`source` + `secrets`. `source`/`secrets`/`state` depend only on the leaf infra
packages (`iox`, `jsonkeys`, `paths`). `drift`, `iox`, `jsonkeys`, `paths`, and
`log` depend on nothing internal — they're the foundation. See the rendered
dependency graph in [architecture §10](architecture.md#10-package-layering).
