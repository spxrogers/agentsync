# How agentsync compares

Where agentsync sits in the AI coding-agent configuration landscape — what it
shares with the rest of the field, and the few things that are genuinely its own.

This is the honest-positioning page. agentsync is **not** the only tool that
renders one config into many agents; that pitch is crowded. What's rare is the
*combination* it ships: a single-binary Go CLI that is **bidirectional** (native
edits are captured back, not just generated outward) **and** carries an
**age-encrypted secret vault** with reference resolution. No competitor we've
found pairs all three.

:::note[Point-in-time]
The landscape moves fast — most tools here launched within months of each other,
and star counts / agent lists drift week to week. Figures below are a **mid-2026
snapshot** and are approximate; treat them as "order of magnitude," not gospel.
Corrections welcome.
:::

## The three axes that matter

When comparing these tools, four questions separate them:

1. **Multi-agent** — does it target several agents, or just port between two?
2. **Bidirectional** — does it detect *drift* in native files and reconcile edits
   back into the canonical source, or is it a one-way generator?
3. **Component coverage** — does it manage the full surface (memory, skills, MCP,
   subagents, commands, hooks), or just rules/skills?
4. **Secrets** — does it resolve and protect secrets, or leave them to you?

Almost everyone does #1 and at least part of #3. **#2 and #4 are where the field
thins out**, and where agentsync concentrates.

## Comparison matrix

Legend: ✅ full · ◐ partial / experimental · ❌ none. Components abbreviated
**Mem**ory · **Sk**ills · **MCP** · **Sub**agents · **Cmd** · **Hooks**. For
agentsync's exact per-agent fidelity (native vs projected vs skipped), see the
[capability matrix](capability-matrix.md) — the ✅s below are a summary, not a
fidelity claim.

| Tool | Lang | Agents | Mem | Sk | MCP | Sub | Cmd | Hooks | Bidirectional / drift | Secrets |
|---|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|---|---|
| ⭐️ **agentsync** ⭐️ *(this tool)* | **Go** | **30+**[^breadth] | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ **3-state classifier + `reconcile`/`import` capture** | ✅ **age vault, `${secret:}`/`${env:}`, re-ref + leak backstop** |
| [agentsmesh](https://github.com/sampleXbro/agentsmesh) | TS/Py | 30+ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ `generate`/`import`/`check` (lock-file drift in CI) | ❌ (defers to your store) |
| [rulesync](https://github.com/dyoshikawa/rulesync) | TS | 25+ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ◐ `generate` + `import` (one-shot ingest, no state model) | ❌ |
| [gaal](https://github.com/getgaal/gaal) | **Go** | 17–20 | ◐ files | ✅ | ✅ | ❌ | ◐ files | ✅ | ❌ one-way (`--prune`, `init --import-all` bootstrap) | ❌ |
| [ruler](https://github.com/intellectronica/ruler) | TS | 32 | ✅ | ◐ | ✅ | ◐ | ❌ | ❌ | ❌ one-way (+ `revert` from backups) | ❌ |
| [ai-rulez](https://github.com/Goldziher/ai-rulez) | **Go** | 19+ | ✅ | ✅ | ◐ | ✅ | ✅ | ◐ | ❌ one-way (+ pre-commit enforce/validate) | ❌ |
| [amtiYo/agents](https://github.com/amtiYo/agents) | TS | 11 | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ◐ one-way + `sync --check` drift | ◐ placeholder split (gitignored plaintext) |
| [caliber / ai-setup](https://github.com/caliber-ai-org/ai-setup) | Node | 5 | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ | ❌ one-way generate (LLM-"tailored", `undo`) | ◐ provider keys `0600` only |
| [ai-config-sync-manager](https://github.com/slash9494/ai-config-sync-manager) | Node | 2 (CC↔Codex) | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ true two-way host-aware translation + rollback | ◐ carries MCP bearer tokens |
| [nicepkg/vsync](https://github.com/nicepkg/vsync) | TS | 4 | ❌ | ✅ | ✅ | ✅ | ✅ | ❌ | ◐ one-way from a "source-of-truth" tool (+ import) | ◐ rewrites ref syntax, never expands |
| [mcpup](https://github.com/mohammedsamin/mcpup) | **Go** | 13 | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ◐ per-client enable/disable + `doctor` drift | ◐ plain env |
| [skillshare](https://skillshare.runkids.cc) | **Go** | 60+ | ◐ files | ✅ | ❌ | ✅ | ◐ files | ❌ | ❌ one-way (symlink/copy; `commit` checkpoints) | ❌ (skill security audit only) |

**Adjacent / DIY worth knowing:** the **chezmoi pattern** (a dotfile manager +
Go templates rendering per-agent files, *with* age/`private_` secrets — the
closest *conceptual* analog among workarounds, but one-way, path-level rather than
a typed schema, and no drift capture) and **GNU Stow** (a symlink farm —
same bytes everywhere, no translation or secrets).

## Where agentsync is genuinely differentiated

- **Secrets.** No config-sync competitor we found replicates the age-encrypted
  vault + `${secret:}`/`${env:}` resolution at apply, with **re-reference and a
  fail-closed leak backstop on capture** (it refuses to persist a live secret back
  into your canonical source). The nearest attempts are weaker: `amtiYo/agents`
  (plaintext gitignored placeholder file), the chezmoi pattern (`private_` / env
  vars), `caliber` (only its own provider keys). Tools that *do* use age
  ([chrisleekr/agentsync](https://github.com/chrisleekr/agentsync),
  [ewimsatt/agent-vault](https://github.com/ewimsatt/agent-vault)) are a different
  category — encrypted machine-to-machine *snapshots*, or *runtime* MCP credential
  proxies — not secrets templated into rendered config and then re-referenced on
  the way back. This is agentsync's clearest, most defensible wedge.
- **The drift model.** agentsync's [three-state model](concepts.md) (canonical /
  native / last-applied) with a 9-case classifier and a single `reconcile`/`import`
  capture funnel is materially more developed than anything in the field.
  Competitors' "bidirectional" is usually a one-shot `import`, a CI lock-file check
  (agentsmesh), symlinks (so edits are trivially shared), or a best-effort
  promotion. Only `ai-config-sync-manager` and the much smaller
  `ZacheryGlass/agent-sync` do real state-tracked two-way translation — and both
  cover just two agents.
- **Go + safety invariants.** `gaal`, `ai-rulez`, and `mcpup` are also Go, but
  none pairs the single-binary distribution with agentsync's secret/leak-guard
  architecture.
- **Breadth *and* depth.** The competitors' big agent counts ("30+", "25+") are
  almost entirely **rules-file fan-out** — every tool that reads an instructions
  file counts as an "agent." agentsync now matches that breadth (**31**: a 22-agent
  data-driven generic tier for memory + same-shape MCP) *while also* keeping nine
  **deep** adapters that do multi-component, bidirectional projection — and even
  the breadth tier runs through the drift/secrets/capture pipeline, not a one-way
  dump. Each breadth entry's paths are verified against upstream docs, so the count
  is honest rather than a long list of unmaintained stubs.

## The category map

The field clusters into five groups. agentsync spans the first two and adds
secrets on top.

- **Full bidirectional / multi-component sync** (agentsync's true peers):
  **agentsmesh** (closest on architecture — a canonical dir with
  `generate`/`import`/`check`, framed as "`package.json` generates
  `package-lock.json`"; TypeScript, no secret vault), **rulesync** (the most
  popular at ~1.1k★; broad components, `generate` + `import`, but no drift state
  model or secrets), and **ai-config-sync-manager** (genuine two-way, but only
  Claude Code ↔ Codex).
- **One-way generators** — **gaal** (Go sibling; "one YAML, every agent, every
  machine," adds multi-protocol repo cloning, but no drift-back and no secrets),
  **ruler** (~2.7k★, rules + MCP), **ai-rulez** (Go, broad components),
  **caliber / ai-setup**. These render outward and stop.
- **One-shot porters** — **[cc2codex](https://github.com/ussumant/cc2codex)** (the
  Claude Code → Codex migration plugin; one-time, one-direction, redacts secrets
  rather than moving them). The opposite design point from a persistent canonical
  source.
- **Skills / commands tools** — `vercel-labs/skills`, `skillshare`, `skillkit`,
  and friends. Narrow but high-traffic; increasingly redundant with native
  cross-tool skill loading.
- **MCP-only managers** — **mcpup** (Go, covering much of agentsync's
  Claude Code + OpenCode + Codex + Cursor + Gemini + Continue set, but MCP-only),
  [vek-sync](https://github.com/Vektor-Memory/vek-sync) (an AES-encrypted MCP vault
  with `export`/`diff`/`sync`), [mcpm](https://github.com/pathintegral-institute/mcpm.sh).
  Distinct from MCP *gateways* (MetaMCP, Docker MCP Gateway), which run as a server
  in the request path rather than writing native config.

### A note on the AGENTS.md standard

[AGENTS.md](https://agents.md/) (~22k★, now stewarded under the Linux
Foundation's Agentic AI Foundation) is **not a competitor — it's substrate**.
agentsync renders memory to it for Codex/OpenCode, like everyone else in this
list. The reason these tools exist at all is that Claude Code's `CLAUDE.md`,
skills, hooks, and subagents sit *outside* AGENTS.md, so a single Markdown
standard doesn't make the fan-out problem go away.

## Sources

Primary sources (repos / project sites), verified mid-2026:
[gaal](https://github.com/getgaal/gaal),
[agentsmesh](https://github.com/sampleXbro/agentsmesh),
[rulesync](https://github.com/dyoshikawa/rulesync),
[ruler](https://github.com/intellectronica/ruler),
[ai-rulez](https://github.com/Goldziher/ai-rulez),
[amtiYo/agents](https://github.com/amtiYo/agents),
[caliber/ai-setup](https://github.com/caliber-ai-org/ai-setup),
[ai-config-sync-manager](https://github.com/slash9494/ai-config-sync-manager),
[nicepkg/vsync](https://github.com/nicepkg/vsync),
[mcpup](https://github.com/mohammedsamin/mcpup),
[vek-sync](https://github.com/Vektor-Memory/vek-sync),
[cc2codex](https://github.com/ussumant/cc2codex),
[vercel-labs/skills](https://github.com/vercel-labs/skills),
[skillshare](https://github.com/runkids/skillshare),
[AGENTS.md](https://agents.md/).

[^breadth]: Counted the same way the field's other large numbers are — every
    agent that reads a config file. Of agentsync's, **9 are deep adapters**
    (multi-component, bidirectional projection — Claude Code, OpenCode, Codex,
    Cursor, Gemini CLI, Continue, Windsurf, Roo Code, Cline) and the rest a
    data-driven **breadth tier** (memory + same-shape MCP). The other large
    counts here — agentsmesh, rulesync, ruler, skillshare — are likewise mostly
    breadth (rules- or skills-file fan-out), not deep per-agent adapters.
