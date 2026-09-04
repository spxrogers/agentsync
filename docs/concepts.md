# Concepts & glossary

agentsync borrows its mental model from [chezmoi](https://www.chezmoi.io/): you
keep one **canonical source** of truth, you **apply** it to produce the real
files agents read, and when something edits those files out from under you,
agentsync detects the **drift** and helps you **reconcile** it.

Read this page once and the rest of the docs — the [architecture](architecture.md),
the [user guide](user-guide.md), and `agentsync --help` — will click into place.

---

## The three-state model

Everything agentsync does is a comparison between three states of the same
logical thing (an MCP server, a memory file, a plugin's slash command):

| State | What it is | Where it lives |
|---|---|---|
| **Source** | What *you* committed — the intent. | `~/.agentsync/` (a git repo you own) |
| **Target** | What the source *renders to* for a given agent, computed fresh in memory at apply time. | nowhere on disk — it's transient |
| **Destination** | What is *actually on disk* in each agent's native config right now. | `~/.claude.json`, `~/.config/opencode/`, … |

```
   Source                Target                 Destination
  ~/.agentsync/   render   (in-memory)   write    ~/.claude/…
  hand-edited    ───────▶  per-agent    ───────▶  ~/.config/opencode/…
  TOML + .md               FileOps                native config files
       ▲                                                │
       │                      capture                   │
       └────────────────────────────────────────────────┘
              (write native edits back into source)
```

The genius of the model is that **drift is just a hash comparison**:

- **Drift** — the destination changed since agentsync last wrote it
  (`hash(destination) ≠ hash(last-applied)`). Something edited the native file.
- **Source change** — you edited the canonical source
  (`hash(target) ≠ hash(last-applied)`). A normal pending change.
- **Conflict** — *both* happened. `apply` overwrites the destination anyway
  (no backup, no prompt — see below); `reconcile` is how you catch it first.

---

## Core terms

### Canonical source (`~/.agentsync/`)
The single directory you own and (optionally) commit to git. It holds small,
hand-editable TOML files (one MCP server per file, one plugin per file) plus
markdown for memory and skills. A **skill** follows the [Agent Skills](https://agentskills.io)
spec — it is a *directory* `skills/<name>/` whose only required member is
`SKILL.md`, and any bundled `scripts/`, `references/`, `assets/`, or nested files
are carried verbatim (binary included), not just the `SKILL.md`. Override the
canonical location with `AGENTSYNC_HOME`.
There is **no hidden internal representation** — the Go structs that parse these
TOML files *are* the canonical model.

### Adapter
A per-agent translator. Each adapter knows how to **render** the canonical model
into one agent's native config format and how to **ingest** that native config
back into the canonical model. Claude, OpenCode, Codex, and Cursor are all real
adapters (see the [capability matrix](capability-matrix.md)). Adding an agent
means adding an adapter — the canonical schema never changes.

### Apply / Render
`agentsync apply` runs the **render** pipeline: load the source → resolve
secrets → ask each enabled adapter to project the model into a set of file
operations → write them atomically → record hashes in state. Apply is
**local-only and offline** — it never hits the network. Rendered memory files
also get a short **managed banner** prepended (naming the file, pointing edits
back at `.agentsync/memory/AGENTS.md` + `agentsync apply`); it lives only in the
rendered file — stripped on ingest/capture, never written to the canonical
source — and is on by default (`[memory] banner = false` opts out).

### Capture / Ingest / write-back
The reverse direction. **Ingest** reads an agent's native config back into the
canonical model; **capture** persists that back into `~/.agentsync/`. This is how
`agentsync import` (pull a native edit into the source) and the reconcile
`[w]rite-back` action work. All write-back funnels through one place
(`internal/capture`) so secrets get re-referenced and never leak as cleartext.

### Drift & the 3-way classifier
For every managed item, agentsync holds three hashes — source (`H_src`),
last-applied (`H_applied`, from state), and destination (`H_dest`) — and
classifies the item into exactly one of nine cases:

| Class | Meaning | What `apply` does |
|---|---|---|
| **clean** | all three agree | nothing |
| **pending** | you changed the source | write the new source |
| **drift** | the destination was edited | overwrite it — no backup, since agentsync already owns it; `reconcile` is how you keep the edit instead |
| **converged** | source and dest changed to the *same* value | refresh state silently |
| **conflict** | source and dest changed to *different* values | overwrite it — no backup, same reason; `reconcile` is how you merge the edit instead |
| **new** | brand-new item, nothing on disk | create |
| **foreign-collision** | a pre-existing file agentsync didn't write | back it up, then write |
| **orphan** | removed from source, still on disk | delete, if `apply` still reclaims that kind of file |
| **orphan-drifted** | removed from source, but the dest was also edited | back it up, then delete, if `apply` still reclaims that kind of file |

`apply` never blocks or prompts on any of these — it always finishes the run.
Only **foreign-collision** and **orphan-drifted** get a per-file backup before
the write/delete: those are the two cases where the destination holds content
agentsync doesn't already own in state. **drift** and **conflict** ARE
already state-owned by definition, so the writer's per-file backup path skips
them and overwrites directly — the hand edit is simply lost, with no
per-file copy of it kept. **orphan**/**orphan-drifted** are hedged with "if
`apply` still reclaims that kind of file": `apply` only reclaims a
skill/subagent/command destination when its source stops rendering it — for
every other kind (memory, MCP/hook/LSP entries), `status` still reports the
class, but the next `apply` doesn't touch the destination at all; it simply
drops the stale bookkeeping. `status`/`diff`/`reconcile` are how you catch a
drift/conflict/orphan-drifted item BEFORE the next `apply` acts on it; a
user-scope apply's destination git-versioning (opt-out, default `prompt`) is
the after-the-fact recovery net when enabled — see "Rolling back a bad
apply" in the [user guide](user-guide.md). It's absent entirely at
project scope, so a project-scope drift/conflict overwrite has no automatic
recovery beyond your own source control of the destination.

`agentsync status`'s formatted dashboard displays a **converged** item as
**clean** — both mean `apply` has nothing left to do, and the distinction
above is bookkeeping the classifier and `status --json` need, not something a
human scanning the report benefits from. `status --legend` prints this table
(as a CLI reference); `status --json` always reports the real class. One fold
applies to both: a clean or converged whole file whose permission bits differ
from what `apply` writes is reported as **drift**, because the next apply
changes it.

Granularity is **per-key** for structured files (JSON/JSONC/TOML, tracked by
JSON pointer) and **per-file** for everything else. Keys agentsync never wrote
are **foreign keys** — surfaced for awareness but never touched.

### Reconcile
The interactive merge UX for drift and conflicts. For each drifting item you
choose `[w]rite-back` (adopt the dest edit into source), `[o]verride` (re-impose
source), `[s]kip`, `[i]gnore` (add to `ignore.toml`), `[d]iff`, or `[q]uit`.
Bulk hotkeys (`W`/`O`/`S`) and non-interactive flags (`--auto-writeback`,
`--auto-override`, `--auto-safe`) exist for scripting.

### Scope & the project source tree
Config applies at **user** scope (the whole machine) or **project** scope (one
repo). A repo opts in by holding a `.agentsync/` **source tree** at its root —
the same on-disk layout as `~/.agentsync/` (an `agentsync.toml` plus `mcp/`,
`skills/`, `subagents/`, `commands/`, `hooks/`, `lsp/`, `memory/`). Scaffold it with
`agentsync init --scope project`; commit it to share project agent config with
collaborators. Project scope is always an **explicit opt-in** — pass `--scope
project` (walks up from cwd for the tree) or `--project <path>`. Commands default
to **user** scope; the one exception is that running with no scope *inside* a
project tree is ambiguous, so agentsync **prompts** for project-vs-user (or, when
non-interactive / `--no-input`, errors rather than guessing). It never silently
acts on a tree it merely detected. The project tree is **overlaid** onto the
user canonical: a project entry replaces a user entry with the same id/name, new
entries are added, and project memory is appended. The project's `[agents]`
table is **authoritative**: project scope renders only to the agents the project
itself declares, never the user's enabled agents — so a committed tree produces
the same render on every collaborator's machine. A project that declares no
agents is a hard error on every scope-aware render path —
`apply`/`status`/`diff`/`reconcile`/`plugin upgrade`/`check` (declare agents
with `agentsync agent add <name> --scope project`); `import --scope project`
stays available for bootstrapping the tree from native config first. A
project `plugins/<id>.toml` with `disabled = true`
suppresses that plugin's components in the repo. (The retired M5 single-file
`.agentsync.toml` marker is no longer read; `agentsync init --scope project`
prints how to migrate.)

### Secrets: `${secret:…}` and `${env:…}`
The canonical source never stores cleartext credentials. You write
`${secret:github.token}` or `${env:HOME}`; agentsync **resolves** these at apply
time — `${secret:…}` from an age-encrypted file, `${env:…}` from the
environment. The public **recipient** key is safe to commit; the private
**identity** key is per-machine and never backed up for you.

> A central invariant: a *resolved* cleartext secret must never be written back
> into the canonical source. The type system enforces this — see
> [architecture](architecture.md#8-secrets--how-the-leak-is-prevented) and `CLAUDE.md`.

### State (`~/.agentsync/.state/`)
Gitignored bookkeeping: `targets.json` (the last-applied hashes that make drift
detection possible), the apply lock, the two-phase write staging dir, first-apply
backups, and the marketplace/plugin cache. Each entry is addressed by an opaque,
injective key that stores the project root and destination path `${HOME}`-relative,
so state stays portable across machines.

### Destination git backup (rollback history)
Optionally, each **user-scope destination dir** (`~/.claude`, `~/.codex`, …) gets
its **own local-only git repo**: `apply` records a checkpoint commit after every
run that changes managed files there, and `agentsync revert` rolls a dir back to a
prior checkpoint. This is distinct from `.state/` — `.state/` is agentsync's
operational memory (hashes for drift, narrow capped foreign-collision backups),
whereas these repos are a durable, browsable, revertible rollback history for the
*rendered destination files*. They are governed by the
`[destination_directory_git_backup]` table and are **never pushed** — the rendered
files hold secrets in cleartext, so the history stays local (the canonical source
you push still carries only `${secret:…}` references). Because that history holds
cleartext, the local `.git` directory is hardened to `0700` on **POSIX** to limit
at-rest exposure; on **Windows** this chmod is a silent no-op, so the filesystem
ACLs — not a mode bit — are the boundary there. See `apply`/`revert` in the
[user guide](user-guide.md#command-reference) and
[architecture §4](architecture.md).

### Marketplace / Plugin / Projection
A **marketplace** is a registry of plugins (Claude's marketplace format). A
**plugin** is a bag of components — MCP servers, skills, subagents, commands,
hooks, LSP servers. **Projection** translates each component independently into
each target agent, which is where fan-out happens: install once, land on every
enabled agent that supports the component.

**Apply fans out the components, not the plugin itself.** agentsync owns
plugins in `~/.agentsync/plugins/` and writes each plugin's *components* to the
agent's native paths (skills to `~/.claude/skills/<name>/`, MCP to
`mcpServers`, etc.). It deliberately does NOT write enablement metadata
(Claude's `enabledPlugins`, Codex's `[plugins."x@y"]`) back to the agent's
config — once the components land at native paths the agent reads them as
regular components, and writing the enablement back would pick a fight with
the agent's own `/plugin disable` UI on every apply. The `PluginIngester`
interface is **read-only by design**: `import` captures plugin enable-state
for discovery, `apply` never re-emits it. See
[architecture.md § PluginIngester (read-only)](architecture.md#pluginingester-read-only)
for the full rationale.

**An agent that installs a plugin itself is not projected to.** Because apply
never writes plugin enablement back, a plugin the agent's own manager installs
stays installed there — so projecting that plugin's components into the same
agent's standalone paths would DUPLICATE every one of them (and double-fire its
hooks). `plugins/<id>.toml` therefore carries two targeting keys: `agents`, your
fan-out allowlist, and `native_agents`, the agents that serve the plugin
themselves. A component renders for an agent only if `agents` targets it and
`native_agents` does not claim it. `import` offers `native_agents` per plugin for the
agents it discovered the plugin installed in, so the import→apply round trip
does not manufacture a duplicate by default; declining warns that it will, and
uninstalling the native copy and dropping the entry hands the plugin to
agentsync. Both gates are enforced in ONE place — the render
waist (`source.FilterForAgent`, via `secrets.Resolved.ForAgent`) — never in an
adapter. See the [user guide](user-guide.md).

**Plugin components are namespaced by their plugin.** Because apply flattens
every enabled plugin's components into one destination directory, two plugins
shipping a same-named component would render two files at one path — so a
plugin-provided subagent, skill, or command renders as `<plugin>-<name>`:
`feature-dev`'s `code-reviewer` lands as `feature-dev-code-reviewer`. Components
you hand-author in `~/.agentsync/` are never renamed. A plugin's derived name can
still land on one of yours (the mapping is not injective); agentsync reports that
clash naming both sides rather than letting either win silently. See
[architecture.md § Plugin component namespacing](architecture.md#plugin-component-namespacing).

### Translation report & coverage
Every `apply` and `plugin explain` ends with a report showing, per plugin per agent,
what landed (`check` only schema-lints the source and validates secrets — it
does not project plugins or print a coverage report):

- **✓ native** — full fidelity; the agent has the concept directly.
- **◐ projected** — lossy but defensible translation, explicitly reported.
- **✗ skipped** — no honest translation exists; logged so it's never silent.

A row can also report that a plugin contributed nothing to an agent by
*configuration* rather than by failed translation — `disabled` for a plugin
turned off in this scope, `not-targeted` when its `agents` allowlist excludes the
agent, and `native` when its `native_agents` list defers to that agent's own
plugin manager. These are deliberate outcomes, so they are never rendered in the
✗ failure vocabulary.

### Polling (the networked verb of the daily loop)
`agentsync plugin outdated` is the command that touches the network in the daily
loop: it polls marketplaces, refreshes the cache, and recomputes version pins.
`agentsync plugin upgrade --all` then re-pins every pending bump and re-applies.
`apply` itself runs entirely from cache — the split keeps it fast, reproducible,
and offline-safe. It is not the only networked command (`plugin add`,
`marketplace add`, `import <agent>:plugin`, and `init <git-url>` all fetch), just
the one the loop runs.

---

## How the verbs relate

```
   you edit ~/.agentsync/                      an agent edits its own config
            │                                              │
            ▼                                              ▼
   agentsync apply  ──writes──▶  native config  ──drift──▶  agentsync status
   (source ▶ dest)                                          agentsync diff
                                                                  │
                                                                  ▼
                                              agentsync reconcile / import
                                                  (dest ▶ source capture)

   agentsync plugin outdated ─network─▶ refresh marketplace cache & pins
   agentsync plugin upgrade --all      re-pin pending bumps, then re-apply
                                       (plain apply renders from cache, offline)
```

Next: see the [architecture](architecture.md) for how these are implemented, or
jump straight to the [user guide](user-guide.md) to get hands-on.
