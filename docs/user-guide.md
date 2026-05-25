<div align="center">

# agentsync — User Guide

**One source of truth for every AI coding agent on your machine.**

Define your MCP servers, memory, skills, and marketplace plugins *once*.
Run `agentsync apply`. Watch them land — correctly translated — in Claude Code,
OpenCode, and (soon) Codex and Cursor.

[Why agentsync](#why-agentsync) · [Install](#install) · [Your first sync](#your-first-sync-5-minutes) · [Already have configs?](#already-have-configs) · [The daily loop](#the-daily-loop) · [Building your config](#building-your-config) · [Command reference](#command-reference)

</div>

---

## Why agentsync

If you use more than one AI coding agent, you've felt this: you add an MCP server
to Claude, then hand-copy it into OpenCode's JSON, then again into Codex's TOML.
You install a plugin in one and forget it in the others. You hard-code a token
into a config file and pray it never lands in git. Your `~/.claude.json` and your
OpenCode config slowly drift apart, and you have no idea which one is "right."

agentsync fixes the fan-out. You keep **one canonical config** in `~/.agentsync/`
— small, hand-editable TOML and markdown files you can commit to a dotfiles repo
— and agentsync projects it into each agent's *native* format. Add a server
once; it lands everywhere. Install a plugin once; every agent that understands
its components gets them. Reference a secret as `${secret:github.token}`; it's
resolved at apply time and **never** written back as cleartext.

And because agents edit their own configs, agentsync is **bidirectional**: it
notices when a native file drifts from what it last wrote and offers a
chezmoi-style merge — adopt the edit into your source, or re-impose the source.
Nothing is overwritten behind your back, and nothing is lost.

> **The promise:** edit in one place, apply once, trust the result — with your
> secrets safe and your drift visible.

---

## The 60-second mental model

Three states, one comparison. (Full version in [Concepts](concepts.md).)

```
   ~/.agentsync/            apply            ~/.claude.json
   (your source)   ───────────────────▶     ~/.config/opencode/…
   TOML + markdown     render + translate    (what agents read)
        ▲                                            │
        └──────────── reconcile / import ────────────┘
                  (capture native edits back)
```

- **Source** — what you committed in `~/.agentsync/`.
- **`apply`** — renders the source and writes each agent's native config.
- **Drift** — an agent (or you) edited a native file; `status`/`diff` show it.
- **`reconcile`** — merge that edit back into source, or override it.

That's the whole tool. Everything below is detail.

---

## Install

> **Beta note:** the package-manager channels below are wired up and publish
> starting with the first tagged release. Until then, **build from source.**

**From source (works today):**

```bash
go install github.com/spxrogers/agentsync/cmd/agentsync@latest
# or: git clone … && go build ./cmd/agentsync
```

**macOS — Homebrew**

```bash
brew tap spxrogers/tap
brew install agentsync
```

**Windows — Scoop / Chocolatey**

```bash
scoop bucket add spxrogers https://github.com/spxrogers/scoop-bucket
scoop install agentsync
# or:
choco install agentsync
```

**Linux** — `.deb`/`.rpm` on the [Releases page](https://github.com/spxrogers/agentsync/releases),
or `yay -S agentsync-bin` (AUR).

Verify:

```bash
agentsync --version
```

---

## Your first sync (5 minutes)

This is the greenfield path — start clean, end with an MCP server live in two
agents.

```bash
# 1. Create ~/.agentsync/ and its layout.
agentsync init

# 2. Register the agents you use.
agentsync agent add claude
agentsync agent add opencode

# 3. Add an MCP server once — it will fan out to both agents.
agentsync mcp add github \
  --command npx \
  --args "-y,@modelcontextprotocol/server-github"

# 4. Preview before writing anything. Always safe; never touches disk.
agentsync apply --dry-run

# 5. Apply for real.
agentsync apply
```

Confirm it landed in both native configs:

```bash
jq '.mcpServers.github' ~/.claude.json
jq '.mcp.github'        ~/.config/opencode/opencode.json
```

> **`apply --dry-run` is your friend.** It prints the full
> [translation report](#multi-agent-fan-out) — what lands natively (✓), what's
> projected with loss (◐), and what's skipped (✗) — without writing a byte. Run
> it before every real apply until you trust the output.

---

## Already have configs?

Most people don't start clean — you arrive with servers and plugins already
configured in Claude or OpenCode. Bring them under management instead of
retyping them.

```bash
# See what's on disk vs. what agentsync would write.
agentsync status

# Pull native config into your canonical source.
# Selector grammar: <agent>[:<component>[:<name>]] — drop parts to widen scope.
agentsync import claude --dry-run       # preview what a full import would write
agentsync import claude                 # the agent's full native config
agentsync import claude:mcp             # every MCP server
agentsync import claude:mcp:github      # a single MCP server
agentsync import opencode:mcp:linear

# Now it's in ~/.agentsync/ — apply to fan it out to your other agents.
agentsync apply
```

Dropping the name imports every entry of a component; dropping the component
too imports everything the agent has (MCP, skills, subagents, commands, hooks,
LSP, and memory) in one pass. A bulk import that finds nothing for a component
reports it and exits cleanly rather than erroring. Add `--dry-run` to list the
source files an import would write without touching `~/.agentsync/`.

On a populated machine, the **first** apply will see pre-existing native files it
didn't write and treat them as `foreign-collision`: it backs each one up to
`~/.agentsync/.state/backups/<timestamp>/` *before* writing. Nothing is lost.
Preview which files will be backed up with `agentsync apply --dry-run` first.

---

## The daily loop

Four commands cover day-to-day use:

| Command | When you run it |
|---|---|
| `agentsync apply` | After editing your source — push changes to agents. |
| `agentsync status` | "What's out of sync?" — a summary across all agents. |
| `agentsync diff` | "Show me exactly what changed." Secrets are redacted. |
| `agentsync reconcile` | An agent edited its config — merge or override the drift. |

A typical session after an agent edited a file out from under you:

```bash
agentsync status              # spot the drift
agentsync diff claude         # inspect it (resolved secrets are masked)
agentsync reconcile           # interactively resolve
```

Inside `reconcile`, for each drifting item:

```
~/.claude/settings.json#$.permissions.bash[2]   (drift)
  source:      "Bash(git push:*)"
  destination: "Bash(git push:*) Bash(npm publish:*)"

  [w]rite-back   [o]verride   [s]kip   [i]gnore   [d]iff   [q]uit
```

- **`w`** adopt the destination edit into your source (and `W` for all).
- **`o`** re-impose the source, discarding the edit (`O` for all).
- **`i`** stop tracking this path (adds it to `~/.agentsync/ignore.toml`).
- **`s`/`q`** skip / quit.

Scripting it? `--auto-writeback`, `--auto-override`, or `--auto-safe` (which only
auto-resolves changes that can't lose work).

---

## Building your config

`~/.agentsync/` is just files. Use the CLI or edit them in `$EDITOR` — both are
first-class. Layout:

```
~/.agentsync/
├── agentsync.toml            # agents, update defaults, secrets backend
├── mcp/<server>.toml         # one MCP server per file
├── marketplaces/<name>.toml  # one marketplace per file
├── plugins/<id>.toml         # one plugin enablement per file
├── memory/AGENTS.md          # canonical memory (+ fragments/*.md)
├── skills/<name>/SKILL.md    # standalone skills
└── secrets/secrets.age       # age-encrypted secrets
```

### Agents

```bash
agentsync agent add claude        # register
agentsync agent list              # see registry + enabled state
agentsync agent disable opencode  # stop applying to it (keeps source)
agentsync agent disable opencode --purge   # also remove what it wrote
```

> `agent add codex` / `agent add cursor` are rejected in beta — their adapters
> are no-ops (Codex is v1.1, Cursor v1.2). See the [capability matrix](capability-matrix.md).

### MCP servers

```bash
# stdio transport
agentsync mcp add github \
  --command npx \
  --args "-y,@modelcontextprotocol/server-github" \
  --env "GITHUB_TOKEN=\${secret:github.token}"

# http/sse transport
agentsync mcp add linear --type http --url https://mcp.linear.app/sse

# limit fan-out to specific agents
agentsync mcp add company-api --command npx --args "-y,@company/mcp" \
  --agents "claude,opencode"

agentsync mcp list
agentsync mcp remove github
```

By default a server fans out to **all enabled agents** (`--agents "*"`). The
`mcp/<name>.toml` file it writes is small and editable by hand.

### Memory

Your canonical memory lives in `memory/AGENTS.md` and renders to each agent's
native file (`CLAUDE.md` for Claude, `AGENTS.md` for OpenCode). Compose it from
reusable fragments:

```markdown
<!-- ~/.agentsync/memory/AGENTS.md -->
# Coding conventions

@import ./fragments/style.md
@import ./fragments/security-rules.md
```

### Marketplaces & plugins — the fan-out payoff

This is where agentsync earns its keep. Add a marketplace, install a plugin once,
and every enabled agent gets the components it understands:

```bash
agentsync marketplace add github:anthropics/claude-plugins-official
agentsync plugin install atlassian@anthropic

agentsync update           # fetch from the network (the only networked verb)
agentsync apply            # render from cache → all agents
```

A plugin is a bag of components (MCP servers, skills, subagents, commands, hooks,
LSP servers). Each is translated independently per agent — fully, lossily, or
skipped — and the report tells you exactly which:

```
plugin: atlassian@anthropic
  claude    ✓ full    (1 mcp, 5 commands)
  opencode  ◐ partial (1 mcp; 5 commands → projected)
```

Inspect any plugin's coverage without applying:

```bash
agentsync explain atlassian@anthropic
agentsync explain atlassian@anthropic --json
```

Control fan-out per plugin with `agents = [...]` in the plugin's TOML file. (A
per-component `[plugin.overrides.<agent>]` table was specced but is **not wired
in v1** — the projector does not consult it; use the `agents` allowlist.)

### Secrets

Never put a credential in a config file. Reference it:

```toml
# in mcp/github.toml
[server.env]
GITHUB_TOKEN = "${secret:github.token}"
```

Store the value in the age-encrypted vault — three ways:

```bash
agentsync secrets set github.token --stdin    # from stdin (best for scripts / 1Password CLI)
agentsync secrets set github.token            # interactive prompt, echo off
agentsync secrets edit                        # open the whole vault in $EDITOR
agentsync secrets get github.token            # read one back (to verify)
```

`${secret:…}` is resolved at apply time and written into native config; `${env:…}`
pulls from the environment. The resolved value is **never** captured back into
your source — `agentsync diff` even redacts it so a piped diff can't leak it.

> ### ⚠ Back up your age key
> Secrets are encrypted to an age **recipient** (public key — safe to commit).
> Decryption needs the **identity** file (private key), which is per-machine.
> **agentsync does not back it up for you.** Lose it and you lose access to every
> encrypted secret. Stash it in a 1Password Secure Note or your machine-setup repo.

### Project-local config

A repo can carry its own overlay. Drop a `.agentsync.toml` at its root; agentsync
finds it by walking up from your current directory.

```toml
# myrepo/.agentsync.toml
agents = ["claude", "opencode"]   # subset for this project

[[mcp]]
id      = "company-api"
type    = "stdio"
command = "npx"
args    = ["-y", "@company/mcp"]

[plugins]
disabled = ["screenshot"]         # turn off a user-level plugin here
```

Apply from inside the repo and the overlay merges onto your user config:

```bash
cd ~/code/myrepo
agentsync apply               # auto-detects project scope
ls .claude/settings.json      # project-scope config landed
```

Force a scope explicitly with `--scope user|project` or `--project <path>`.

---

## Updating from the network

`update` is the **only** command that touches the network. It polls
marketplaces, refreshes the local cache, and recomputes version pins — without
touching any agent config. `apply` then renders from that cache, so it's always
fast, offline, and reproducible.

```bash
agentsync update                  # refresh cache + show pending plugin bumps
agentsync update --apply          # refresh, then apply
agentsync update --apply --auto-safe   # same, auto-resolving only safe changes
```

Want nightly refreshes? agentsync ships no daemon — wire
`agentsync update --apply --auto-safe` into your own cron / launchd / systemd /
Task Scheduler.

---

## Multi-agent fan-out

Not every agent supports every component, and agentsync never pretends
otherwise. Each component is marked **✓ native**, **◐ projected** (lossy, but
reported), or **✗ skipped** (no honest translation) per agent.

| Component | Claude | OpenCode | Codex (v1.1) | Cursor (v1.2) |
|---|:--:|:--:|:--:|:--:|
| MCP server | ✓ | ✓ | ◐ | ◐ |
| Memory | ✓ | ✓ | ◐ | ◐ |
| Skill | ✓ | ✓ | ◐ | ✗ |
| Subagent | ✓ | ◐ | ◐ | ✗ |
| Slash command | ✓ | ◐ | ✗ | ◐ |
| Hook | ✓ | ✗ | ◐ | ◐ |
| LSP server | ✓ | ✗ | ✗ | ✗ |

Full detail, native paths, and the reasoning behind each ◐/✗ are in the
[capability matrix](capability-matrix.md).

---

## Cross-machine sync

agentsync is deliberately single-machine. To carry `~/.agentsync/` across
machines, use [chezmoi](https://www.chezmoi.io/) (or any dotfile manager):

```bash
chezmoi add ~/.agentsync
```

The encrypted secrets file is safe to sync; the age identity (private key) is
not — distribute that through your existing secret-sharing flow.

---

## Command reference

Beta surface. `agentsync <command> --help` is always authoritative.

| Command | Purpose | Key flags / args |
|---|---|---|
| `init [<git-url>]` | Create `~/.agentsync/`; optionally clone a bootstrap repo. | |
| `doctor` | Diagnose setup: PATH, home/state writability, config schema, secrets backend. | |
| `verify` | Validate config and surface every unresolved `${secret:}`/`${env:}` ref. | |
| `agent add\|remove\|list\|enable\|disable <name>` | Manage the agent registry. | `disable --purge` |
| `mcp add\|remove\|list <name>` | Manage MCP servers. | `--type --command --args --url --env --agents` |
| `marketplace add\|remove\|list <url-or-name>` | Manage marketplaces. | |
| `plugin install\|upgrade\|enable\|disable\|remove\|list <id>` | Manage plugins. | `install <id[@marketplace]>` |
| `secrets set\|get\|edit <key>` | Manage age-encrypted secrets. | `set --stdin` |
| `update` | **(network)** Refresh marketplace cache + pins. | `--apply --auto-safe --scope --project` |
| `apply` | Render source → write agent configs (offline). | `--dry-run --scope --project` |
| `status` | Summarize drift/pending across agents. | `--scope --project` |
| `diff [<path>]` | Show pending/drift changes; secrets redacted. | `--scope --project` |
| `reconcile` | Interactively merge drift back into source. | `--auto-writeback --auto-override --auto-safe --scope --project` |
| `import <agent>[:<component>[:<name>]]` | Capture native config into source; drop parts to import a whole component or the agent's full config. | `--dry-run` |
| `explain <plugin>` | Show a plugin's per-agent translation coverage. | `--json` |

Global: `-v/--verbose` for verbose logging on any command.

---

## Troubleshooting & environment overrides

The [README](../README.md#troubleshooting) carries the full troubleshooting list
and the complete environment-variable table. The ones you'll reach for most:

| Env var | Purpose |
|---|---|
| `AGENTSYNC_HOME` | Override the `~/.agentsync/` location. |
| `AGENTSYNC_ALLOW_SYMLINK_DEST=1` | Write through symlinked destinations (e.g. chezmoi-managed files). |
| `AGENTSYNC_ALLOW_INSECURE_URLS=1` | Accept `http://`/`git://` plugin/marketplace sources. |
| `AGENTSYNC_ALLOW_OFFLINE_VERIFY=1` | Let `verify` skip secret resolution (CI without an age key). |

Quick hits:

- **`${secret:foo}` not resolving?** `agentsync secrets get foo` to confirm the
  key exists in the decrypted vault.
- **`update` can't fetch a marketplace?** Sanity-check the URL with
  `git ls-remote`.
- **First apply backed up a pile of files?** Expected on a populated machine —
  they're in `.state/backups/<ts>/`, nothing was lost.

---

## Where to go next

- **[Concepts & glossary](concepts.md)** — the mental model in depth.
- **[Architecture](architecture.md)** — how the pipeline and safety invariants work.
- **[Capability matrix](capability-matrix.md)** — exactly what each agent supports.
- **[Component map](components.md)** — the codebase, package by package.
- **[SECURITY.md](../SECURITY.md)** — threat model and reporting.

Found a rough edge during your first 100 minutes? That's exactly the beta
feedback we want — [open an issue](https://github.com/spxrogers/agentsync/issues).
