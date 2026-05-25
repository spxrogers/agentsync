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
| **Codex CLI** | 🔜 Planned (v1.1) | Registered as a no-op. `agent add codex` is rejected unless `AGENTSYNC_ALLOW_UNIMPLEMENTED=1`. |
| **Cursor** | 🔜 Planned (v1.2) | Registered as a no-op. Will manage project-scope rules only (user rules live in Cursor's app-local storage). |

---

## Component × agent

Component support across agents. Codex (v1.1) and Cursor (v1.2) columns describe
the **planned** projection per the design spec; their adapters are no-ops today.

| Component | Claude (v1.0) | OpenCode (v1.0) | Codex (v1.1) | Cursor (v1.2) |
|---|:--:|:--:|:--:|:--:|
| **MCP server** | ✓ `~/.claude.json` | ✓ `opencode.json` | ◐ `config.toml` | ◐ `mcp.json` |
| **Memory** | ✓ `CLAUDE.md` | ✓ `AGENTS.md` | ◐ `~/.codex/AGENTS.md` | ◐ `AGENTS.md` |
| **Skill** | ✓ `~/.claude/skills/X/SKILL.md` | ✓ shared `.claude/skills/` | ◐ `~/.agents/skills/` | ✗ no skills concept |
| **Subagent** | ✓ `~/.claude/agents/X.md` | ◐ frontmatter munged | ◐ markdown → TOML | ✗ |
| **Slash command** | ✓ `~/.claude/commands/X.md` | ◐ `argument-hint` dropped | ✗ no custom commands | ◐ → `.cursor/rules/*.mdc` |
| **Hook** | ✓ JSON in settings | ✗ skip (JS/TS plugins) | ◐ `hooks.json`, 5/9 events | ◐ `hooks.json`, ~6/9 events |
| **LSP server** | ✓ native | ✗ skip (deferred) | ✗ no LSP concept | ✗ deferred |

> The ◐/✗ cells are *features*, not bugs: agentsync refuses to invent a
> translation that would mislead you. Every ◐ and ✗ is printed in the apply
> report and queryable with `agentsync explain <plugin> --json`.

---

## Why OpenCode skips hooks and LSP

- **Hooks** — OpenCode hooks are JS/TS plugins that subscribe to events, not
  declarative shell commands like Claude's. There is no mechanical translation;
  generating a JS/TS shim is deferred past v1. Hand-author a small plugin if you
  need a hook on OpenCode.
- **LSP** — projection of LSP servers to non-Claude agents is deferred to v1.x.
  Claude plugins that bundle LSP servers install correctly on Claude itself; on
  other agents you'll see `lsp server X skipped` in the report.

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

## Escape hatches

You control fan-out explicitly:

- `agents = ["claude", "opencode"]` on an MCP server or plugin entry → fan out
  only to those agents.
- `[plugin.overrides.<agent>]` with `component = "skip"` → drop one component
  for one agent.

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
