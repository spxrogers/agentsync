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
agentsync import opencode:subagent:reviewer
agentsync import opencode:mcp:linear

# Now it's in ~/.agentsync/ — apply to fan it out to your other agents.
agentsync apply
```

Dropping the name imports every entry of a component; dropping the component
too imports everything the agent has (MCP, skills, subagents, commands, hooks,
LSP, memory, **and plugins**) in one pass. A bulk import that finds nothing for a
component reports it and exits cleanly rather than erroring. Add `--dry-run` to
list the source files an import would write without touching `~/.agentsync/`.

**Import never re-captures a plugin's own components** — subagents, skills,
commands, MCP servers, LSP servers, and hooks alike (hooks per *handler*, so a
plugin hooking an event you also hook never costs you your own handler). Once you have applied, a plugin's
components are sitting in the agent's native config indistinguishable from ones
you wrote yourself — the agent's config gives no hint which ones agentsync put
there. Importing them would create canonical copies that collide
with the plugin's own on the next apply, so `import` skips them and says which
plugin provides each. Naming one explicitly
(`agentsync import claude:subagent:feature-dev-code-reviewer`) is an error rather
than a silent no-op. Components you wrote into the native config yourself are
still captured normally. To change a plugin's component, change it upstream, or
run `agentsync plugin disable <id>` to stop projecting it. `reconcile` refuses
`[w]rite-back` on one for the same reason — use `[o]verride` to restore the
plugin's version.

**Plugins are a special case.** The `plugin` component (Claude and Codex) reads
the agent's installed plugins and their marketplaces and re-fetches each one into
the agentsync cache, pinning a manifest SHA — the same artifacts `marketplace
add` + `plugin add` produce. Because it re-fetches, a real plugin import (not
`--dry-run`) needs network access. A plugin's marketplace is resolved from
agentsync's own registered marketplaces first, then the agent's native config. A
plugin whose marketplace is registered in neither — for example a plugin from
Claude's built-in `claude-plugins-official` (which doesn't appear in
`extraKnownMarketplaces`) before you have registered it — is reported and
skipped; register it with `agentsync marketplace add <source>` and re-import.

An imported plugin is **not projected back into the agent it came from** unless
you ask for it. That agent has the plugin installed, and apply never disables a
plugin inside another tool's plugin manager — so projecting the same components
there would duplicate every one of them. `import` offers to record
`native_agents = ["claude"]`; accepting is the default, and declining warns that
you must disable the plugin inside Claude yourself. Either way every other
enabled agent gets the full fan-out. See
[Which agents a plugin fans out to](#which-agents-a-plugin-fans-out-to).

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
agentsync diff                # inspect it (resolved secrets are masked)
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

Scripting it? `--auto-writeback`, `--auto-override`, or `--auto-safe`. The last
resolves nothing — every item that reaches `reconcile` needs a human — and
instead lists each one as left unresolved, so it works as a non-interactive
"does anything need me" check.

---

## Rolling back a bad apply

`apply` can keep each user-scope destination dir (`~/.claude`, `~/.codex`, …) in
its **own local-only git repo**, recording a checkpoint commit after every apply
that changes managed files there. **Even the first apply is revertible:** before
that apply overwrites the dir, agentsync records a **pre-apply baseline** commit of
the prior content of the files it is about to manage, so the apply checkpoint's parent
is the genuine pre-apply state — there is no "the first apply can't be undone" gap.
Pre-existing files agentsync did **not** write (an agent's credentials, conversation
transcripts, your own scratch files) are deliberately left **out** of the versioned
history so it never becomes a durable copy of your secrets — they are untracked, so a
revert leaves them untouched anyway. The flip side: such files are **preserved, not
versioned** — a revert never deletes them, but because they are never committed, the
backup history cannot restore one *you* delete. If an apply ever goes wrong, roll it
back:

```bash
agentsync revert claude              # undo the most recent apply to ~/.claude
agentsync revert claude --to HEAD~3  # roll back to an older checkpoint
agentsync revert --all --dry-run     # preview reverting every managed dir
```

`revert` is **append-only** — it records a new commit rather than rewriting
history, so the bad apply stays in the log and the revert is itself revertible. If
you hand-edited a **tracked** file after the last apply, revert snapshots that edit
into history first, so **nothing tracked is lost** (recover it with `revert --to
<snapshot>`). The snapshot deliberately covers **tracked files only** — untracked
scratch files you dropped into a managed dir are never committed and are outside
the revert snapshot (they are also left untouched by the rollback, not deleted).
That snapshot is taken by the rollback engine itself, so the guarantee holds however
revert is invoked; in the rare event a rollback fails partway (e.g. a full disk), the
error names the snapshot commit and the pre-revert checkpoint so you can recover.
Any **untracked files** you dropped into the dir (and gitignored files) are **left
untouched** — revert only rewinds the files agentsync itself versions, so your own
scratch files are never deleted. `revert --dry-run` notes when such files are
present. And if you later clone your own git repo *inside* a managed dir (e.g.
`~/.claude/skills/.git`), revert refuses to hard-reset over your repo's checked-out
files: naming an agent (`agentsync revert claude`) **errors**, while `--all`
**skips that dir** with a warning and carries on with the rest. (Strictness follows
the invocation — there is no `--strict` flag.) It moves only the *destination*, so afterwards it reminds you to
**reconcile** (or fix the canonical source) before the next `apply` re-renders over
it.

The first apply to an untracked dir **asks** before initializing the repo
(opt-out); it takes the pre-apply baseline the moment you agree, so that first apply
is covered too. Answer once and it's remembered in `agentsync.toml`:

```toml
[destination_directory_git_backup]
mode = "on"          # "prompt" (default) | "on" | "off"
# author_name  = "agentsync"      # optional commit-identity overrides
# author_email = "agentsync@localhost"
```

`mode` accepts exactly `prompt`, `on`, or `off` (case-sensitive; an omitted
`mode` defaults to `prompt`). Any other value — a typo like `"On"`, `"yes"`, or
`"true"` — is now **rejected at load** with a path-prefixed error, so every
command that reads the config (`apply`, `doctor`, …) agrees. (Previously an
invalid value was silently ignored and only `doctor` warned about it.)

`apply --no-git-backup` skips it for one run (CI/scripting) without touching
config, and `agentsync doctor` shows the current mode and per-dir status.

The unit is the **directory**, not the agent: each agent's config dir plus any
shared cross-agent dir it writes (e.g. `~/.agents/skills`, which Codex and several
agents share) is versioned — shared dirs are de-duplicated to a single repo, and a
dir nested under another (like `~/.claude/skills` under `~/.claude`) is folded into
the parent, so there's never a repo inside a repo. Because a shared dir is one
repo, `revert <agent>` of a shared dir rolls back *every* agent's files in it —
revert warns you when that happens.

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
├── subagents/<name>.md       # one subagent per file (NOT `agents/` — that
│                             #   word names the harness registry in agentsync.toml)
├── commands/<name>.md        # one slash command per file
├── hooks/<event>.toml        # one hook per file
├── marketplaces/<name>.toml  # one marketplace per file (its `head_sha`/`name`
│                             #   keys are CLI fetch-cache metadata, regenerated on
│                             #   fetch — not modeled in the canonical schema)
├── plugins/<id>.toml         # one plugin enablement per file (`agents` /
│                             #   `native_agents` decide which agents it renders to)
├── memory/AGENTS.md          # canonical memory (+ fragments/*.md)
├── skills/<name>/            # a skill is a DIRECTORY: SKILL.md + bundled
│   ├── SKILL.md              #   scripts/, references/, assets/, nested files —
│   └── scripts/ …            #   all carried verbatim, executable bit preserved
└── secrets/secrets.age       # age-encrypted secrets
```

A **project source tree** at a repo's `<root>/.agentsync/` has the *same*
on-disk layout (created by `agentsync init --scope project`) — `agentsync.toml`
plus `mcp/`, `lsp/`, `subagents/`, `commands/`, `hooks/`, `memory/` (with
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

Every agent command also takes `--scope project` / `--project <path>` to manage
the **project's own** `[agents]` declaration in `<root>/.agentsync/agentsync.toml`
instead of the user registry — project scope renders only to agents declared
there (see [Project-local config](#project-local-config)). At project scope,
`disable --purge` removes only that project's rendered files; at user scope,
`--purge` cleans up that agent's rendered files across every scope and project
(the historical behavior).

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
agentsync plugin add atlassian@anthropic

agentsync plugin outdated  # fetch from the network (refresh cache, show bumps)
agentsync plugin upgrade --all   # re-pin every pending bump and re-apply
```

A plugin is a bag of components (MCP servers, skills, subagents, commands, hooks,
LSP servers). Each is translated independently per agent — fully, lossily, or
skipped — and the report tells you exactly which:

```
▸ atlassian@anthropic
  → claude    ✓ full        1 mcp · 5 commands · 3 subagents · 1 lsp
  → codex     ◐ partial     1 mcp · 5 commands · 3 subagents · 1 lsp  (3 reduced · 1 dropped)
      → codex couldn't fully translate — reduced = rendered without some fields; dropped = not emitted:
        • subagent atlassian-ai-architect   reduced  Codex agents are TOML with no per-agent tools allowlist; dropped tools, color
        • subagent atlassian-deploy-expert  reduced  Codex agents are TOML with no per-agent tools allowlist; dropped tools, color
        • subagent atlassian-perf-optimizer reduced  Codex agents are TOML with no per-agent tools allowlist; dropped tools, color
        • lsp atlassian-lsp                 dropped  Codex has no LSP configuration concept
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
agentsync plugin explain atlassian@anthropic                   # one plugin
agentsync plugin explain atlassian@anthropic superpowers@obra  # space-separated
agentsync plugin explain --all                                 # every installed plugin
agentsync plugin explain atlassian@anthropic --json            # machine-readable
```

(`agentsync plugin list` prints the installed ids. The top-level `explain` name
answers a different question — see [Where did this file come from?](#where-did-this-file-come-from).)

#### Plugin components are namespaced by their plugin

A plugin's **subagents, skills, and slash commands** render under
`<plugin>-<name>`, which is why the report above reads `atlassian-ai-architect`
rather than `ai-architect`:

| Plugin | Ships | Lands at | You invoke |
|---|---|---|---|
| `atlassian` | `agents/ai-architect.md` | `~/.claude/agents/atlassian-ai-architect.md` | `@agent-atlassian-ai-architect` |
| `atlassian` | `commands/jira.md` | `~/.claude/commands/atlassian-jira.md` | `/atlassian-jira` |

Every agent reads its components from one flat directory, so without this two
plugins shipping a same-named component would write two files at one path. That
is not hypothetical: `feature-dev` and `pr-review-toolkit` — both stock official
plugins — each ship `agents/code-reviewer.md`, and before namespacing that made
`agentsync apply` fail with no way out, since neither file is yours to rename.
Claude Code reaches the same end natively, addressing a plugin's agent as
`plugin:agent`; agentsync uses a hyphen because a colon is not legal in a
subagent `name` (or in a filename on Windows).

**Components you write yourself are never renamed.** Anything in your own
`~/.agentsync/subagents/`, `skills/`, or `commands/` keeps the name you gave it,
even when an installed plugin ships one by the same name. In the rare case where
a plugin's *derived* name lands on one of yours (`feature-dev` shipping
`code-reviewer` versus your own `feature-dev-code-reviewer`), agentsync says so
and names both sides rather than picking a winner. MCP and LSP servers keep their ids too: two sources claiming one
server id is refused rather than renamed apart, because repointing a trusted
server's endpoint is a hijack, not a naming clash.

### Which agents a plugin fans out to

Two keys in `plugins/<id>.toml` decide where a plugin's components render. They
are enforced once, at the render waist, so every agent and every component kind
obeys them identically:

```toml
[plugin]
id            = "feature-dev@claude-plugins-official"
agents        = ["*"]        # your fan-out choice: which agents may receive it
native_agents = ["claude"]   # agents that install it THEMSELVES — do not project there
```

`agents` is the allowlist you control: `["*"]` (the default, and the same
meaning as omitting the key) sends the plugin to every enabled agent, or name
agents explicitly to narrow it.

`native_agents` is a statement about the world rather than a preference. When an
agent's own plugin manager already installs a plugin — you ran `/plugin install`
in Claude Code — agentsync must not ALSO project that plugin's components into
that agent's standalone paths, or you get two of every skill, subagent, and
command, and every hook fires twice. agentsync never disables a plugin inside
another tool's plugin manager (see [architecture.md § PluginIngester
(read-only)](architecture.md#pluginingester-read-only)), so the only way to
avoid the duplicate is not to project there.

**`import` asks, per plugin.** Importing a plugin out of an agent means, by
definition, that the agent has it installed, so `agentsync import claude:plugin`
stops and offers the deferral:

```
ℹ INFO   claude already installs the plugin "feature-dev".
         agentsync can leave those components to claude, or project its own copy alongside them.
  Let claude keep serving this plugin? [Y]es / [n]o, project it there too:
```

Accepting records `native_agents = ["claude"]`. Claude keeps serving the plugin
itself; every OTHER enabled agent still gets the fan-out — that is the whole
point of importing it.

Declining is a real choice, and agentsync says what it costs:

```
⚠ WARN   this will DUPLICATE the plugin "feature-dev"'s content in your claude
         harness — every skill, subagent and command it ships will appear twice,
         and its hooks will fire twice. Disable or uninstall "feature-dev" in
         claude now that agentsync is managing it.
```

That is a working setup **only** if you then turn the plugin off inside Claude
Code. Declining writes no key, so a later import asks again — and asks only if
the duplicate still exists. Once you disable the plugin natively, the question
stops being asked. To silence it while keeping the plugin enabled in both, write
`native_agents = []` by hand: an explicitly empty list means "defer to nobody"
and is preserved.

You are only asked when the answer can take effect — never for a plugin whose
`native_agents` you have already set, since a re-import preserves it. With
`--no-input`, or when stdin is not a terminal, the deferral is recorded without
asking and an INFO line says so: a scripted import must not silently produce two
of everything.

To hand a plugin over to agentsync completely, uninstall it in the agent
(`/plugin uninstall` in Claude Code) and drop that agent from `native_agents`.
The next apply projects the components there. Nothing about the agent's own
config is touched either way — the deferral lives entirely in your canonical
source, which is what keeps `apply` reproducible from a dotfiles repo alone
rather than dependent on whatever the machine happens to have installed.

Both keys survive re-installs and re-imports untouched (issue #140), so a
narrowed allowlist or an adopted plugin is never silently reset.

Because apply's plan never reads the destination, agentsync cannot notice a
plugin you install natively AFTER declaring it. `status` and `doctor` do read
it, and warn when a plugin is installed in an agent *and* projected there. Under
`--agents`, that check follows the agents you selected — so a narrowed run names
the enabled agents with a native plugin manager that it did not examine, rather
than letting silence read as a clean bill of health.

One caveat on `--lossless` (`plugin upgrade <id> --lossless` and
`plugin upgrade --all --lossless`; `plugin outdated` does not take the flag):
the check that decides whether an upgrade would lose something in translation
renders every *enabled* agent and does **not** honour a plugin's `agents` /
`native_agents`. So it can decline an upgrade over a loss that falls on an agent
this plugin is never projected to. It errs toward declining, never toward
upgrading; run `agentsync plugin explain <id>` to see which agents actually
receive the plugin, and re-run without `--lossless` if the affected one is not
among them. A bump the check could not evaluate at all is also excluded, but
reported separately — that refusal is not about targeting, and dropping the flag
is not the answer to it.

(A per-component `[plugin.overrides.<agent>]` table was specced but is **not
wired in v1** — the projector does not consult it; use the keys above.)

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
agentsync secret set github.token --stdin    # from stdin (best for scripts / 1Password CLI)
agentsync secret set github.token            # interactive prompt, echo off
agentsync secret edit                        # open the whole vault in $EDITOR
agentsync secret get github.token            # read one back (to verify)
```

`secret set` refuses an **empty or whitespace-only** value by default (a
fat-fingered paste or an empty `pbpaste`/`1password` pipe would otherwise store a
silently-broken secret that resolves to `""` at apply time); pass `--allow-empty`
to store an empty value deliberately.

`${secret:…}` is resolved at apply time and written into native config; `${env:…}`
pulls from the environment. The resolved value is **never** captured back into
your source — `agentsync diff` even redacts it so a piped diff can't leak it.

> ### ⚠ Back up your age key
> Secrets are encrypted to an age **recipient** (public key — safe to commit).
> Decryption needs the **identity** file (private key), which is per-machine.
> **agentsync does not back it up for you.** Lose it and you lose access to every
> encrypted secret. Stash it in a 1Password Secure Note or your machine-setup repo.

#### The `env` backend

`[secrets].backend` takes one of two values, **matched case-insensitively**
(`"age"`, `"Age"` and `"AGE"` are the same value — agentsync folds the name
before selecting a backend). `"age"` — everything above — resolves `${secret:…}`
from the age-encrypted vault. `"env"` resolves it from the process environment
instead, the same lookup `${env:…}` performs:

```toml
[secrets]
backend = "env"
```

Reach for it when something else already puts credentials in the environment —
`direnv`, the 1Password CLI, a CI secret store. There is no vault and no
identity file, so `recipient`, `identity_file` and `file` are unused; the
`agentsync secret` subcommands manage the age vault and refuse with a message
naming the backend they need. Both `agentsync check` and `agentsync doctor`
accept `backend = "env"` (in any casing). Any other **non-empty** value —
whitespace included, since the name is folded but never trimmed — is rejected by
both.

An absent or empty `backend` is not a third value but "no secrets configured" —
but the two commands do not report it the same way, because two different
configs land there:

- **No `[secrets]` block at all** (or a bare `[secrets]` header with nothing
  under it, which parses identically): `check` says nothing about it, and
  `doctor` prints one informational line — `• backend    not configured (skip —
  no [secrets] block)`.
- **A `[secrets]` block that is present but sets no `backend`** — it carries
  `recipient` / `identity_file` / `file`, but nothing switched the vault on:
  `doctor` **warns**, `⚠ backend    not set — ${secret:…} will not resolve (set
  "age" or "env")`, because that block is inert rather than absent. `check` still
  exits 0 on it, deliberately: a source carrying no `${secret:…}` reference
  applies cleanly with that block, so failing there would make `check` refuse a
  config `apply` accepts. When references *are* present, `check` fails on them,
  alongside `doctor` and `apply`.

`apply` treats an unrecognised *or* empty backend as *no* backend, so every
`${secret:…}` then fails to resolve.

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
`subagents/`, `commands/`, `hooks/`, `memory/`, `plugins/`, and `secrets/` — the
same files as the user tree, minus `.state/` (apply records state centrally under
`~/.agentsync/.state/`, keyed by project root). Author the project's config in
this tree:

```toml
# myrepo/.agentsync/agentsync.toml
[agents]
claude = { enabled = true }       # the agents THIS project renders to —
                                  # required; user-scope agents are never
                                  # inherited, so every collaborator gets the
                                  # same render
```

Declare agents with the project-scope agent commands instead of hand-editing:

```bash
agentsync agent add claude --scope project     # or --project <path>
agentsync agent list --scope project
agentsync agent disable claude --scope project
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
replaces a user entry with the same id/name, new entries are appended, and
project memory is appended after user memory. The project's `[agents]` table is
**authoritative** — project scope renders only to the agents the project itself
declares, never your user-scope agents. A project that declares none is a hard
error on every scope-aware render path — `apply`/`status`/`diff`/`reconcile`/
`plugin upgrade --all`/`check` (run `agentsync agent add <name> --scope project` to
fix); `import --scope project` still works before any agents are declared, so
you can bootstrap the tree from native config first.

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

`plugin outdated` is the polling verb of the daily loop: it re-fetches every
registered marketplace into the local cache and recomputes version pins, without
touching any agent config. `apply` then renders from that cache, so it's always
fast, offline, and reproducible.

```bash
agentsync plugin outdated                      # refresh cache + show pending bumps
agentsync plugin upgrade --all                 # re-pin every pending bump, then re-apply
agentsync plugin upgrade --all --lossless      # same, skipping bumps that would lose translation
agentsync plugin upgrade atlassian             # re-fetch one plugin, then re-apply
```

Both `upgrade` forms end in a re-apply, so an upgrade lands in your agents in one
command rather than leaving them stale until the next `apply`.

`plugin outdated` is not a pure read despite the `npm outdated` prior: it uses
the network and it writes state (each marketplace's fetch timestamp and head
SHA). Nor is it the *only* networked command — `plugin add`, `plugin
upgrade`, `marketplace add`, `import <agent>:plugin`, and `init <git-url>` all
fetch. It is simply the one the daily loop runs.

> **Removed:** the top-level `update` command is gone, with no alias. Move your
> cron lines over: bare `update` → `plugin outdated`, `--apply` → `plugin upgrade
> --all`, `--apply --auto-safe` → `plugin upgrade --all --lossless`.

Want nightly refreshes? agentsync ships no daemon — wire
`agentsync plugin upgrade --all --lossless` into your own cron / launchd /
systemd / Task Scheduler.

---

## Where did this file come from?

`diff` answers "what changed". `explain <path>` answers the other question — the
one a merged file like `~/.claude/settings.json` makes hard:

```bash
agentsync explain ~/.claude/agents/reviewer.md          # a whole file
agentsync explain ~/.claude.json#/mcpServers/github     # one merged key
agentsync explain ~/.claude.json --pointer /mcpServers/github   # same, unambiguous
agentsync explain ~/.claude.json --json                 # machine-readable
```

For each item it reports the **source of record** (`mcp/github.toml`,
`subagents/reviewer.md`, `memory/AGENTS.md` *plus its fragments*), the **plugin
origin** if the component came from a projection, the **adapter transform**
(what was reduced or dropped en route), the **ownership** — `managed`,
`untracked` (rendered, not applied yet), or `foreign` (yours; preserved by the
key merge) — the **drift class** in the classifier's own vocabulary, and which
`${secret:…}`/`${env:…}` references resolved there.

Three things worth knowing:

- **It prints metadata only.** No key values, no drift snippets, no file
  content. That is what lets it answer with a **locked secrets vault**, where
  `diff` has to fail closed rather than print cleartext it can't redact. Want
  the content? That's `agentsync diff <path>`.
- **It re-renders live**, like `diff` — so it is honest about an unapplied
  source tree instead of narrating the last apply.
- **One path can have several owners.** Agents share destinations (the breadth
  tier shares `~/.agents/skills/`), so output is grouped per owning agent, and
  two agents rendering *different* content to one path is reported as its own
  answer.

An unmanaged or typo'd path is reported distinctly from a clean one (with the
nearest managed path suggested), exactly as `diff [<path>]` does.

For per-plugin translation coverage — "what can each agent do with this plugin"
— use [`agentsync plugin explain`](#multi-agent-fan-out) instead.

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
| Hook | ◐ | ✗ | ◐ | ◐ | ◐ | ✗ | ✗ | ✗ | ✗ |
| LSP server | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |

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
| `doctor` | Diagnose setup: PATH, home/state writability, config schema, the `[secrets]` block (validated by the same rules `check` enforces), destination-git-backup mode + per-dir repo status; flags natively-installed plugins missing from source. | |
| `check` | Validate the **config**: schema lint plus every `${secret:}`/`${env:}` reference resolved. `--scope project`/`--project <path>` lints the project tree against the inherited user secrets backend. Its sibling is `doctor`, which validates the **machine**. (Renamed from `verify` — see [Upgrading](#command-reference).) | `--scope --project` |
| `agent add\|remove\|list\|enable\|disable <name>` | Manage the agent registry — the user's, or with `--scope project`/`--project <path>` the project tree's own `[agents]` declaration (which project scope renders from; never inherited). At project scope `disable --purge` touches only that project's rendered files. | `disable --purge --scope --project` |
| `skill\|subagent\|command\|hook\|lsp list` | List that component in the canonical source. Read-only by design: unlike an MCP server, none of these is flag-authorable — a skill is a *directory*, a subagent is a markdown file — so you author them on disk or capture them with `import`. | `--scope --project` |
| `migrate subagents` | One-shot move of the retired canonical `agents/` directory to `subagents/`, rewriting that tree's recorded `source_id` values. Run once per tree (`--scope project` / `--project <path>` for a project tree). Refuses, listing the names, if a file exists under both directories. | `--scope --project` |
| `mcp add\|remove\|list\|enable\|disable <name>` | Manage MCP servers. `enable`/`disable` flip the server's `enabled` bit — keeping the definition but stopping the render (`remove` deletes it). `--header "Name: Value"` (repeatable, http/sse only) sets request headers — the usual remote-auth secret site, e.g. `--header "Authorization: Bearer ${secret:TOKEN}"`. | `--type --command --args --url --env --agents --header` |
| `marketplace add\|remove\|list <url-or-name>` | Manage marketplaces. | |
| `plugin add\|upgrade\|enable\|disable\|remove <id[@marketplace]>` / `list` / `outdated` / `explain` | Manage plugins (the lifecycle subcommands all accept the same `id[@marketplace]` ref `add` accepts; the bare id also works, and a qualifier naming a different marketplace than the one the plugin was installed from is refused). `outdated` **(network)** polls the marketplaces and reports pending bumps — it also writes each marketplace's fetch timestamp + head SHA to state. `upgrade` **(network)** re-fetches one plugin, or with `--all` every plugin with a pending bump, and **re-applies** in both cases; `--lossless` skips an upgrade that would introduce a new translation loss, reporting it. `explain` shows per-agent translation coverage. | `outdated` · `upgrade [<id>] --all --lossless --scope --project` · `explain [<id>...] --all --json` |
| `secret set\|get\|list\|remove <key>` / `secret edit` | Manage age-encrypted secrets (`list` prints KEYS only; `edit` opens the whole vault, no `<key>`; `set` refuses an empty value unless `--allow-empty`). All five require `[secrets].backend = "age"` (matched case-insensitively, exactly as `apply` matches it) and `[secrets].identity_file`; the three that re-encrypt — `set`, `edit`, `remove` — additionally require `[secrets].recipient`. | `set --stdin` |
| `apply` | Render source → write agent configs (offline). Git-versions each user-scope destination dir into a local-only repo (opt-out) so a bad apply is revertible. A delete-only run (a component removed from source) reports `removed: N key(s), M file(s)` — key-removals and file-deletes counted distinctly — and a mixed run `applied: X ops, removed: …`, rather than mislabeling itself `up to date`/`applied: 0 ops`; `--dry-run` previews the same removal counts. | `--agents --dry-run --scope --project --no-git-backup` |
| `revert <agent>` | Roll a destination dir back to a prior apply checkpoint (append-only). Default undoes the most recent apply; prints an out-of-sync notice. `--to` must name one of the dir's own checkpoints (the current one or an ancestor) — anything else is refused. A dir under which a foreign git repo has appeared (or that isn't an agentsync-managed backup) is an **error** when you name the agent, and a **skip with a warning** under `--all` — strictness follows the invocation; there is no `--strict` flag. | `--agents --to --all --dry-run` |
| `status` | Summarize drift/pending across agents; notes natively-installed plugins not yet in source. Skill directories collapse to one summary row by default (`--verbose` expands them). The formatted report shows `converged` items as `clean` (both mean apply has nothing to do); `--json` keeps the two distinct. `--legend` prints a standalone glossary of all nine drift classification statuses and exits (rejects combination with `--json`/`--exit-code`/`--agents` rather than silently ignoring them). `--exit-code` makes it a CI gate: exit `2` when any drift is detected, `0` when clean. | `--agents --verbose --legend --scope --project --json --exit-code` |
| `diff [<path>]` | Show pending/drift changes; secrets redacted. `<path>` is a filesystem path; an unmanaged/typo'd path is reported distinctly from a clean one. `--agents` narrows to an agent allowlist (like `status`); `--exit-code` exits `2` when any hunk exists, `0` when clean. | `--agents --scope --project --json --exit-code` |
| `reconcile` | Interactively merge drift back into source. | `--agents --auto-writeback --auto-override --auto-safe --scope --project` |
| `import <agent>[:<component>[:<name>]]` | Capture native config into source; drop parts to import a whole component or the agent's full config. Includes `plugin` (Claude), which re-fetches installed plugins + marketplaces **(network)**. `--scope project` reads the agent's *native project-scope* config (e.g. `<root>/.claude/`) and captures it into the project tree `<root>/.agentsync/`, seeding central state with the project scope + root. Plugin import is user-scope only. | `--dry-run --scope --project` |
| `explain <path>[#<pointer>]` | Show what produced a destination file (or one merged key): source of record, plugin origin, adapter transform, ownership, drift class, and any `${secret:…}` references. **Metadata only** — never destination content, so it answers even when the secrets vault is locked (where `diff` must fail closed). `--pointer` is the unambiguous alternative to an in-argument `#`. Project scope is inferred when the path lies inside a project tree. | `--pointer --scope --project --json` |
| `version` | Print version information (alias for `--version`). | |

Global: `--scope user|project` and `--project <path>` are **root flags**, declared
once and accepted by every command that operates on a scoped source tree (`init`,
`agent`, `mcp`, `apply`, `check`, `status`, `diff`, `reconcile`, `import`,
`explain`, `migrate`, `plugin upgrade`, and the component `list`s). A
command that *cannot*
honor them — `doctor`, `revert`, `version`, and the `plugin` / `marketplace` /
`secret` groups, all of which act on per-machine state — **refuses them with the
reason** rather than accepting and ignoring them.

`-v/--verbose` for verbose logging on any command (in `status` it also
expands each collapsed skill directory back to one row per bundled file).
`--color=auto|always|never` controls whether output is styled with ANSI color
and bold (default `auto` — on for a TTY, off when piped/redirected; honors
`NO_COLOR`). Color is a *second* signal, never the only one: every diagnostic
also carries a level label — `✗ ERROR`, `⚠ WARN`, `ℹ INFO` (and a reserved
`• DEBUG`, which nothing emits today) — on
**stderr**, so severity survives being piped into a file or a CI log. Command
*results* (a `--json` payload, a `status` table, a `diff`, a `list`) go to
stdout with no label, and a success outcome leads with an emoji instead —
`🎉 applied: 12 ops`, `✅ added agent: claude`, `🧹 removed mcp server: github`,
`📥 imported 4 items from claude`, `🔙 reverted…`, `✨ …initialized`. That split is why a
warning never lands in the middle of `status --json`. `--agents <list>` is the **one** way to say which agents a command
acts on: `apply`, `status`, `diff`, `reconcile`, and `revert` all take it, with
identical parsing and the same `*` = all-enabled convention (an empty or unknown
value is rejected identically by all five). On `revert` it is a spelling of the
positional form, so `--agents`, a positional agent, and `--all` stay mutually
exclusive. Every `list` accepts `ls`, and every `remove` accepts `rm`. `status
--json` and `diff [<path>] --json` emit
the structured report instead of the formatted one, suitable for CI gates and
dashboards (`status --json` is never collapsed — it carries every tracked file;
`diff --json` masks the same resolved secrets the formatted diff does; its
`pointer` field is an RFC-6901 pointer for a merged key, and one of three
pseudo-pointers for a whole-file finding that is not a text difference —
`mode` for a content-identical permission change, `symlink` for a symlinked
destination agentsync is not comparing through, `shape` for a destination
that is not a readable regular file — wrong shape, or cannot be stat'd). For a
gate that should **fail the build** on drift, add `--exit-code`: `status
--exit-code` / `diff --exit-code` exit `2` when drift/hunks exist and `0` when
clean (exit `2` is distinct from the generic error exit `1`, and prints no extra
error line). Interactive prompts (e.g. the scope menu) always go to **stderr**,
so a `--json` payload piped from stdout is never corrupted.

The formatted `status` report shows a `converged` item as `clean` — the two are
distinct in the internal drift classifier (converged means source *and*
destination changed independently but landed on the same value, versus clean
where neither changed), but both mean apply has nothing to do, so the
dashboard folds them into one word and one tally. `status --json` keeps the
real classification. `status --legend` prints a standalone glossary of all
nine classification statuses (including `clean`/`converged` spelled out
separately) and exits without running the drift scan; a run whose summary has
anything to explain ends with a one-line hint pointing at it, suppressed
whenever there's nothing tracked to explain (no agents enabled, or an enabled
agent that renders nothing yet).

---

## Troubleshooting & environment overrides

The [README](../README.md#troubleshooting) carries the full troubleshooting list
and the complete environment-variable table. The ones you'll reach for most:

| Env var | Purpose |
|---|---|
| `AGENTSYNC_HOME` | Override the `~/.agentsync/` location. |
| `AGENTSYNC_ALLOW_SYMLINK_DEST=1` | Write through symlinked destinations, and compare through them when reading (e.g. chezmoi-managed files). Needed by `apply` and by `status`/`diff`/`reconcile`/`explain`. |
| `AGENTSYNC_ALLOW_INSECURE_URLS=1` | Accept `http://`/`git://` plugin/marketplace sources. |
| `AGENTSYNC_ALLOW_OFFLINE_VERIFY=1` | Let `check` validate reference *shape* only, skipping resolution (CI without an age key). |
| `AGENTSYNC_NO_UPGRADE_NOTICE=1` | Never show the one-time first-run-after-upgrade notice. |

Quick hits:

- **`${secret:foo}` not resolving?** `agentsync secret get foo` to confirm the
  key exists in the decrypted vault.
- **`plugin outdated` can't fetch a marketplace?** Sanity-check the URL with
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
