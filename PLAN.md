# opensync — implementation plan

## Context

The user wants a CLI to centrally manage AI coding-agent configurations across Claude Code, OpenCode, Codex CLI, Cursor, Continue, Gemini, and Aider. Existing tools are SKILL-specific, weak on MCP, weak on project-local, and have no first-class plugin/marketplace story — yet plugin marketplaces are now the standard distribution channel for Claude Code and OpenCode. opensync makes marketplaces the primary configuration mechanism, supports per-project overrides, and detects drift when a user installs/edits something directly in an agent (prompting to write-back or override).

**Scope guardrail.** opensync is single-machine. Cross-machine sync is delegated: the user can chezmoi-manage `~/.opensync/` itself. opensync does not reinvent dotfile distribution.

## Locked decisions (from clarifying Qs)

- **Config model**: hybrid — canonical schema for cross-agent concerns (MCP, AGENTS.md/memory, marketplace registrations, plugin enables, permissions); passthrough for agent-native files (Claude subagents/skills, OpenCode modes, Codex profiles, Cursor rules).
- **Source location**: opinionated `~/.opensync/`. User layers chezmoi on top for multi-machine.
- **Language**: Go, single static binary, cobra CLI.
- **v1 agents**: all seven, but depth varies — Claude + OpenCode get full marketplace/plugin/canonical projection; Codex/Cursor/Gemini get MCP+memory+passthrough; Continue/Aider get minimal in v1.

## Architecture

### Source-state layout (`~/.opensync/`)

```
opensync.toml                # global toggles, secret backend
agents.toml                  # per-agent enable + path overrides
canonical/
  mcp/servers.toml           # [server.<id>] command/args/env/agents=["claude","*"]
  marketplaces/registry.toml # [marketplace.<name>] source/ref/sha/agents
  marketplaces/cache/        # fetched marketplace.json blobs
  plugins/enabled.toml       # ["<id>@<marketplace>"] enabled/agents
  memory/AGENTS.md           # canonical memory; @-imports under fragments/
  permissions.toml
passthrough/<agent>/...      # verbatim files, mirrored to native paths
projects/<slug>/             # project-scoped overlay (same shape as top-level)
  .meta.toml                 # { path = "/abs/path/to/project" }
  canonical/...
  passthrough/...
secrets/secrets.age          # opt-in age-encrypted env (resolved at apply)
.state/
  targets.json               # last-applied hashes + provenance map
  apply.lock
  staging/                   # two-phase write staging dir
  backups/<ts>/              # first-apply backups of pre-existing native files
  cache/                     # marketplace HTTP/etag cache
```

**Scope expression**: `agents = ["*"]` or `["claude","opencode"]` or `exclude_agents=[...]` on canonical entries. Project scope is a deep-merge overlay on user scope, keyed by stable id; `disabled = true` to suppress an inherited entry.

**Secrets**: `${secret:foo}` in canonical values, resolved by pluggable backend (`env` | `file` | `age` in v1; sops deferred).

### Adapter interface

One Go interface, all seven adapters implement it. Capabilities are advertised as flags so the renderer fans out without per-agent special-casing.

```go
// internal/adapter/adapter.go
type Capability uint32
const (
  CapMCP Capability = 1 << iota
  CapMarketplace; CapPlugin; CapMemory; CapSubagents
  CapCommands; CapHooks; CapPermissions; CapSkills
)

type Adapter interface {
  Name() string
  Capabilities() Capability
  ManagedPaths(scope Scope, projectRoot string) []string
  Project(ctx, CanonicalModel) ([]File, error)        // pure: source -> target files
  Ingest(ctx, scope, projectRoot string) (CanonicalModel, error)  // disk -> partial source
  DetectDrift(ctx, scope, projectRoot string, lastApplied map[string]FileHash) ([]Drift, error)
  AddMCPServer(ctx, id, MCPServer) ([]SourceEdit, error)
  RegisterMarketplace(ctx, name, Marketplace) ([]SourceEdit, error)
  EnablePlugin(ctx, PluginRef) ([]SourceEdit, error)
  InstallPluginAsMCPProjection(ctx, PluginRef, PluginManifest) ([]SourceEdit, error)
}
```

`Drift` carries both file-level unified diffs and per-key `KeyDrift` for structured formats; `KeyDrift.Owner` distinguishes opensync-owned keys from foreign keys (so opensync never claims to manage e.g. unrelated keys in `~/.claude.json`).

### Drift detection (3-way, on-demand)

For each managed (agent, scope, path), compare three values:
- **lastApplied** — hash from `.state/targets.json` (what we wrote last apply).
- **currentSource** — recompute by running `Project()` now.
- **destination** — what's on disk.

| destination vs lastApplied | source vs lastApplied | classification |
|---|---|---|
| = | = | clean |
| = | ≠ | pending (run apply) |
| ≠ | = | **drift** (user edited destination directly) |
| ≠ | ≠, dest=src | converged — refresh state silently |
| ≠ | ≠, dest≠src | **conflicting drift** — reconcile must resolve |

Structured files (JSON/JSONC/TOML/YAML) get per-key drift via tree walk over generic `map[string]any`. Foreign keys are reported but never block apply. Reconcile UX uses charmbracelet/huh to prompt per-key: write-back / override / skip / ignore / diff, with bulk-apply hotkeys.

Write-back uses provenance recorded in `.state/targets.json` to find the canonical TOML key that produced a destination key, and edits it via TOML AST (preserving comments). Passthrough write-back is a straight file copy back into `passthrough/<agent>/...`.

### Marketplace primacy

`opensync marketplace add github:anthropic/claude-code-plugins`:
1. Fetch (shallow git clone or HTTP) into `.state/cache/marketplaces/...`.
2. Pin sha into `canonical/marketplaces/registry.toml`.
3. Cache parsed `marketplace.json` for offline render.
4. Does NOT auto-enable plugins.

`opensync apply` then projects per agent capability. A plugin is treated as a heterogeneous bundle (MCP servers, instructional text, slash commands, subagents, skills, hooks, settings fragments); each content type has its own translation policy:

| Plugin content | Native marketplace agents (Claude, OpenCode) | Non-marketplace agents (Codex, Cursor, Gemini, Continue) | Aider |
|---|---|---|---|
| MCP servers | native registration | project to native MCP config | skip (no MCP) |
| Instructional text / memory | native registration | project as memory file (`AGENTS.md` for Codex, `.cursorrules` for Cursor, `GEMINI.md` for Gemini, Continue rules block) | project to `CONVENTIONS.md` |
| Slash commands (md) | native registration | Cursor + Continue: project as prompt-rules. Codex/Gemini: skip with log | skip with log |
| Subagents | native (Claude/OpenCode) | skip with log | skip |
| Skills | native (Claude) | skip with log | skip |
| Hooks | native (Claude) | skip with log | skip |

Native marketplace agents additionally get the registry written into their native files (`~/.claude.json` `marketplaces` + `enabledPlugins`; OpenCode equivalents), with plugin install paths populated via symlinks from the cache.

Cross-agent translation richer than the table above (subagent→mode mapping, skill→MCP-tool wrappers, hook portability) is explicitly **v2** — too much semantic impedance for v1.

### Translation reporting

`opensync apply` and `opensync verify` print an explicit per-agent translation summary so the user is never surprised by silent drops:

```
plugin: security-scan@anthropic
  claude   ✓ full (3 MCP, 2 commands, 1 subagent, 1 hook)
  opencode ✓ full (3 MCP, 2 commands, 1 subagent)
  codex    ◐ partial (3 MCP projected; 2 commands skipped: no equivalent; 1 subagent skipped; 1 hook skipped)
  cursor   ◐ partial (3 MCP, 2 commands as rules; 1 subagent skipped; 1 hook skipped)
  gemini   ◐ partial (3 MCP, instructions → GEMINI.md; 2 commands skipped; 1 subagent/hook skipped)
  continue ◐ partial (3 MCP, 2 commands as rules; 1 subagent/hook skipped)
  aider    ✗ skipped (no MCP support; instructions → CONVENTIONS.md only)
```

Same data is available structured via `opensync explain <plugin>` and `opensync status --json` for scripting. `verify` exits non-zero only on real schema/integrity errors, never on "this content type doesn't translate to that agent".

## Commands (cobra)

| Verb | Behavior |
|---|---|
| `init` | Bootstrap `~/.opensync/`, auto-detect installed agents, optional initial `add --all` ingest. |
| `apply [--scope] [--project] [--dry-run] [--agent ...] [--force]` | Drift-check → render → two-phase atomic write → update state. `--force` overrides drift block. |
| `status [--agent ...]` | Drift report. Exit 0 clean / 1 drift / 2 error. |
| `diff [PATH]` | Unified diff of target vs destination. |
| `add PATH... [--agent] [--scope]` | Capture native file into source (canonicalize when possible; passthrough otherwise). |
| `mcp add/list/remove` | Edit `canonical/mcp/servers.toml`. |
| `marketplace add/list/remove` | Manage `canonical/marketplaces/registry.toml`. |
| `plugin install/enable/disable/list` | Manage `canonical/plugins/enabled.toml`. |
| `agents list/enable/disable [--purge]` | Manage which adapters opensync targets. |
| `reconcile [--auto-writeback\|--auto-override]` | Interactive drift resolver, per-key prompts. |
| `project init/list` | Register project under `projects/<slug>/`. |
| `edit TARGET` | Open canonical file in `$EDITOR`. |
| `verify` / `doctor` | Schema check / environment health. |

`apply` blocks on drift unless `--force`; `add` and the `mcp/plugin/marketplace` mutators only touch source state.

## Project structure (Go)

```
cmd/opensync/main.go
internal/cli/{root,apply,status,diff,add,init,mcp,plugin,marketplace,agents,reconcile,project,doctor}.go
internal/source/{source,schema,merge,passthrough,secrets,validate}.go
internal/adapter/adapter.go
internal/adapter/{claude,opencode,codex,cursor,continue_,gemini,aider}/...
internal/render/{pipeline,stage,provenance}.go
internal/diff/{tree,unified,report}.go
internal/drift/{detector,state}.go
internal/marketplace/{fetch,manifest,project,cache}.go
internal/config/paths.go        # OPENSYNC_HOME + OPENSYNC_TARGET_ROOT (test-only)
internal/iox/{atomic,lock}.go
docs/{architecture,adapters,schema}.md
.goreleaser.yaml
```

**Dependencies (pinned picks)**: cobra; pelletier/go-toml/v2 (TOML AST for write-back); tailscale/hujson (JSONC for OpenCode); sigs.k8s.io/yaml; sergi/go-diff; charmbracelet/huh + lipgloss; gofrs/flock; filippo.io/age; Masterminds/sprig/v3 + text/template; go-git/v5 with shell-out fallback; stdlib slog and errors.Join. Skip viper, promptui, hashicorp/go-multierror.

## Critical files to create

- `cmd/opensync/main.go` — cobra root + `OPENSYNC_HOME` / `OPENSYNC_TARGET_ROOT` resolution.
- `internal/adapter/adapter.go` — the `Adapter` interface + `Capability`, `File`, `Drift`, `KeyDrift`, `SourceEdit` types.
- `internal/source/schema.go` — canonical types (`MCPServer`, `Marketplace`, `PluginEnable`, `MemoryDoc`, `PermissionSet`).
- `internal/render/pipeline.go` — apply orchestration: drift gate → fan-out to adapters → two-phase atomic write → state update.
- `internal/drift/detector.go` — 3-way classifier described above.
- `internal/adapter/claude/{adapter,paths,settings,marketplace,plugins,memory,subagents,hooks,ingest}.go` — first full adapter.
- `internal/marketplace/{fetch,manifest,project}.go` — marketplace fetch + plugin→MCP projection.

## Milestones

- **M1 — Claude-only loop closes.** Module skeleton, source schema, adapter interface, Claude adapter (MCP, memory, subagents passthrough, hooks read-only), commands `init/apply/status/diff/mcp`, file-level + JSON per-key drift, atomic two-phase write, golden tests.
- **M2 — Marketplaces + OpenCode.** Marketplace fetch/cache, `marketplace`/`plugin` commands, Claude marketplace projection, OpenCode adapter (full), project scope (`opensync project init`, `apply --project`), plugin→MCP and plugin→memory projection helpers, translation-report rendering.
- **M3 — Codex + Cursor + Gemini + reconcile.** Three more adapters (MCP + memory + passthrough). `reconcile` interactive resolver. Write-back path for canonical and passthrough. Secrets backends (env/file/age). Plugin→MCP+memory enabled for all three; plugin→commands enabled for Cursor (rules); `explain` command.
- **M4 — v1 close.** Continue (YAML, hub best-effort, plugin→commands as prompt-rules), Aider (minimal: memory only). `edit`/`verify`/`doctor`. `agents enable/disable --purge`. Permissions canonical projection. `.opensync-ignore`. Docs. goreleaser cross-platform binaries.

Cross-agent subagent/skill/hook projection is explicitly **deferred to v2** — too much semantic translation to do well in v1. Slash-command projection ships in v1 only where there is a clean target (Cursor, Continue).

## Verification

- **`OPENSYNC_TARGET_ROOT` env var** redirects every adapter's destination root to a temp dir. Implement in `internal/config/paths.go` from day one. Lint rule forbids `os.UserHomeDir()` in `_test.go`.
- **Layers**: unit per package; golden-file renderer tests per adapter (`testdata/<case>/source` + `expected/`); round-trip `Ingest()→Project()` parity tests; the 8-case 3-way drift matrix per format; e2e shell script per adapter (`init → mcp add → apply → mutate destination → status drift → reconcile --auto-writeback → apply → clean`); concurrent-apply lock test; marketplace fetch test against a `file://` fake repo (no network).
- **Atomic always**: write `<target>.opensync.tmp` → fsync → rename. Backup pre-existing native files into `.state/backups/<ts>/` on first apply for easy rollback.
- **CI**: `go test -race ./...`, golangci-lint (govet, staticcheck, errcheck, gocritic), goreleaser dry-run for linux/macos/windows × amd64/arm64.

## Honest scope notes

- Seven full-depth adapters in v1 is unrealistic; M3/M4 deliberately ship Continue and Aider at minimal depth.
- Plugin → non-native-agent projection in v1 covers MCP servers (universal), instructional text as memory (universal), and slash commands → prompt-rules (Cursor + Continue only). Subagents, skills, and hooks are Claude/OpenCode-only and surfaced as "skipped" in the per-agent translation report rather than silently dropped.
- opensync does not run vendor install hooks; symlinks from marketplace cache to native plugin paths instead. Reduces coupling to vendor CLIs that may rev independently.
