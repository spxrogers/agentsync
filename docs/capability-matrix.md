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

| Agent | Status in v1.0 / beta | Notes |
|---|---|---|
| **Claude Code** | ✅ Full adapter | All seven components, including LSP. The reference implementation. Its installed **plugins + marketplaces** are captured by `import` (read from `enabledPlugins` / `extraKnownMarketplaces`), then projected to native paths on `apply` — agentsync deliberately does NOT write enabledPlugins / extraKnownMarketplaces back, because once each plugin's components materialise at `~/.claude/skills/<name>/`, `mcpServers` in `.claude.json`, etc. Claude Code serves them as regular components and doesn't need plugin-manager metadata. The asymmetry keeps agentsync from ping-ponging against the user's `/plugin disable` in Claude's own UI. Codex also captures plugin enable-state via `import` (see its row); the planned Cursor adapter has a native plugin system too and should do the same. |
| **OpenCode** | ✅ Adapter (some components projected/skipped) | MCP, memory, skills, subagents, commands. Hooks and LSP are skipped with a warning. No native plugin/marketplace concept, so nothing for plugin `import` to capture; it still *receives* plugin-projected components (skills, MCP, …) on `apply`. |
| **Codex CLI** | ✅ Adapter (some components projected) | MCP, memory, skills, subagents, slash commands, and hooks. MCP servers and hooks both merge into the TOML `~/.codex/config.toml` (as `[mcp_servers.*]` / inline `[hooks.*]` tables — Codex's documented equivalent to a separate `hooks.json`), so config.toml is the adapter's single key-merge file and the user's other keys (`model`, `sandbox_mode`, `[plugins.*]`, …) are preserved; subagents project to Codex's TOML agent format and slash commands to global-only custom prompts (both ◐). Codex **has a native plugin system**[^codex-plugins] with enable-state in `~/.codex/config.toml` as `[plugins."<name>@<source>"] enabled = …`, so the adapter implements `PluginIngester`: `import codex:plugin` captures that enable-state. Codex records no marketplace *fetch source* in a documented config location, so each plugin's marketplace is resolved from agentsync's own registered marketplaces (`agentsync marketplace add <source>` first), warning + skipping any it can't — exactly how Claude's auto-available built-in marketplace is handled. |
| **Cursor** | 🔜 Planned | Registered as a no-op. Planned coverage is broad — MCP, memory, skills, subagents, slash commands, and hooks all project (see the matrix); only LSP is unsupported. User-level rules/memory live in Cursor's app-local storage, so those stay project-scope. Cursor **has a native plugin system**[^cursor-plugins] (rules, skills, agents, commands, hooks, MCP servers) whose `.cursor-plugin/marketplace.json` + `.cursor-plugin/plugin.json` schema is almost identical to Claude's `.claude-plugin/*` — so projection largely carries over and the adapter should support `import <agent>:plugin` too. The open question is purely *where Cursor records local enable-state* (undocumented, possibly app-local like its rules); the plugin/marketplace content schema is already a near-match. |

---

## Component × agent

Component support across agents.

| Component | Claude | OpenCode | Codex | Cursor[^planned] |
|---|:--:|:--:|:--:|:--:|
| **MCP server** | ✓ `~/.claude.json` | ✓ `opencode.json` | ✓ `config.toml` | ✓ `.cursor/mcp.json` |
| **Memory** | ✓ `CLAUDE.md` | ✓ `AGENTS.md` | ✓ `~/.codex/AGENTS.md` | ◐ `AGENTS.md` |
| **Skill** | ✓ `~/.claude/skills/X/` (dir) | ✓ shared `.claude/skills/` | ✓ `~/.agents/skills/` | ✓ `.cursor/skills/` |
| **Subagent** | ✓ `~/.claude/agents/X.md` | ◐ frontmatter munged | ◐ markdown → TOML | ◐ `.cursor/agents/` |
| **Slash command** | ✓ `~/.claude/commands/X.md` | ◐ `argument-hint` dropped | ◐ `~/.codex/prompts/` | ◐ `.cursor/commands/` |
| **Hook** | ✓ JSON in settings | ✗ skip (JS/TS plugins) | ◐ `config.toml` `[hooks.*]` | ◐ `.cursor/hooks.json` |
| **LSP server** | ✓ native | ✗ skip (deferred) | ✗ no LSP concept | ✗ no LSP config |

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

**Cursor** *(planned)*

- **Memory** — project memory lands as `AGENTS.md`, but Cursor keeps *user-level*
  rules in app-local storage (not the filesystem), so user-scope memory has no
  projection target.
- **Subagent** — markdown under `.cursor/agents/`, but Claude's `tools` allowlist
  is unsupported (the nearest analog is a coarse `readonly` boolean) and is dropped.
- **Slash command** — Cursor commands (`.cursor/commands/*.md`) are plain markdown
  with no frontmatter, so `argument-hint`, `description`, and `allowed-tools` are
  all dropped — only the prompt body survives.
- **Hook** — Cursor uses a declarative `hooks.json` too, but with its own
  (camelCase) event names and script-path handlers, so Claude's events are remapped
  approximately and some have no Cursor equivalent.

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
- **Codex memory** — the same markdown lands at `~/.codex/AGENTS.md`.
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

## Known limits (v1.x)

These are documented trade-offs, not regressions. The authoritative list lives
in the [README](../README.md#known-limits-in-v1x); the highlights:

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
- **Not on the roadmap**: Continue, Gemini CLI, Aider.

See the [user guide](user-guide.md) to put this into practice.

[^planned]: **Planned — not yet implemented.** The Cursor adapter is registered
    as a no-op today, so its column describes the *intended* projection per the
    design spec. `agent add cursor` is rejected unless
    `AGENTSYNC_ALLOW_UNIMPLEMENTED=1`. (Codex graduated to a real adapter.)

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
    config file and needs investigation when the adapter is built.
