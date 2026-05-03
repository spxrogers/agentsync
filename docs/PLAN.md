# opensync — Implementation Plan

## Context

Engineers using multiple coding-agent CLIs (Claude Code, OpenCode, Cursor, Codex) accumulate near-identical configuration sprawled across `~/.claude/`, `~/.config/opencode/`, `~/.cursor/`, `~/.codex/` and per-project mirrors. Each agent has its own settings format, MCP registry, plugin model (or none), rules format, and memory file. There is no single command for "this is the MCP server set I want everywhere" or "this skill applies to all my agents."

opensync is that command. It borrows chezmoi's three-state model (source → target → destination) but generalises the destination from "one home directory" to "N agent-specific config trees," with a canonical **intermediate representation (IR)** in the middle and per-agent adapters at the edges. The IR is a language-neutral set of Go structs (`Plugin`, `MCPServer`, `Skill`, …) that each agent's idiosyncratic config parses *into* and renders *out of* — so one canonical plugin definition fans out to four different agents. Plugins/marketplaces are first-class: the user commits a plugin once, runs `opensync apply`, every registered agent picks it up, and `opensync status` flags any drift introduced out-of-band.

**Confirmed design decisions:**
- **Language: Go.** cobra+viper CLI, `bbolt` for state, sprig+`text/template`, `go-git` for source/marketplace sync, `afero` for filesystem fakes.
- **MVP agents:** Claude Code, OpenCode, Cursor, OpenAI Codex CLI (all four).
- **Plugin model:** Adopt Claude Code's `.claude-plugin/marketplace.json`/`plugin.json` shape as the canonical IR. Per-agent adapters translate to native files for agents without a native plugin concept.
- **Drift:** On-demand only via `status`/`diff`/`apply`/`re-add`; no daemon.

## Architecture

```
+------------------------+        +-------------------+        +----------------------------+
|  Source repo (git)     |        |  Canonical IR     |        |  Per-agent destinations    |
|  $OPENSYNC_HOME        |        |  (Go structs)     |        |                            |
|                        |        |                   |        |  ~/.claude/...             |
|  plugins/              | -----> | Plugin            | -----> | ~/.config/opencode/...     |
|  mcp-servers/          | parse  | MCPServer         | adapt  | ~/.cursor/...              |
|  skills/, agents/      |        | Skill, Agent      |        | ~/.codex/...               |
|  settings/*.tmpl       | render | Hook, Rule        |        | <project>/.claude/...      |
|  memory/, projects/    |        | Settings          |        | <project>/.cursor/...      |
+------------------------+        +-------------------+        +----------------------------+
        ^                                    |                              |
        | re-add                             | hash                         | hash
        |                                    v                              v
        |                          +---------------------------------------------+
        +--------------------------|  BoltDB ($OPENSYNC_HOME/.state.db)          |
                                   |  applied_files | applied_keys | plugins ... |
                                   +---------------------------------------------+
```

Three states made explicit:
- **Source** — what the user committed in `$OPENSYNC_HOME` (a git repo).
- **Target** — the rendered output of templating + IR-to-agent translation, computed in memory at apply time.
- **Destination** — what is actually on disk under each agent's config dirs right now.

Drift = `hash(destination) != stored_hash(applied)`. Source change = `hash(target) != stored_hash(applied)`. Both → conflict.

## Source repo layout

`$OPENSYNC_HOME` defaults to `~/.local/share/opensync` (overridable via env).

```
$OPENSYNC_HOME/
├── opensync.toml                         # registered agents, defaults, project map
├── .state.db                             # BoltDB (gitignored)
├── .gitignore
├── marketplaces/<slug>/                  # cached marketplace clones (gitignored)
├── plugins/<name>/                       # canonical plugins, Claude marketplace shape
│   ├── .claude-plugin/plugin.json
│   ├── skills/<skill>/SKILL.md
│   ├── agents/*.md
│   ├── commands/*.md
│   ├── hooks/hooks.json
│   └── mcp/<server>.json
├── mcp-servers/<name>.toml               # standalone canonical MCP defs
├── skills/<name>/SKILL.md                # standalone (not in a plugin)
├── agents/<name>.md
├── rules/<name>.md
├── settings/
│   ├── global.toml.tmpl                  # rendered to each agent's user-scope settings
│   └── project.toml.tmpl                 # rendered to each agent's project-scope settings
├── memory/
│   ├── CLAUDE.md.tmpl                    # canonical memory; rendered per agent
│   └── AGENTS.md.tmpl
└── projects/<slug>/                      # per-project overrides; mirrors layout above
    ├── opensync.toml
    ├── memory/CLAUDE.md.tmpl
    └── settings/project.toml.tmpl
```

`plugins/` mirrors Claude's marketplace shape verbatim so plugins authored once can be shipped to a marketplace unchanged. `mcp-servers/` covers the common case of "I just want this server everywhere" without authoring a full plugin. `settings/*.tmpl` lets per-agent forking be one `{{ if eq .agent.name }}` away.

## Canonical IR (intermediate representation; `internal/ir/`)

The IR is the universal vocabulary. Every adapter speaks it. `source/loader.go` reads `$OPENSYNC_HOME` and produces an IR `Bundle`; each adapter's `Plan(bundle)` translates *out* of IR into its agent's on-disk files; each adapter's `Read()` translates the agent's on-disk files back *into* IR (used by `re-add` and `status`).

```go
type Plugin struct {
    Name, Version, Description string
    Source     Source           // marketplace+ref OR local path
    Skills     []Skill
    Agents     []Agent
    Commands   []Command
    Hooks      []Hook
    Rules      []Rule
    MCPServers []MCPServer
    Enabled    bool
    Scope      Scope            // user | project
}
type Source struct { Kind, Marketplace, Ref, Path string } // kind: marketplace|git|local
type Scope int; const ( ScopeUser Scope = iota; ScopeProject )

type MCPServer struct {
    Name, Type string                 // stdio|http|sse
    Command    string; Args []string  // stdio
    Env        map[string]string
    URL        string                 // http|sse
    Headers    map[string]string
    Enabled    bool; Scope Scope
}
type Skill struct { Name, Description string; Frontmatter map[string]any; Body string; Assets []Asset }
type Agent struct { Name, Description string; Frontmatter map[string]any; Body string }
type Command struct { Name, Body string }
type Hook struct { Event, Matcher, Type, Command string }
type Rule struct { Name, Body string; Globs []string; AlwaysOn bool; Frontmatter map[string]any }
type Memory struct { Body string }
type Settings struct { Raw map[string]any }
type Asset struct { RelPath string; Bytes []byte; Mode uint32 }

// Bundle is what an adapter consumes for one (agent, scope) pair.
type Bundle struct {
    Agent      string; Scope Scope
    Plugins    []Plugin
    MCPServers []MCPServer    // standalone + flattened from plugins
    Skills     []Skill
    Agents     []Agent
    Rules      []Rule
    Hooks      []Hook
    Memory     Memory
    Settings   Settings
}
```

## Adapter interface (`internal/adapters/`)

```go
type Adapter interface {
    Name() string                                              // "claude-code" | ...
    Capabilities() Capabilities                                // what IR components this adapter can consume
    Detect(env Env) (bool, error)                              // is this agent installed?
    Paths(scope ir.Scope, project string) Paths                // resolve config dirs
    Read(scope ir.Scope, project string) (ir.Bundle, error)    // for re-add and drift
    Plan(target ir.Bundle, scope ir.Scope, project string) ([]FileOp, []Skip, error)
    Apply(ops []FileOp) error
}
type Capabilities struct { MCP, Skills, Agents, Commands, Hooks, Rules, Memory bool }
type Skip struct { Component, Name, Reason string }            // surfaced in --dry-run / --strict
type FileOp struct { Path, Action string; Content []byte; Mode uint32; Reason, SourceID string }
type Paths struct { Settings, MCP, PluginsDir, SkillsDir, AgentsDir, RulesDir, Memory string }
```

**Per-adapter mapping:**
- **claudecode**: settings → `~/.claude/settings.json` (user) or `<proj>/.claude/settings.json` (project). MCP → `mcpServers` in same file plus `~/.claude.json` for user scope. Plugins copied to `~/.claude/plugins/<name>/` preserving canonical structure. Skills → `<scope>/.claude/skills/<name>/SKILL.md`. Agents → `agents/<name>.md`. Hooks merged into settings. Memory → `CLAUDE.md`. Rules → `.claude/rules/<name>.md`.
- **opencode**: settings → `~/.config/opencode/opencode.json` (JSONC; round-trip via hujson/jsonc). MCP → `mcp` key. Plugins → `~/.config/opencode/plugins/<name>/`; skills/agents/commands → `.opencode/{skills,agents,commands}/`. Memory → `AGENTS.md` at project root for project scope.
- **cursor**: no plugin concept — `Apply(plugin)` extracts only `MCPServers` and `Rules`, logs the rest as skipped. MCP → `~/.cursor/mcp.json` or `<proj>/.cursor/mcp.json`. Rules → `<scope>/.cursor/rules/<name>.mdc` with frontmatter (`description`, `globs`, `alwaysApply`).
- **codex**: TOML only. MCP → `[mcp_servers.<name>]` tables in `~/.codex/config.toml` or `<proj>/.codex/config.toml`. Memory → `AGENTS.md` at project root (and `~/.codex/AGENTS.md` user scope). Plugins flatten to MCP + rules-into-AGENTS.md.

Edge cases each adapter must handle: **comment preservation in JSONC/TOML round-trip**, **idempotent merges with hand-edited keys outside the opensync-managed set** — we only delete keys we previously wrote, tracked via `applied_keys` (below).

## Cross-agent plugin translation (Claude-only marketplaces)

A marketplace is just a registry; each plugin's manifest is what actually fans out. When opensync installs a plugin, every registered adapter is asked "what can you consume from this?" — translation is per-component, not per-marketplace, and unsupported pieces are skipped with explicit warnings (not silent drops).

| Component | Claude Code | OpenCode | Cursor | Codex |
|---|---|---|---|---|
| MCP servers | native | native | native | native (`[mcp_servers.X]`) |
| Rules | `.claude/rules/*.md` | rules/memory | `.cursor/rules/*.mdc` | appended to `AGENTS.md` |
| Memory (CLAUDE.md) | `CLAUDE.md` | rendered as `AGENTS.md` | – | rendered as `AGENTS.md` |
| Skills | native | native | skipped (warn) | skipped (warn) |
| Subagents | native | native | skipped (warn) | skipped (warn) |
| Slash commands | native | native | skipped (warn) | skipped (warn) |
| Hooks | native | partial (only matching events) | skipped (warn) | skipped (warn) |

A Claude-authored marketplace whose plugins are mostly MCP + rules fans out cleanly to all four agents. A skill/hook-heavy marketplace lands fully on Claude Code with a per-component skip report for the others. `opensync apply --dry-run` always prints exactly what each adapter will drop before any write happens.

**Escape hatches:**
- **`targets`** in `opensync.toml` (per plugin or per marketplace) — explicit allowlist, e.g. `targets = ["claude-code", "opencode"]` to keep a Claude-specific plugin out of Codex/Cursor entirely.
- **`--strict`** flag on `apply` — turns "would-skip" warnings into hard errors so the user must acknowledge skips by either editing `targets` or accepting them.
- Per-adapter `Capabilities()` method advertises what the adapter can consume; the planner consults this so adding a new adapter (e.g. Aider) automatically gets correct skip behaviour without touching plugin code.

## Persistent state (BoltDB at `$OPENSYNC_HOME/.state.db`, gitignored)

| Bucket | Key | Value (JSON) |
| --- | --- | --- |
| `applied_files` | `<agent>/<scope>/<project>/<dest_path>` | `{sha256, mode, applied_at, source_id}` |
| `applied_keys` | `<agent>/<scope>/<project>/<file>:<json_pointer>` | `{sha256_of_value, applied_at, source_id}` |
| `marketplaces` | `<slug>` | `{url, ref, fetched_at, head_sha}` |
| `plugins` | `<marketplace>/<name>` | `{version, enabled, scope, manifest_sha}` |
| `meta` | `schema_version` | int |

`applied_keys` is the trick that lets opensync merge into shared files (e.g. `~/.claude/settings.json`) without clobbering hand-written user keys: it records the JSON pointers it wrote, so on next apply it can diff/remove only its own entries.

## CLI surface (cobra)

```
# Source repo bootstrap
opensync init [<git-url>]                                    # clone or scaffold $OPENSYNC_HOME
opensync update                                              # git pull source
opensync doctor                                              # env + adapter detection

# Agent registration
opensync agent {add,remove,list}

# Apply / inspect / capture drift
opensync apply [--agent X] [--scope user|project|all] [--project <slug>] [--dry-run]
opensync status [--agent X]
opensync diff [--agent X] [<path>]
opensync re-add <selector>                                   # e.g. codex:mcp:filesystem
opensync merge <path>                                        # 3-way interactive

# Marketplaces & plugins (Claude marketplace.json shape)
opensync marketplace {add,remove,list,refresh}
opensync plugin {add,remove,list,enable,disable}

# Standalone primitives (no plugin needed)
opensync mcp {add,remove,list,enable,disable}
opensync skill {add,remove,list}

# Project-local
opensync project {init,list}
```

Source-mutating commands: `init`, `update`, `agent add/remove`, `re-add`, `marketplace *`, `plugin *` (except list), `mcp` (except list), `skill add/remove`, `project init`. Destination-mutating: `apply`, `merge`. Read-only: everything else.

Examples: `opensync apply --agent codex --scope user --dry-run`; `opensync re-add codex:mcp:filesystem`; `opensync plugin add anthropics/claude-plugins:web-tools --scope project`.

## Templating

Engine: `text/template` + sprig v3. Files ending `.tmpl` are rendered; others copied verbatim.

Variables exposed to templates:
```
.agent.name           "claude-code" | "opencode" | "cursor" | "codex"
.agent.version        detected version (best-effort)
.agent.paths          resolved Paths struct
.scope                "user" | "project"
.project.slug         "" or "<slug>"
.project.root         absolute path or ""
.os, .arch, .hostname
.user.name, .user.home
.env.<KEY>            allow-listed env vars only
.opensync.home, .opensync.version
```

Layering (last write wins): `settings/global.toml.tmpl` → `settings/project.toml.tmpl` → `projects/<slug>/settings/*.tmpl`. Per-agent forks via `{{ if eq .agent.name "claude-code" }}…{{ end }}`. Sprig defaults minus `env`/`expandenv` (replaced with allow-listed `.env`).

## Drift detection algorithm

For each managed destination file (`applied_files` rows + new IR-derived ones):

```
H_src     = sha256(target_bytes)              // computed now from IR + templates
H_applied = state.applied_files[key].sha256   // last write opensync did, or nil
H_dest    = sha256(read_or_nil(dest_path))

case (H_src, H_applied, H_dest):
  (a, a, a)              -> noop
  (a, a, b) and a!=b     -> DRIFT (dest changed under us)
  (b, a, a) and a!=b     -> SOURCE_CHANGED (apply overwrites cleanly)
  (b, a, c) all distinct -> CONFLICT (3-way; refuse without --force)
  (a, nil, _)            -> NEW (first-time write)
  (nil, a, a)            -> ORPHAN (delete on apply)
  (nil, a, b) and a!=b   -> ORPHAN_DRIFTED (warn, suggest re-add)
```

`status` prints the case for every managed file. `apply` acts on `noop`, `SOURCE_CHANGED`, `NEW`, `ORPHAN`. It refuses `DRIFT`/`CONFLICT`/`ORPHAN_DRIFTED` without `--force` and prints a `re-add` hint. The same algorithm runs at JSON-pointer granularity (using `applied_keys`) for files we only partially own (settings JSON).

## Project-local resolution

Order:
1. CLI `--project <slug>` wins.
2. Walk up from cwd for a `.opensync` marker file/dir.
3. Look up cwd in `opensync.toml`'s `[projects]` map (`projects.<slug>.path = "/abs/path"`).
4. Else scope = user only.

IR assembly when a project is active:
```
base IR    := load($OPENSYNC_HOME/{plugins,mcp-servers,skills,agents,rules})
project IR := load($OPENSYNC_HOME/projects/<slug>/...)
final IR   := merge(base, project)         // project wins on name collision
final IR.MCPServers = filter(enabled where scope matches)
```

Per-project enable/disable in `projects/<slug>/opensync.toml`:
```toml
[plugins]
enabled  = ["web-tools", "github-mcp"]
disabled = ["screenshot"]
[mcp_servers]
enabled  = ["filesystem-scoped"]
```

## `re-add` workflow (worked example)

User runs `codex mcp add filesystem -- npx -y @modelcontextprotocol/server-filesystem /tmp` directly.
1. `opensync status` → codex adapter `Read()` parses TOML, IR contains `MCPServer{Name:"filesystem"}`. State DB has no entry pointing at it → **UNMANAGED**.
2. Output: `codex (user) UNMANAGED ~/.codex/config.toml#mcp_servers.filesystem` with hint `opensync re-add codex:mcp:filesystem`.
3. `opensync re-add codex:mcp:filesystem` parses selector → asks codex adapter for the IR → source writer renders canonical TOML at `$OPENSYNC_HOME/mcp-servers/filesystem.toml` → records sha256 in `applied_keys`. `--all-agents` flag re-applies it elsewhere.
4. Next `opensync status` is clean; `opensync apply` is a no-op.

Other selectors: `claude-code:plugin:web-tools`, `cursor:rule:no-emojis`, `opencode:skill:diagram-builder`.

## Project structure (Go module)

```
opensync/
├── cmd/opensync/main.go              # cobra root, DI wiring
├── internal/
│   ├── cli/                          # one file per top-level command
│   │   ├── root.go, apply.go, status.go, readd.go, doctor.go
│   │   └── plugin.go, marketplace.go, mcp.go, skill.go, project.go
│   ├── source/                       # load/write $OPENSYNC_HOME
│   │   ├── repo.go                   # opensync.toml parsing
│   │   ├── loader.go                 # walks dirs -> IR
│   │   └── writer.go                 # used by re-add + add commands
│   ├── ir/                           # canonical structs
│   ├── adapters/
│   │   ├── adapter.go (interface), registry.go
│   │   ├── claudecode/, opencode/, cursor/, codex/
│   ├── state/                        # bbolt wrapper, schema migrations
│   ├── template/                     # render(), funcMap (sprig + custom)
│   ├── diff/                         # 3-way hash, file diff renderer, merger
│   ├── git/                          # go-git for source + marketplaces
│   ├── fs/                           # afero.Fs provider (real vs in-memory)
│   └── log/                          # slog wrapper
├── testdata/                         # golden files per adapter
├── go.mod
├── Makefile
└── README.md
```

`cli` is thin and only translates flags into calls into `source`, `adapters`, `state`. `source` and `adapters` are the two halves of the IR; `diff`+`state` glue them. `template` and `git` split out for mockability.

## Phased delivery

- **v0.1.0 — skeleton + Claude Code, user scope.** cobra+viper; `init` scaffolds `$OPENSYNC_HOME`; BoltDB state; templating; `claudecode` adapter `Detect/Read/Plan/Apply`; commands: `init`, `apply`, `status`, `diff`, `doctor`. Plugins authored locally under `plugins/`; no marketplaces yet.
- **v0.2.0 — Codex adapter + standalone MCP.** `codex` adapter (pure TOML). `mcp add/remove/list/enable/disable`. Confirms IR generality.
- **v0.3.0 — Cursor + OpenCode.** Cursor's no-plugin case forces the "translate to MCP+rules" path; OpenCode tests the JSONC writer. Skills, agents, rules end-to-end.
- **v0.4.0 — Marketplaces.** `marketplace add/remove/list/refresh`, plugin install via `go-git`, `plugins` BoltDB bucket, version pinning.
- **v0.5.0 — Drift + re-add.** Full case table; `re-add <selector>`; `merge` invokes `$EDITOR` with 3-pane temp files.
- **v0.6.0 — Project-local.** `projects/<slug>/`, `.opensync` marker, override layering, `--project` flag, project memory rendering.
- **v0.7.0 — Polish.** `doctor` adapter detection, shell completion, JSON output mode, goreleaser, managed-settings precedence on macOS/Windows.

## Testing strategy

- **Unit**: every IR loader and adapter `Plan` is table-driven with `testdata/<adapter>/<case>/{source,expected_dest,state_in.json,state_out.json}` over `afero.MemMapFs`.
- **Adapter golden files**: `Plan` returns `[]FileOp`; serialise and diff against `golden.json`. `make update-golden` re-records.
- **Round-trip**: `Read(Apply(target)) == target` for every adapter.
- **Drift cases**: synthetic state DB + on-disk fixtures exercise all eight cases.
- **End-to-end**: `e2e_test.go` boots tmpdir `$OPENSYNC_HOME` and tmpdir `$HOME`, runs binary via `os/exec`. One per adapter; one cross-adapter test that adds an MCP server and confirms it appears in all four agents.
- **Fuzz**: JSONC and TOML round-trip fuzzers to ensure comment preservation does not corrupt user-managed keys.

## Manual end-to-end verification

```bash
export OPENSYNC_HOME=$HOME/.local/share/opensync
opensync init
opensync agent add claude-code && opensync agent add codex
opensync agent add cursor      && opensync agent add opencode
opensync doctor                                            # all four detected

opensync marketplace add https://github.com/anthropics/claude-plugins
opensync plugin add anthropics/claude-plugins:web-tools
opensync mcp add github --command npx --args "-y,@modelcontextprotocol/server-github"
opensync apply --scope user

# Verify
jq '.mcpServers.github' ~/.claude/settings.json
grep -A3 '\[mcp_servers.github\]' ~/.codex/config.toml
jq '.mcpServers.github' ~/.cursor/mcp.json
jq '.mcp.github' ~/.config/opencode/opencode.json

# Drift
codex mcp add scratch -- npx -y some-server
opensync status                                            # shows UNMANAGED scratch
opensync re-add codex:mcp:scratch
ls $OPENSYNC_HOME/mcp-servers/scratch.toml
opensync apply                                             # propagates to others; no-op for codex

# Project-local
cd ~/code/myproj && touch .opensync
opensync project init myproj
opensync plugin add anthropics/claude-plugins:web-tools --scope project
opensync apply
ls .claude/plugins/web-tools/
```

## Critical files to be created

- `/home/user/opensync/cmd/opensync/main.go` — cobra root + DI wiring
- `/home/user/opensync/internal/ir/ir.go` — canonical structs (§ Canonical IR)
- `/home/user/opensync/internal/adapters/adapter.go` — `Adapter` interface
- `/home/user/opensync/internal/adapters/claudecode/adapter.go` — first concrete adapter (v0.1)
- `/home/user/opensync/internal/state/bolt.go` — BoltDB schema + migrations
- `/home/user/opensync/internal/source/loader.go` — walks `$OPENSYNC_HOME` → IR
- `/home/user/opensync/internal/diff/three_way.go` — drift case table
- `/home/user/opensync/go.mod`, `Makefile`, `README.md`

## Open risks / unknowns

1. **Claude plugin manifest schema is not fully documented.** Plan: load `plugin.json` as `map[string]any`, pass through verbatim, validate only the keys we touch (`name`, `version`, `mcpServers`, `skills`, `agents`, `commands`, `hooks`). Tighten post-v0.1 when we have real-world samples.
2. **OpenCode plugins can be filesystem dirs OR npm packages.** Plan: v0.3 supports filesystem only; npm-package plugins are represented in IR as a stub the adapter writes into the `plugin` config field, deferring fetch to OpenCode itself.
3. **Cursor has no plugin concept.** Plan: adapter explicitly drops skills/agents/hooks/commands and emits `WARN: cursor cannot consume X (skipped)` in the apply plan; documented in adapter README.
4. **JSONC/TOML comment preservation across round-trips** is essential for non-destructive merges into hand-edited files. Plan: `github.com/tailscale/hujson` for JSONC and `github.com/pelletier/go-toml/v2` AST mode for TOML; if a destination uses unsupported comment placement, fall back to `# opensync:begin` / `# opensync:end` managed-block markers and only manage content inside.
5. **Managed-settings precedence on macOS/Windows** (Claude Code reads platform-specific managed-settings paths). Plan: v0.1 implements user/project only; managed-settings is a v0.7 enhancement, documented up front so users are not surprised.
