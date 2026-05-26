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
| **Claude Code** | ✅ Full adapter | All seven components, including LSP. The reference implementation. |
| **OpenCode** | ✅ Adapter (some components projected/skipped) | MCP, memory, skills, subagents, commands. Hooks and LSP are skipped with a warning. |
| **Codex CLI** | 🔜 Planned | Registered as a no-op. `agent add codex` is rejected unless `AGENTSYNC_ALLOW_UNIMPLEMENTED=1`. |
| **Cursor** | 🔜 Planned | Registered as a no-op. Planned coverage is broad — MCP, memory, skills, subagents, slash commands, and hooks all project (see the matrix); only LSP is unsupported. User-level rules/memory live in Cursor's app-local storage, so those stay project-scope. |

---

## Component × agent

Component support across agents.

| Component | Claude | OpenCode | Codex[^planned] | Cursor[^planned] |
|---|:--:|:--:|:--:|:--:|
| **MCP server** | ✓ `~/.claude.json` | ✓ `opencode.json` | ✓ `config.toml` | ✓ `.cursor/mcp.json` |
| **Memory** | ✓ `CLAUDE.md` | ✓ `AGENTS.md` | ✓ `~/.codex/AGENTS.md` | ◐ `AGENTS.md` |
| **Skill** | ✓ `~/.claude/skills/X/SKILL.md` | ✓ shared `.claude/skills/` | ✓ `~/.agents/skills/` | ✓ `.cursor/skills/` |
| **Subagent** | ✓ `~/.claude/agents/X.md` | ◐ frontmatter munged | ◐ markdown → TOML | ◐ `.cursor/agents/` |
| **Slash command** | ✓ `~/.claude/commands/X.md` | ◐ `argument-hint` dropped | ◐ `~/.codex/prompts/` | ◐ `.cursor/commands/` |
| **Hook** | ✓ JSON in settings | ✗ skip (JS/TS plugins) | ◐ `~/.codex/hooks.json` | ◐ `.cursor/hooks.json` |
| **LSP server** | ✓ native | ✗ skip (deferred) | ✗ no LSP concept | ✗ no LSP config |

> The ◐/✗ cells are *features*, not bugs: agentsync refuses to invent a
> translation that would mislead you. Every ◐ and ✗ is printed in the apply
> report and queryable with `agentsync explain <plugin> --json`.

---

## What each ◐ loses

Every projected (◐) cell above is a deliberate, reported translation. Here's what
doesn't carry over.

**OpenCode**

- **Subagent** — Claude's `tools` allowlist is remapped onto OpenCode's
  `permission` model (approximate), and Claude frontmatter keys OpenCode doesn't
  recognize are dropped.
- **Slash command** — Claude's `argument-hint` has no OpenCode field and is
  dropped; there's no command-level `allowed-tools` (scoping is per-agent instead).

**Codex** *(planned)*

- **Subagent** — Codex custom agents are TOML, not markdown: the prose body
  becomes `developer_instructions`, and there is no per-agent `tools` allowlist
  (tool scoping is only expressible via `[mcp_servers]` / skill toggles), so the
  allowlist is dropped.
- **Slash command** — maps to Codex *custom prompts* (`~/.codex/prompts/*.md`),
  which do preserve `argument-hint`, but they're global-only (no project scope),
  can't be namespaced in subdirectories, and the feature is deprecated in favor of
  skills.
- **Hook** — Codex mirrors Claude's declarative hook JSON schema
  (`~/.codex/hooks.json`), but recognizes ~11 lifecycle events; Claude events
  outside that set have no target and drop.

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
- **Skills (Codex & Cursor)** — the same `SKILL.md` (name + description +
  `scripts/`/`references/`/`assets/`). Codex installs them under `~/.agents/skills/`
  (enabled by default — no feature flag), Cursor under `.cursor/skills/`, and both
  also read the shared `.claude/skills/`.

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

- **Comment preservation** — comments in `mcp/*.toml` and in `opencode.json` are
  not preserved across a write-back/import round-trip in the rewritten section.
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

[^planned]: **Planned — not yet implemented.** The Codex and Cursor adapters are
    registered as no-ops today, so these columns describe the *intended*
    projection per the design spec. `agent add codex` / `agent add cursor` are
    rejected unless `AGENTSYNC_ALLOW_UNIMPLEMENTED=1`.
