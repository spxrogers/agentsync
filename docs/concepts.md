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
- **Conflict** — *both* happened. agentsync stops and asks you to reconcile.

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
back into the canonical model. Claude, OpenCode, and Codex are real adapters;
Cursor is registered but no-op (see the [capability matrix](capability-matrix.md)).
Adding an agent means adding an adapter — the canonical schema never changes.

### Apply / Render
`agentsync apply` runs the **render** pipeline: load the source → resolve
secrets → ask each enabled adapter to project the model into a set of file
operations → write them atomically → record hashes in state. Apply is
**local-only and offline** — it never hits the network.

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
| **drift** | the destination was edited | block; suggest `reconcile` |
| **converged** | source and dest changed to the *same* value | refresh state silently |
| **conflict** | source and dest changed to *different* values | block; require `reconcile` |
| **new** | brand-new item, nothing on disk | create |
| **foreign-collision** | a pre-existing file agentsync didn't write | back it up, then write |
| **orphan** | removed from source, still on disk | delete |
| **orphan-drifted** | removed from source, but the dest was also edited | warn; ask for explicit action |

Granularity is **per-key** for structured files (JSON/JSONC/TOML, tracked by
JSON pointer) and **per-file** for everything else. Keys agentsync never wrote
are **foreign keys** — surfaced for awareness but never touched.

### Reconcile
The interactive merge UX for drift and conflicts. For each drifting item you
choose `[w]rite-back` (adopt the dest edit into source), `[o]verride` (re-impose
source), `[s]kip`, `[i]gnore` (add to `ignore.toml`), `[d]iff`, or `[q]uit`.
Bulk hotkeys (`W`/`O`/`S`) and non-interactive flags (`--auto-writeback`,
`--auto-override`, `--auto-safe`) exist for scripting.

### Scope & the project marker
Config applies at **user** scope (the whole machine) or **project** scope (one
repo). A repo opts in by dropping a `.agentsync.toml` marker at its root;
agentsync walks up from the current directory to find it. The marker can add
project-only MCP servers, disable plugins, and merge extra memory.

### Secrets: `${secret:…}` and `${env:…}`
The canonical source never stores cleartext credentials. You write
`${secret:github.token}` or `${env:HOME}`; agentsync **resolves** these at apply
time — `${secret:…}` from an age-encrypted file, `${env:…}` from the
environment. The public **recipient** key is safe to commit; the private
**identity** key is per-machine and never backed up for you.

> A central invariant: a *resolved* cleartext secret must never be written back
> into the canonical source. The type system enforces this — see
> [architecture](architecture.md#secrets-how-the-leak-is-prevented) and `CLAUDE.md`.

### State (`~/.agentsync/.state/`)
Gitignored bookkeeping: `targets.json` (the last-applied hashes that make drift
detection possible), the apply lock, the two-phase write staging dir, first-apply
backups, and the marketplace/plugin cache. Keys are stored `${HOME}`-relative so
state is portable across machines.

### Marketplace / Plugin / Projection
A **marketplace** is a registry of plugins (Claude's marketplace format). A
**plugin** is a bag of components — MCP servers, skills, subagents, commands,
hooks, LSP servers. **Projection** translates each component independently into
each target agent, which is where fan-out happens: install once, land on every
enabled agent that supports the component.

### Translation report & coverage
Every `apply`, `verify`, and `explain` ends with a report showing, per plugin
per agent, what landed:

- **✓ native** — full fidelity; the agent has the concept directly.
- **◐ projected** — lossy but defensible translation, explicitly reported.
- **✗ skipped** — no honest translation exists; logged so it's never silent.

### Update (the networked verb)
`agentsync update` is the command that touches the network in the daily loop: it
polls marketplaces, refreshes the cache, and recomputes version pins. `apply`
then runs entirely from cache. This split keeps `apply` fast, reproducible, and
offline-safe. (The one other networked path is setup-time: `import`'s `plugin`
component re-fetches the plugins + marketplaces it captures from an agent's
native config.)

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

   agentsync update  ──network──▶  refresh marketplace cache & pins
                                   (then apply renders from cache, offline)
```

Next: see the [architecture](architecture.md) for how these are implemented, or
jump straight to the [user guide](user-guide.md) to get hands-on.
