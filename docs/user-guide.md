<div align="center">

# agentsync — User Guide

**One source of truth for every AI coding agent on your machine.**

Define your MCP servers, memory, skills, and marketplace plugins *once*.
Run `agentsync apply`. Watch them land — correctly translated — across **31
agents**: nine deep adapters (Claude Code, OpenCode, Codex CLI, Cursor, Gemini CLI,
Continue, Windsurf, Roo Code, Cline) plus a 22-agent breadth tier (amp, goose,
qwen, warp, zed, kiro, junie, factory, copilot, crush, …).

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

**Linux** — `.deb`/`.rpm` on the [Releases page](https://github.com/spxrogers/agentsync/releases).
(AUR packaging is wired but not published yet — [issue #13](https://github.com/spxrogers/agentsync/issues/13).)

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

> **`apply --dry-run` is your friend.** It lists every destination the apply
> would touch, labeling each `✓ synced` (already holds our exact bytes) or
> `→ write` (would be created or changed) — with a `— N to write, M already
> synced` tally — so a clean re-apply reads as a no-op instead of a wall of
> "write"s. It also prints the full [translation report](#multi-agent-fan-out) —
> what lands natively (✓), what's projected with loss (◐), and what's skipped
> (✗) — and previews any foreign-collision backups, all without writing a byte.
> Run it before every real apply until you trust the output.

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
agentsync import claude:plugin          # every installed plugin + marketplace
agentsync import opencode:mcp:linear

# Now it's in ~/.agentsync/ — apply to fan it out to your other agents.
agentsync apply
```

Dropping the name imports every entry of a component; dropping the component
too imports everything the agent has (MCP, skills, subagents, commands, hooks,
LSP, memory, **and plugins**) in one pass. A bulk import that finds nothing for a
component reports it and exits cleanly rather than erroring. Add `--dry-run` to
list the source files an import would write without touching `~/.agentsync/`.

**Plugins are a special case.** The `plugin` component (Claude only in v1) reads
the agent's installed plugins and their marketplaces and re-fetches each one into
the agentsync cache, pinning a manifest SHA — the same artifacts `marketplace
add` + `plugin install` produce. Because it re-fetches, a real plugin import (not
`--dry-run`) needs network access. A plugin's marketplace is resolved from
agentsync's own registered marketplaces first, then the agent's native config. A
plugin whose marketplace is registered in neither — for example a plugin from
Claude's built-in `claude-plugins-official` (which doesn't appear in
`extraKnownMarketplaces`) before you have registered it — is reported and
skipped; register it with `agentsync marketplace add <source>` and re-import.

**Importing a project's native config.** `import <agent> --scope project`
(optionally `--project <path>`) reads the agent's *native project-scope* config
(e.g. `<root>/.claude/`) and captures it into the project source tree
`<root>/.agentsync/` rather than your user `~/.agentsync/`. It seeds central state
with the project scope + root so the next apply doesn't treat those files as a
foreign collision. Plugins are excluded: a named `import claude:plugin:<name>
--scope project` errors, and a bulk `import claude:plugin --scope project`
silently skips — plugins are a user-scope concept across the harnesses.

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

## Rolling back a bad apply

`apply` can keep each user-scope destination dir (`~/.claude`, `~/.codex`, …) in
its **own local-only git repo**, recording a checkpoint commit after every apply
that changes managed files there. If an apply ever goes wrong, roll it back:

```bash
agentsync revert claude              # undo the most recent apply to ~/.claude
agentsync revert claude --to HEAD~3  # roll back to an older checkpoint
agentsync revert --all --dry-run     # preview reverting every managed dir
```

`revert` is **append-only** — it records a new commit rather than rewriting
history, so the bad apply stays in the log and the revert is itself revertible. If
you hand-edited a tracked file after the last apply, revert snapshots that edit
into history first, so **nothing is lost** (recover it with `revert --to <snapshot>`).
It moves only the *destination*, so afterwards it reminds you to **reconcile** (or
fix the canonical source) before the next `apply` re-renders over it.

The first apply to an untracked dir **asks** before initializing the repo
(opt-out). Answer once and it's remembered in `agentsync.toml`:

```toml
[destination_directory_git_backup]
mode = "on"          # "prompt" (default) | "on" | "off"
# author_name  = "agentsync"      # optional commit-identity overrides
# author_email = "agentsync@localhost"
```

`apply --no-git-backup` skips it for one run (CI/scripting) without touching
config, and `agentsync doctor` shows the current mode and per-dir status.

The unit is the **directory**, not the agent: each agent's config dir plus any
shared cross-agent dir it writes (e.g. `~/.agents/skills`, which Codex and several
agents share) is versioned — shared dirs are de-duplicated to a single repo, and a
dir nested under another (like `~/.claude/skills` under `~/.claude`) is folded into
the parent, so there's never a repo inside a repo.

These repos are **never pushed**. The rendered files they version contain secrets
resolved to **cleartext** (unlike the canonical source, which keeps `${secret:…}`
references), so the history can hold secrets — which is fine precisely because it
stays local. The thing you commit and push is still `~/.agentsync/`, references
only. A destination dir you already keep under your own git (e.g. `~/.claude` in a
dotfiles repo) is detected and left untouched. (`~/.claude.json`, written directly
in `$HOME`, is not versioned — agentsync never inits a repo at `$HOME`; it keeps the
existing `.state/backups` safety net.)

---

## Building your config

`~/.agentsync/` is just files. Use the CLI or edit them in `$EDITOR` — both are
first-class. Layout:

```
~/.agentsync/
├── agentsync.toml            # agents, update defaults, secrets backend, [memory] banner, [destination_directory_git_backup]
├── mcp/<server>.toml         # one MCP server per file
├── lsp/<server>.toml         # one LSP server per file
├── agents/<name>.md          # one subagent per file
├── commands/<name>.md        # one slash command per file
├── hooks/<event>.toml        # one hook per file
├── marketplaces/<name>.toml  # one marketplace per file
├── plugins/<id>.toml         # one plugin enablement per file
├── memory/AGENTS.md          # canonical memory (+ fragments/*.md)
├── skills/<name>/            # a skill is a DIRECTORY: SKILL.md + bundled
│   ├── SKILL.md              #   scripts/, references/, assets/, nested files —
│   └── scripts/ …            #   all carried verbatim, executable bit preserved
└── secrets/secrets.age       # age-encrypted secrets
```

A **project source tree** at a repo's `<root>/.agentsync/` has the *same*
on-disk layout (created by `agentsync init --scope project`) — `agentsync.toml`
plus `mcp/`, `lsp/`, `agents/`, `commands/`, `hooks/`, `memory/` (with
`fragments/`), `skills/`, `plugins/`, and `secrets/`. The one difference: it has
**no `.state/`** — apply records state centrally under `~/.agentsync/.state/`,
keyed by project root. Commit the `.agentsync/` tree to the repo to share project
agent config with collaborators. See [Project-local config](#project-local-config).

### Agents

```bash
agentsync agent add claude        # register
agentsync agent list              # see registry + enabled state
agentsync agent list --all        # every supported agent (registered or not)
agentsync agent disable opencode  # stop applying to it (keeps source)
agentsync agent disable opencode --purge   # also remove what it wrote
```

> All nine deep adapters (`claude`, `opencode`, `codex`, `cursor`, `gemini`,
> `continue`, `windsurf`, `roo`, `cline`) plus 22 breadth-tier agents work with
> `agent add` — run `agentsync agent list --all` for the full set, or see the
> [capability matrix](capability-matrix.md).

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

Fragments **round-trip both ways**. On `apply`, agentsync wraps each inlined
fragment in HTML-comment boundary markers in the native file:

```markdown
<!-- agentsync:fragment style.md -->
Be concise.
<!-- /agentsync:fragment style.md -->
```

so `import`/`reconcile` can reverse the expansion — a native memory edit *inside*
a fragment block is captured back into that **fragment file**, and the `@import`
structure is preserved (the edit is never flattened into `AGENTS.md`). The
markers read as metadata, not instructions. If the markers are missing (a
fragment whose own text contains the marker token disables them) or were
hand-mangled into an unbalanced/ambiguous state, agentsync refuses the write-back
rather than guess; the drift still shows in `status`/`diff` and you fold it into
`memory/` by hand.

**The managed banner.** Every rendered memory file is prepended with a short
agentsync notice — a blockquote naming the file (e.g. `CLAUDE.md`) and pointing
edits back at `.agentsync/memory/AGENTS.md` + `agentsync apply`. It is written by
agentsync, **not** stored in your canonical `memory/AGENTS.md`: it is wrapped in
`<!-- agentsync:managed memory-banner -->` markers, stripped on
`import`/`reconcile`, and re-rendered each apply — so it never compounds and
(being static) never shows as drift. It is on by default; opt out with a
`[memory]` table in `agentsync.toml`:

```toml
[memory]
banner = false
```

The `agentsync:managed` marker is **reserved** — if your `memory/AGENTS.md` or a
fragment contains it, agentsync errors and asks you to remove it (so it can't
collide with the banner). The reverse is safe too: capture only strips agentsync's
own banner, so any other content you keep is never deleted.

### Marketplaces & plugins — the fan-out payoff

This is where agentsync earns its keep. Add a marketplace, install a plugin once,
and every enabled agent gets the components it understands:

```bash
agentsync marketplace add github:anthropics/claude-plugins-official
agentsync plugin install atlassian@anthropic

agentsync update           # fetch from the network (refresh + re-pin plugins)
agentsync apply            # render from cache → all agents
```

A plugin is a bag of components (MCP servers, skills, subagents, commands, hooks,
LSP servers). Each is translated independently per agent — fully, lossily, or
skipped — and the report tells you exactly which:

```
▸ atlassian@anthropic
  → claude    ✓ full        1 mcp · 5 commands · 3 subagents · 1 lsp
  → codex     ◐ partial     1 mcp · 5 commands · 3 subagents · 1 lsp  (3 reduced · 1 dropped)
      → codex couldn't fully translate — reduced = rendered without some fields; dropped = not emitted:
        • subagent ai-architect   reduced  Codex agents are TOML with no per-agent tools allowlist; dropped tools, color
        • subagent deploy-expert  reduced  Codex agents are TOML with no per-agent tools allowlist; dropped tools, color
        • subagent perf-optimizer reduced  Codex agents are TOML with no per-agent tools allowlist; dropped tools, color
        • lsp atlassian-lsp       dropped  Codex has no LSP configuration concept
```

Each row's count tail lists every component kind the plugin hosts for that agent
— MCP servers, commands, skills, subagents, hooks, and LSP servers (only the
non-zero kinds are shown) — so the inventory is fully descriptive, not just `mcp`
+ `commands`. The counts describe what the plugin *hosts*; the coverage glyph and
the trailing note describe what the agent could *do* with it.

That trailing note is split by kind so it never reads as "N whole components
discarded": a **reduced** part still rendered, just without some fields the agent
has no home for (here each subagent landed as Codex TOML, only its Claude-only
`tools`/`color` frontmatter dropped); a **dropped** part had no native target at
all and was not emitted (the LSP server — Codex has no LSP concept). An LSP-only
plugin on Codex reads `✗ none  1 lsp  (1 dropped)`, telling you both what is there
and that none of it landed.

A `◐ partial` row is never a dead end: every part the agent could not fully
translate is itemized beneath a framing header, each tagged `reduced` or
`dropped` with the reason, so you can see exactly what loss `apply` would incur.
`--json` carries the per-kind counts (`mcp`, `commands`, `skills`, `subagents`,
`hooks`, `lsp`) and the `skipDetails` array (each entry `{component, name,
reason, kind}`) on every row. `kind` is `"reduced"` or `"dropped"` — the explicit
machine surface for the split, so a consumer never re-derives it from the
component string. `component` is the plain component kind (`subagent`, `command`,
`lsp`, …); it carries no `-frontmatter` suffix.

Inspect any plugin's coverage without applying:

```bash
agentsync explain atlassian@anthropic                   # one plugin
agentsync explain atlassian@anthropic superpowers@obra  # space-separated
agentsync explain --all                                 # every installed plugin
agentsync explain --list                                # just the ids (skip rendering)
agentsync explain atlassian@anthropic --json            # machine-readable
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

First, create an age keypair. The vault is encrypted to the **recipient**
(public key — safe to commit); decryption needs the **identity** (private key —
per-machine). agentsync embeds age, but generating the key uses the `age-keygen`
CLI (`brew install age`, `apt install age`, …):

```bash
mkdir -p ~/.config/agentsync
age-keygen -o ~/.config/agentsync/age.key   # prints "Public key: age1…" to stderr
chmod 600 ~/.config/agentsync/age.key        # agentsync refuses a group/other-readable identity
```

Then point `agentsync.toml` at it — `recipient` is the `age1…` public key
`age-keygen` printed (agentsync encrypts to a single X25519 recipient, so use the
age-keygen key, not an SSH key):

```toml
[secrets]
backend       = "age"
recipient     = "age1…"
identity_file = "${env:HOME}/.config/agentsync/age.key"
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

A repo can carry its own **project source tree** — a `.agentsync/` directory at
its root, with the same layout as your user `~/.agentsync/`. Commit it to share
the project's agent config with collaborators. Scaffold it with:

```bash
cd ~/code/myrepo
agentsync init --scope project        # creates ./.agentsync/
# or target another path explicitly (implies project scope):
agentsync init --project ~/code/myrepo
```

That writes `<root>/.agentsync/agentsync.toml` plus `mcp/`, `lsp/`, `skills/`,
`agents/`, `commands/`, `hooks/`, `memory/`, `plugins/`, and `secrets/` — the
same files as the user tree, minus `.state/` (apply records state centrally under
`~/.agentsync/.state/`, keyed by project root). Author the project's config in
this tree:

```toml
# myrepo/.agentsync/agentsync.toml
[agents]
claude = { enabled = true }       # subset for this project;
                                  # leave [agents] empty to inherit the user's
                                  # enabled agents
```

```toml
# myrepo/.agentsync/mcp/company-api.toml — same format as a user-scope mcp file
[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@company/mcp"]
```

```toml
# myrepo/.agentsync/plugins/screenshot.toml — turn off a user-level plugin here
[plugin]
disabled = true
```

Author project memory directly in `<root>/.agentsync/memory/AGENTS.md` (compose
it from `fragments/` just like the user tree).

The project tree is **overlaid** onto your user canonical: a project entry
replaces a user entry with the same id/name, new entries are appended, project
memory is appended after user memory, and an empty project `[agents]` inherits
the user's enabled agents.

Apply at project scope (an explicit opt-in) and the overlay merges onto your
user config:

```bash
cd ~/code/myrepo
agentsync apply --scope project   # walks up from cwd to the .agentsync/ tree
ls .mcp.json                      # project-scope MCP servers landed (repo root)
```

Commands default to **user** scope. Project scope is never auto-applied: pass
`--scope project` (walks up from cwd to find the tree) or `--project <path>`
(`--scope user` together with `--project` is an error). If you run a command with
no scope *inside* a project tree, agentsync **prompts** you to choose
project-vs-user; in a non-interactive shell — or with the global `--no-input`
flag — it errors instead of guessing. `--scope project` with no tree found (and
`--project` at a path without a `.agentsync/` tree) is a hard error pointing you
at `agentsync init --scope project` — it never silently falls back to user scope.

> **Upgrading from the old single-file marker?** The retired `.agentsync.toml`
> marker at a repo root is no longer read — agentsync errors and tells you to run
> `agentsync init --scope project` and move the settings into the `.agentsync/`
> tree.

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

Claude, OpenCode, Codex, Cursor, Gemini CLI, Continue, Windsurf, Roo Code, and Cline are all real adapters.

| Component | Claude | OpenCode | Codex | Cursor | Gemini | Continue | Windsurf | Roo | Cline |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| MCP server | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Memory | ✓ | ✓ | ✓ | ◐ | ✓ | ✓ | ✓ | ✓ | ◐ |
| Skill | ✓ | ✓ | ✓ | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ |
| Subagent | ✓ | ◐ | ◐ | ◐ | ◐ | ✗ | ✗ | ✗ | ✗ |
| Slash command | ✓ | ◐ | ◐ | ◐ | ◐ | ◐ | ◐ | ◐ | ◐ |
| Hook | ✓ | ✗ | ◐ | ◐ | ◐ | ✗ | ✗ | ✗ | ✗ |
| LSP server | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |

(Some adapters are scope-asymmetric: Windsurf's and Cline's MCP is global-only and renders at user scope — Windsurf memory + commands render at both scopes, Cline's at project scope; Roo renders MCP at project scope only — VS Code agents keep global MCP in app-storage. See the [capability matrix](capability-matrix.md).)

Beyond these nine deep adapters, a **breadth tier** of 22 more agents (amp, goose,
qwen, warp, zed, kiro, junie, factory, copilot, crush, …) is supported via one
data-driven generic adapter — memory for all, MCP where the agent reads a JSON
server-map, and Agent Skills (`SKILL.md` directories) where the agent natively scans
a skills directory. Run `agentsync agent list --all` to see them all; see the
[capability matrix → Breadth tier](capability-matrix.md#breadth-tier) for per-agent
coverage.

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
| `init [<git-url>]` | Create `~/.agentsync/` (user scope); optionally clone a bootstrap repo. `--scope project` scaffolds a project tree at `<cwd>/.agentsync/` instead; `--project <path>` targets `<path>/.agentsync/` (implies project scope). A git-URL clone is user-scope only. | `--scope --project` |
| `doctor` | Diagnose setup: PATH, home/state writability, config schema, secrets backend; flags natively-installed plugins missing from source. | |
| `verify` | Validate config and surface every unresolved `${secret:}`/`${env:}` ref. `--scope project`/`--project <path>` schema-lints the project tree and validates its references against the inherited user secrets backend. | `--scope --project` |
| `agent add\|remove\|list\|enable\|disable <name>` | Manage the agent registry. | `disable --purge` |
| `mcp add\|remove\|list <name>` | Manage MCP servers. | `--type --command --args --url --env --agents` |
| `marketplace add\|remove\|list <url-or-name>` | Manage marketplaces. | |
| `plugin install\|upgrade\|enable\|disable\|remove\|list <id>` | Manage plugins. | `install <id[@marketplace]>` |
| `secrets set\|get\|edit <key>` | Manage age-encrypted secrets. | `set --stdin` |
| `update` | **(network)** Refresh marketplace cache + pins. | `--apply --auto-safe --scope --project` |
| `apply` | Render source → write agent configs (offline). Git-versions each user-scope destination dir into a local-only repo (opt-out) so a bad apply is revertible. | `--dry-run --scope --project --no-git-backup` |
| `revert <agent>` | Roll a destination dir back to a prior apply checkpoint (append-only). Default undoes the most recent apply; prints an out-of-sync notice. | `--to --all --dry-run` |
| `status` | Summarize drift/pending across agents; notes natively-installed plugins not yet in source. Skill directories collapse to one summary row by default (`--verbose` expands them). | `--agents --verbose --scope --project --json` |
| `diff [<path>]` | Show pending/drift changes; secrets redacted. | `--scope --project --json` |
| `reconcile` | Interactively merge drift back into source. | `--auto-writeback --auto-override --auto-safe --scope --project` |
| `import <agent>[:<component>[:<name>]]` | Capture native config into source; drop parts to import a whole component or the agent's full config. Includes `plugin` (Claude), which re-fetches installed plugins + marketplaces **(network)**. `--scope project` reads the agent's *native project-scope* config (e.g. `<root>/.claude/`) and captures it into the project tree `<root>/.agentsync/`, seeding central state with the project scope + root. Plugin import is user-scope only. | `--dry-run --scope --project` |
| `explain [<plugin>...]` | Show per-agent translation coverage for one or more plugins. | `--all --list --json` |

Global: `-v/--verbose` for verbose logging on any command (in `status` it also
expands each collapsed skill directory back to one row per bundled file).
`--color=auto|always|never` controls whether output is styled with ANSI color
and bold (default `auto` — on for a TTY, off when piped/redirected; honors
`NO_COLOR`). `status --agents <list>` scopes the report to a comma-separated
agent allowlist (`*` = all enabled, matching `mcp add --agents`). `status --json`
and `diff [<path>] --json` emit
the structured report instead of the formatted one, suitable for CI gates and
dashboards (`status --json` is never collapsed — it carries every tracked file;
`diff --json` masks the same resolved secrets the formatted diff does).

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
