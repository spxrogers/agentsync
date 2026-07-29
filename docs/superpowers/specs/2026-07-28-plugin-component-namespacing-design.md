# Namespace plugin-provided components by plugin — Design Spec

**Date:** 2026-07-28
**Status:** Approved in brainstorming; amended after a four-lens adversarial
review — see [Amendments](#amendments-after-review) for the two design points
this document originally got wrong.
**Issue:** [#211 — Cross-plugin subagent name collisions are unresolvable — the codex error names a remedy the user cannot perform](https://github.com/spxrogers/agentsync/issues/211)

---

## Summary

Two installed plugins that each ship a component with the same name make
`agentsync status` and `agentsync apply` exit 1, and every remedy the error
suggests is one the user structurally cannot perform. The reproduction is two
*stock official* plugins — `feature-dev@claude-plugins-official` and
`pr-review-toolkit@claude-plugins-official` — each shipping
`agents/code-reviewer.md`.

The fix: **plugin-provided subagents, skills, and commands are always namespaced
by their plugin**, at projection time. `feature-dev` + `code-reviewer` renders as
`feature-dev-code-reviewer`; `pr-review-toolkit` + `code-reviewer` renders as
`pr-review-toolkit-code-reviewer`. A component the user hand-authored in
`~/.agentsync/` is never renamed.

---

## Goals / Non-goals

**Goals**

- A stock two-plugin install applies cleanly, with both components reachable.
- Plugin components get stable, predictable names that do not depend on what
  else is installed.
- The collision detector stays, for the cases namespacing cannot reach, and its
  message becomes actionable.
- Existing users' stale pre-rename destination files are removed, not orphaned.

**Non-goals**

- A canonical-side override/precedence knob (let the user pin which plugin wins
  a contested name, or rename a plugin-provided component). Deferred: automatic
  namespacing makes the reported case work without one.
- Fixing `import`'s re-capture of plugin-projected components (see
  [Known adjacent gap](#known-adjacent-gap)).

---

## Why the fix cannot live in an adapter

Both plugins project to `source.Subagent{Name: "code-reviewer"}` — the *file
stem* collides, not merely the frontmatter `name`. Plugin provenance is dropped
at `internal/marketplace/loadprojected.go:91`, a plain
`c.Subagents = append(c.Subagents, proj.Subagents...)`, and `source.Subagent` has
nowhere to keep it. By the time any adapter can detect the collision, the one
piece of information needed to resolve it is already gone.

It is also not codex-specific. The codex adapter errors first
(`internal/adapter/codex/subagent.go:65`), but the Claude adapter emits two write
ops at the same `~/.claude/agents/code-reviewer.md`. The `seen` map in
`internal/render/pipeline.go` is not reset per agent, so its cross-agent
divergence guard fires *within* a single adapter too, aborting with a message
that misattributes the conflict to a second agent:

```
agent "claude" renders different content than an earlier agent for the same path …
```

Skills (`skills/<name>/SKILL.md`) and commands (`commands/<name>.md`) are the
same class: name-keyed components rendered to a name-derived destination path.

---

## Design

### 1. Rename at projection, not in adapters

`projectOnePlugin` (`internal/marketplace/loadprojected.go`) already holds the
plugin's filesystem id. After `projectWithFuncs` returns, each projected
subagent, skill, and command is stamped with its provenance and its `Name` is
rewritten to the namespaced form.

Because `Name` is what every adapter derives its destination path and identity
from, all render sites become correct with **no adapter changes**. The stamping
runs *after* `resolveConflicts`, so the intra-plugin `dedupOrConflict`
`reflect.DeepEqual` comparison is unaffected.

`ProjectInstalled` shares `projectOnePlugin`, so `agentsync plugin explain`
reports the same namespaced names the render produces.

### 2. Separator: `-`

Forced, not chosen. Claude Code documents a subagent `name` as a "Unique
identifier using lowercase letters and hyphens", so `:` is not available — the
familiar `plugin:agent` form is a *scoped identifier* Claude Code derives from
the plugin directory, never a value written into a `name` field. Codex states no
charset rule and treats `name` as the agent's identity, so a hyphenated name is
equally valid there.

The derived name is validated with `source.ValidateComponentID`, which already
rejects path separators, `..`, `:`, and control/bidi runes — so a pathological
plugin id cannot produce an unwritable or deceptive component name.

### 3. Schema

`source.Subagent`, `source.Skill`, and `source.Command` each gain:

| Field | Meaning |
| --- | --- |
| `Plugin string` | Providing plugin's id; empty for a hand-authored component |
| `BaseName string` | The pre-namespace name, for reporting and `explain` |

Both are `toml:"-"`. These are file-backed components whose canonical form is a
file on disk, and a projected component is never written back into
`~/.agentsync/`, so neither field is ever serialized.

Safe against `TestNewSecretFieldGuard`: that guard covers `MCPServerSpec`,
`LSPServerSpec`, `Hook`, and their wrappers. Subagents, skills, and commands are
text components the secret walker deliberately never visits, so no new field
joins `walkSecretFields`.

### 4. Frontmatter `name` follows the rename

Renaming only `Name` would leave both components still colliding:

- The codex adapter prefers `Frontmatter["name"]` over `s.Name` when deriving
  the Codex agent identity (`internal/adapter/codex/subagent.go:58`).
- Claude's Agent Skills require the frontmatter `name` to match the skill
  directory name.

So projection also rewrites `Frontmatter["name"]` **when the key is present**,
leaving it absent when it was absent. That preserves the #144 contract that a
deliberately-divergent frontmatter name survives Render → Ingest, while keeping
the identity consistent with the path.

### 5. The hard error stays; the message is fixed

Namespacing cannot reach every collision:

- two hand-authored canonical components with the same effective name;
- the pathological case of plugin `a` shipping `b-c` against plugin `a-b`
  shipping `c`.

The detector in `codex/subagent.go` remains. Its message currently prints
`%q and %q` from two file stems, which for the reported case renders as
`"code-reviewer" and "code-reviewer"` — the same string twice, with no origin.
It gains provenance (`from plugin X` / `hand-authored`) so the half of the
message meant to identify the conflict is informative precisely when it fires.

### 6. Orphan reclamation extended to subagents and commands

`skillOrphanDeletes` (now `orphanDeletes`, `internal/render/state_apply.go`) reclaims only
`skills/` SourceIDs. `PruneStaleState` drops the state entry for a destination
that stopped being rendered but does not delete the file.

Without extending reclamation, always-namespacing would leave every existing
user with a stale `~/.claude/agents/code-reviewer.md` beside the two new
namespaced files — and Claude Code would load it. The fix would *add* a
duplicate agent rather than remove one. Reclamation therefore extends to the
`subagents/` and `commands/` SourceID prefixes, matching how skills already
behave.

### 7. Upgrade notice

Always-namespacing renames every plugin-provided handle for every user, so it
gets an `upgradeNotices` entry (the #203 mechanism) and the matching section in
`website/src/content/docs/reference/upgrading.mdx`, per that table's documented
contract that the two are maintained in parallel.

---

## Testing

Anchored to the on-disk artifact, per the CLAUDE.md fidelity rule — the oracle is
the rendered file tree, not the parsed model.

- **Cross-plugin collision, end to end.** Fixtures of two plugin trees each
  shipping `agents/code-reviewer.md` with differing content. Assert the render
  produces two distinct destination files with distinct frontmatter names, and
  that apply succeeds where it previously exited 1.
- **Same for skills and commands**, since they share the failure class.
- **Hand-authored components are untouched** — a canonical `subagents/foo.md`
  keeps the bare name `foo` alongside a plugin that also ships `foo`.
- **Frontmatter `name` absence is preserved** — a component with no `name` key
  does not gain one.
- **Claude-side path collision regression**, pinning that the pre-fix failure
  came from the single-adapter path through `internal/render/pipeline.go`.
- **Orphan reclamation** — a destination file written under a pre-rename name is
  deleted on the next apply.
- **The hard error still fires** for two hand-authored same-name components, and
  its message names provenance.

---

## Known adjacent gap

`importSubagent` walks the *native* ingested canonical and has no way to tell a
plugin-projected file from a hand-authored one, so `agentsync import
claude:subagent` after an apply copies plugin components into
`~/.agentsync/subagents/`. Those then collide with the plugin's own projection on
the next apply.

**This is pre-existing and independent of this change** — it reproduces today
with any single non-colliding plugin component. The provenance fields added here
would make a filter cheap, but it changes `import` semantics and belongs in its
own change. Filed separately.

---

## Docs touched in the same commit

`docs/concepts.md`, `docs/architecture.md`, `docs/capability-matrix.md`,
`docs/components.md`, `CHANGELOG.md`, and
`website/src/content/docs/reference/upgrading.mdx`.

---

## Amendments after review

Two things in the design above were changed during review. They are recorded here
rather than edited away, because both were wrong for reasons worth keeping.

**§2 said the derived name is validated with `source.ValidateComponentID` at
projection. That validation was removed.** It was a *second*, stricter rune set
than the projection's own `validateProjectedName` (which permits `:` and control
runes), so a plugin that projected fine before started hard-failing — and
`loadProjected` propagates a projection error regardless of `lenient`, taking
down `status`/`diff`/`explain`, whose entire design is to degrade and show state.
It was also redundant: `render.Plan` already runs `ValidateComponentID` over every
component id at the single dispatch waist, before any id is joined into a
destination path. The lesson is the codebase's own: one guard at one waist beats
two that can drift.

**§5 said the residual collision keeps its detector in the codex adapter. It
moved to `checkProjectedConflicts`.** The codex check only fires for codex; every
other agent fell through to the render pipeline, whose message can name neither
origin (a `FileOp` carries no provenance) — and where two colliding components
with *identical* bytes were silently deduped, dropping one with no report at all.
That silent drop is the real bug; the loud one was merely unactionable.
`checkProjectedConflicts` is where the codebase already guards cross-source id
collisions for MCP/LSP, it runs with provenance in hand, and it has the
strict-vs-lenient split this needs. Codex keeps its narrower frontmatter-vs-stem
check as a backstop.

**Also broadened during review:** the capture refusal covers MCP servers, LSP
servers, and hooks (hooks per *handler*, since one canonical `hooks/<event>.toml`
holds handlers from many sources), not just the three name-keyed kinds; and the
"Known adjacent gap" below was closed rather than deferred.
