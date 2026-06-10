# Capability matrix

What works, what's lossy, and what's deferred — per agent, per component. This
is the honest-expectations page for the beta. If a translation is lossy or
skipped, agentsync says so at apply time in the [translation report](concepts.md#translation-report--coverage);
nothing is dropped silently.

**Legend**

| Symbol | Meaning |
|:--:|---|
| ✓ | **native** — full fidelity; the agent has the concept directly |
| ◐ | **projected** — translated with documented, reported loss |
| ✗ | **skipped** — no honest translation; logged in the apply report |
| — | not yet implemented (adapter is registered but no-op) |

---

## Agent status

| Agent | Status (beta) | Notes |
|---|---|---|
| **Claude Code** | ✅ Full adapter | All seven components, including LSP. The reference implementation. Its installed **plugins + marketplaces** are captured by `import` (read from `enabledPlugins` / `extraKnownMarketplaces`); on `apply`, each plugin's components project to Claude's native paths and the enablement keys themselves are deliberately left untouched (`PluginIngester` is read-only — see the [shared invariant](#plugin-importapply-the-shared-invariant) below). |
| **OpenCode** | ✅ Adapter (some components projected/skipped) | MCP, memory, skills, subagents, commands. Hooks and LSP are skipped with a warning. No native plugin/marketplace concept, so nothing for plugin `import` to capture; it still *receives* plugin-projected components (skills, MCP, …) on `apply`. |
| **Codex CLI** | ✅ Adapter (some components projected) | MCP, memory, skills, subagents, slash commands, and hooks. MCP servers and hooks both merge into the TOML `~/.codex/config.toml` (as `[mcp_servers.*]` / inline `[hooks.*]` tables — Codex's documented equivalent to a separate `hooks.json`), so config.toml is the adapter's single key-merge file and the user's other keys (`model`, `sandbox_mode`, `[plugins.*]`, …) are preserved; subagents project to Codex's TOML agent format and slash commands to global-only custom prompts (both ◐). Codex **has a native plugin system**[^codex-plugins] with enable-state in `~/.codex/config.toml` as `[plugins."<name>@<source>"] enabled = …`, so the adapter implements `PluginIngester`: `import codex:plugin` captures that enable-state. The render never re-emits those tables (same [invariant](#plugin-importapply-the-shared-invariant) as Claude). Codex records no marketplace *fetch source* in a documented config location, so each plugin's marketplace is resolved from agentsync's own registered marketplaces (`agentsync marketplace add <source>` first), warning + skipping any it can't — exactly how Claude's auto-available built-in marketplace is handled. |
| **Cursor** | ✅ Adapter (some components projected) | MCP, memory, skills, subagents, slash commands, and hooks. MCP lands in `.cursor/mcp.json` (the same `mcpServers` shape as Claude — full fidelity) and hooks in `.cursor/hooks.json` (Claude's lifecycle events remapped to Cursor's camelCase names; the required top-level `version` is asserted when missing, never overwriting a user-set value; events with no Cursor equivalent are dropped with a report). Memory projects to the repo-root `AGENTS.md` at **project scope only** — Cursor keeps user-level rules in app-local storage, so user-scope memory has no filesystem target (reported as a skip). Subagents project to `.cursor/agents/<name>.md` (Claude's `tools`/`color` have no Cursor field and drop); slash commands to `.cursor/commands/<name>.md` (plain markdown — frontmatter drops). Only LSP is unsupported (Cursor has no LSP concept). Cursor **has a native plugin system**[^cursor-plugins], but where it records local enable-state is undocumented, so the adapter implements **no `PluginIngester` yet** (plugin *discovery* on `import` is deferred); it still *receives* plugin-projected components (skills, MCP, …) on `apply` like any agent. |
| **Gemini CLI** | ✅ Adapter (some components projected) | MCP, memory, subagents, slash commands, and hooks. MCP and hooks both merge into `.gemini/settings.json` (MCP `mcpServers` with Gemini's `url`/`httpUrl` transport split; hooks under `hooks`, the same nested shape as Claude, with events remapped to Gemini's `BeforeTool`/`AfterTool`/… and unmapped ones dropped) — so settings.json is the adapter's single key-merge file and the user's other keys (`theme`, `model`, …) are preserved. Memory projects to `GEMINI.md` (`~/.gemini/GEMINI.md` at user scope, repo-root `GEMINI.md` at project scope — full fidelity). Slash commands become `.gemini/commands/<name>.toml` (`description` + `prompt`; `argument-hint`/`allowed-tools` drop), subagents `.gemini/agents/<name>.md` (Claude's `tools` vocabulary differs from Gemini's, so it and `color` drop). **Skills** (Gemini uses extensions, not Agent Skills) and **LSP** have no Gemini concept and are skipped. Gemini has no native plugin enable-state agentsync models, so there is no `PluginIngester`; it still *receives* plugin-projected components on `apply`. |

## Plugin import/apply: the shared invariant

The rule is the same for **every** adapter, present and future:

> **`import` reads the agent's plugin enable-state for discovery; `apply`
> never writes it back. Apply fans out the plugin's _components_, not the
> plugin itself.**

agentsync's `Adapter` interface has a `Render` (canonical → native components)
and the optional `PluginIngester` extension is read-only (`IngestPlugins` —
native plugin enable-state → canonical). There is no `RenderPlugins`. Each
adapter handles the asymmetry the same way:

| Adapter | reads on import (`PluginIngester`)              | writes on apply (`Render`)                   |
|---|---|---|
| Claude  | `settings.json#/enabledPlugins`, `…#/extraKnownMarketplaces` | components only (skills, MCP, commands, …); enable-state keys left untouched |
| Codex   | `~/.codex/config.toml` `[plugins."<name>@<source>"]` | components only (MCP, hooks, memory, skills, …); `[plugins.*]` left untouched |
| OpenCode | — (no native plugin concept)                    | components only (receives plugin-projected components like any user-authored component) |
| Cursor  | — (PluginIngester deferred; native enable-state location undocumented) | components only (skills, MCP, commands, …) |
| Gemini  | — (no native plugin enable-state agentsync models; uses extensions) | components only (MCP, memory, commands, subagents, hooks) |

Once a plugin's components materialise at native paths (`~/.claude/skills/<name>/`,
`mcpServers` in the agent's config, `~/.codex/AGENTS.md`, …), the consumer
agent reads them through the same code path it uses for hand-authored
components. Plugin attribution is purely agentsync's internal bookkeeping. The
write-back is omitted on purpose: it would pick a fight with the agent's own
`/plugin disable` UI (ping-pong on every apply), blur ownership between
agentsync and the agent's plugin manager, and double-install with the agent's
own per-plugin install dir. See
[architecture.md § PluginIngester (read-only)](architecture.md#pluginingester-read-only).

---

## Component × agent

Component support across agents.

| Component | Claude | OpenCode | Codex | Cursor | Gemini |
|---|:--:|:--:|:--:|:--:|:--:|
| **MCP server** | ✓ `~/.claude.json` (user) · `.mcp.json` (project) | ✓ `opencode.json` | ✓ `config.toml` | ✓ `.cursor/mcp.json` | ✓ `.gemini/settings.json` |
| **Memory** | ✓ `CLAUDE.md` | ✓ `AGENTS.md` | ✓ `~/.codex/AGENTS.md` | ◐ `AGENTS.md` | ✓ `GEMINI.md` |
| **Skill** | ✓ `~/.claude/skills/X/` (dir) | ✓ shared `.claude/skills/` | ✓ `~/.agents/skills/` | ✓ `.cursor/skills/` | ✗ no skills concept |
| **Subagent** | ✓ `~/.claude/agents/X.md` | ◐ frontmatter munged | ◐ markdown → TOML | ◐ `.cursor/agents/` | ◐ `.gemini/agents/` |
| **Slash command** | ✓ `~/.claude/commands/X.md` | ◐ `argument-hint` dropped | ◐ `~/.codex/prompts/` | ◐ `.cursor/commands/` | ◐ `.gemini/commands/` (TOML) |
| **Hook** | ✓ JSON in settings | ✗ skip (JS/TS plugins) | ◐ `config.toml` `[hooks.*]` | ◐ `.cursor/hooks.json` | ◐ `settings.json` `hooks` |
| **LSP server** | ✓ native | ✗ skip (deferred) | ✗ no LSP concept | ✗ no LSP config | ✗ no LSP concept |

> The ◐/✗ cells are *features*, not bugs: agentsync refuses to invent a
> translation that would mislead you. Every ◐ and ✗ is printed in the apply
> report and queryable with `agentsync explain <plugin> --json`.

## Reading the report

Every `apply`, `verify`, and `explain` ends with a coverage report — per plugin,
per agent — using the same three marks:

```
plugin: atlassian@anthropic
  claude    ✓ full    (1 mcp, 5 commands)
  opencode  ◐ partial (1 mcp; 5 commands → projected)
```

- **✓ native** — the component landed with full fidelity.
- **◐ projected** — it landed, but with the documented loss below (e.g. an
  OpenCode slash command drops its `argument-hint`).
- **✗ skipped** — no honest translation exists, so nothing was written; the skip
  is logged, never silent.

---

## What each ◐ loses

Every projected (◐) cell above is a deliberate, reported translation. Here's what
doesn't carry over.

Note that **MCP/LSP capture is not field-lossy**: native server fields agentsync
doesn't model (e.g. `timeout`, `disabled`, `cwd`) are preserved verbatim through a
passthrough `[server.extra]` table on import/reconcile and re-rendered on apply,
rather than dropped. (`Extra` is verbatim only — `${secret:…}` there is written
literally, never resolved.)

**OpenCode**

- **Subagent** — Claude's `tools` allowlist is remapped onto OpenCode's
  `permission` model (approximate), and Claude frontmatter keys OpenCode doesn't
  recognize are dropped.
- **Slash command** — Claude's `argument-hint` has no OpenCode field and is
  dropped; there's no command-level `allowed-tools` (scoping is per-agent instead).

**Codex**

- **Subagent** — Codex custom agents are TOML, not markdown: the prose body
  becomes `developer_instructions`, the `description` + `model` frontmatter carry
  over, and there is no per-agent `tools` allowlist (tool scoping is only
  expressible via `[mcp_servers]` / skill toggles), so `tools` (and `color`) are
  dropped with a reported skip.
- **Slash command** — maps to Codex *custom prompts* (`~/.codex/prompts/*.md`),
  which do preserve `description` + `argument-hint`, but they're global-only, so a
  **project-scope** command has no target and is skipped; they also can't be
  namespaced in subdirectories, and the feature is deprecated in favor of skills.
- **Hook** — Codex mirrors Claude's declarative hook schema as inline `[hooks.*]`
  tables in `~/.codex/config.toml` (Codex reads hooks from either a `hooks.json`
  or inline `[hooks]` tables; agentsync uses the config.toml form so the adapter
  has a single key-merge file), but recognizes a fixed set of lifecycle events
  (SessionStart, SubagentStart, PreToolUse, PermissionRequest, PostToolUse,
  Pre/PostCompact, UserPromptSubmit, SubagentStop, Stop); Claude events outside
  that set (e.g. `SessionEnd`, `Notification`) have no target and drop.

**Cursor**

- **Memory** — project memory lands as `AGENTS.md`, but Cursor keeps *user-level*
  rules in app-local storage (not the filesystem), so user-scope memory has no
  projection target (it is reported as a skip).
- **Subagent** — markdown under `.cursor/agents/`. Cursor recognizes
  `name`/`description`/`model`/`readonly`/`is_background`; Claude's `tools`
  allowlist and `color` have no Cursor field and are dropped with a report.
- **Slash command** — Cursor commands (`.cursor/commands/*.md`) are plain markdown
  with no frontmatter, so `argument-hint`, `description`, and `allowed-tools` are
  all dropped — only the prompt body survives.
- **Hook** — Cursor uses a declarative `.cursor/hooks.json`, but with its own
  camelCase event names and a flat entry shape, so Claude's events are remapped
  (`PreToolUse`→`preToolUse`, `UserPromptSubmit`→`beforeSubmitPrompt`, …) and any
  with no Cursor equivalent (e.g. `Notification`, `PostCompact`) are dropped with a
  report. The required top-level `version` is asserted if missing and a user-set
  value is preserved. agentsync models only Cursor's `command` hooks and only the
  `command`/`matcher`/`type` entry fields; on `import`, a Cursor-native event
  agentsync can't render (`afterFileEdit`, `beforeShellExecution`, …) — or an
  event containing an entry it can't fully represent (a `prompt`-type hook, or
  fields like `timeout`/`failClosed`) — is left uncaptured with a warning, so a
  later `apply` never takes ownership of an array it would lossily rewrite.

**Gemini CLI**

- **Subagent** — markdown under `.gemini/agents/`. Gemini recognizes
  `name`/`description`/`model` (plus Gemini-only `kind`/`temperature`/`max_turns`/…
  agentsync doesn't model); Claude's `tools` list uses a *different tool vocabulary*
  (`read_file`/`grep_search`, not `Read`/`Grep`), so copying it verbatim would name
  tools Gemini doesn't have — it and `color` are dropped with a report. `name` is
  defaulted to the filename when absent (Gemini requires it).
- **Slash command** — Gemini commands are TOML (`.gemini/commands/*.toml`) with
  `description` + `prompt`. The body becomes `prompt` and `description` carries
  over; `argument-hint`/`allowed-tools` have no Gemini field and drop. Gemini's
  argument placeholder is `{{args}}` (not Claude's `$ARGUMENTS`/`$1`); the body is
  written verbatim, so placeholder syntax is not auto-translated.
- **Hook** — Gemini hooks live in `settings.json` under `hooks` in the *same nested
  shape* as Claude, so only the event name is remapped (`PreToolUse`→`BeforeTool`,
  `PostToolUse`→`AfterTool`, `UserPromptSubmit`→`BeforeAgent`, `Stop`→`AfterAgent`,
  `PreCompact`→`PreCompress`); events with no Gemini equivalent (`SubagentStart`/
  `SubagentStop`/`PostCompact`/`PermissionRequest`) are dropped with a report.

## Why OpenCode skips hooks and LSP

- **Hooks** — OpenCode hooks are JS/TS plugins that subscribe to events, not
  declarative shell commands like Claude's. There is no mechanical translation;
  hand-author a small plugin if you need a hook on OpenCode.
- **LSP** — OpenCode does have a native `lsp` config, but agentsync defers
  projecting LSP servers beyond Claude to a later release; today you'll see
  `lsp server X skipped` in the report on non-Claude agents.

## How OpenCode MCP servers are projected

OpenCode's native MCP schema differs from the canonical model, so the adapter
translates rather than copying fields verbatim:

- **transport** — canonical `type = "stdio"` → OpenCode `"type": "local"`;
  `"http"`/`"sse"` → `"type": "remote"`. OpenCode has no separate SSE transport,
  so a `sse` server normalizes to `http` if it is later captured back via
  `import`/`reconcile` (an apply-only flow is unaffected).
- **command** — canonical `command` + `args` are flattened into OpenCode's single
  `command` string array (`["npx", "-y", "pkg"]`), and split back on ingest.
- **environment** — canonical `env` is written under OpenCode's `environment`
  key (not `env`).
- **remote** — `url` and `headers` carry through unchanged.

## Full-fidelity projections (✓ with a transform)

A few ✓ cells still change shape on the way out — same content, no loss:

- **Codex MCP** — Claude's JSON `mcpServers` become TOML `[mcp_servers.X]` (stdio
  and streamable-HTTP both representable).
- **Cursor MCP** — `.cursor/mcp.json` uses the same `mcpServers` shape as Claude,
  down to `${env:…}` references.
- **Gemini MCP** — `.gemini/settings.json` `mcpServers`: stdio keeps
  command/args/env; a remote server uses Gemini's transport split — `url` for SSE,
  `httpUrl` for HTTP streaming — both round-tripping the canonical `type`.
- **Codex & Gemini memory** — the same markdown lands at `~/.codex/AGENTS.md` /
  `~/.gemini/GEMINI.md` (repo-root `GEMINI.md` at project scope).
- **Skills (Codex & Cursor)** — the same skill *directory* per the
  [Agent Skills](https://agentskills.io) spec: `SKILL.md` (name + description)
  **plus any bundled `scripts/`/`references/`/`assets/` and nested files**, all
  carried verbatim (binary included, executable bit preserved) on apply, import,
  and reconcile — agentsync is not lossy for anything but the directory itself.
  Removing a skill (or one bundled file) from the source reclaims it from each
  destination on the next `apply` (drifted files backed up first; empty dirs
  pruned). Codex installs them under `~/.agents/skills/` (enabled by default — no
  feature flag), Cursor under `.cursor/skills/`, and both also read the shared
  `.claude/skills/`.

## Escape hatches

You control fan-out explicitly:

- `agents = ["claude", "opencode"]` on an MCP server or plugin entry → fan out
  only to those agents.

(A per-component `[plugin.overrides.<agent>]` skip was specced but is **not
wired in v1** — the projector does not consult it. Use the `agents` allowlist.)

---

## Known limits

These are documented trade-offs, not regressions. The authoritative list lives
in the [README](../README.md#known-limits); the highlights:

- **Comment preservation** — comments in `mcp/*.toml`, in `opencode.json`, and in
  Codex's `~/.codex/config.toml` are not preserved across a write-back/import
  round-trip in the rewritten section.
- **Owned-key hand-edits** — if you hand-edit an agentsync-owned key in a shared
  file, the next `apply` overwrites it with no backup (agentsync considers it its
  own). Run `agentsync reconcile` first to capture the edit.
- **Insecure sources** — `http://` and `git://` plugin/marketplace sources are
  rejected by default (MITM protection); override with
  `AGENTSYNC_ALLOW_INSECURE_URLS=1`.
- **Symlinked destinations** are rejected by default; override with
  `AGENTSYNC_ALLOW_SYMLINK_DEST=1`.
- **Planned (not yet implemented)**: Continue, Aider.

See the [user guide](user-guide.md) to put this into practice.

[^codex-plugins]: Codex plugin system:
    [developers.openai.com/codex/plugins](https://developers.openai.com/codex/plugins).
    Enable-state lives in `~/.codex/config.toml` under
    `[plugins."<name>@<source>"]` tables (an `enabled` bool) — the same
    `name@source` shape as Claude's `enabledPlugins`, so a future Codex
    `PluginIngester` parses those tables (plus its marketplace sources) into the
    same `NativeMarketplace` / `NativePlugin` descriptors `import` already
    consumes.

[^cursor-plugins]: Cursor plugin system:
    [cursor.com/docs/reference/plugins](https://cursor.com/docs/reference/plugins).
    Plugins bundle rules, skills (`SKILL.md`), agents, commands, hooks, and MCP
    servers; the manifest is `.cursor-plugin/plugin.json` and multi-plugin repos
    use `.cursor-plugin/marketplace.json` — nearly identical to Claude's
    `.claude-plugin/*`, so agentsync's projection layer largely transfers. Not
    yet documented: where Cursor records which plugins are installed/enabled
    locally (the `enabledPlugins`-equivalent a `PluginIngester` would read);
    given Cursor keeps user rules in app-local storage, this may not be a plain
    config file. The Cursor adapter therefore ships **without** a `PluginIngester`
    (plugin discovery on `import` is deferred until that location is documented);
    it still fans out plugin *components* on `apply` like every other adapter.
