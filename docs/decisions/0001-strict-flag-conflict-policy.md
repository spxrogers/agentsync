# 0001 — `strict` flag is a conflict policy on a plugin.json + entry union

- **Status:** Accepted — 2026-05-24
- **Area:** plugin projection (`internal/marketplace/projection.go`)
- **Supersedes:** the Strict-mode semantics in
  `docs/superpowers/specs/2026-05-04-agentsync-design.md` (§ "Strict mode", lines 212–217)

## Context

A marketplace plugin can be defined in two places: the plugin's own
`.claude-plugin/plugin.json`, and the `PluginEntry` in a marketplace's
`marketplace.json`. The per-entry `strict` boolean (default `true`) governs how
those two combine.

The original design spec defined it as:

> `strict: true` (default) — `plugin.json` is authoritative; marketplace entry
> supplements (both merged).
> `strict: false` — marketplace entry is the entire definition; conflicts with
> `plugin.json` are an error.

The implemented `strict: false` path (`applyEntryFull`) read **only** the entry
and ignored `plugin.json` entirely. In practice marketplace entries rarely
re-list a plugin's full component set, so this **silently dropped a plugin's own
components** — and an upstream `strict: true → false` flip dropped them on the
next load with no signal. That silent drop was a launch-blocking correctness
bug.

The first fix (Round 9) made projection an unconditional **union** of
`plugin.json` + entry, which removed the drop but left `strict` doing nothing —
an inert flag. Union also can't express the legitimate `strict: false` use case
of a curator **suppressing/replacing** an upstream component, and it forced a
silent "entry wins" on a same-name conflict.

## Decision

`strict` becomes the **conflict-resolution policy on a union**:

- Projection always **unions** `plugin.json` with the entry. A plugin's declared
  components are never silently dropped.
- A **conflict** is a same-name/-id component present in both sides with
  **differing content** (identical content always collapses to one, silently):
  - **`strict: true` (default):** the conflict is a **hard error**. agentsync
    refuses to guess which definition the operator meant.
  - **`strict: false`:** the **entry wins** (overrides), the documented lenient
    curator override.
- Hooks have no override key, so they are deduped on **exact content only**
  (distinct hooks for one event all survive) and are not subject to the policy.

Implemented in `marketplace.resolveConflicts` / `dedupOrConflict`.

## Departure from spec

This intentionally differs from the spec text on both points:

1. **Union, not entry-only, under non-strict.** `strict: false` no longer means
   "entry is the *entire* definition" — `plugin.json` is still read and unioned.
   This is the deliberate trade to guarantee a plugin's own components are never
   silently dropped. The cost: a curator can no longer suppress an upstream
   component purely by *omitting* it under `strict: false`; suppression would
   need an explicit mechanism (not yet built — see below).
2. **Which mode errors on conflict.** The spec attached "conflicts are an error"
   to `strict: false`; we attach it to `strict: true`, which matches the plain
   meaning of the word (strict ⇒ fail on ambiguity) and pairs naturally with
   `strict: false` ⇒ lenient override.

## Consequences

- The flag is meaningful again and conflicts are never resolved silently.
- A marketplace that previously relied on `strict: false` to *replace* a
  plugin's component now gets a union; an incompatible same-name component
  surfaces as an error under the default and as an entry-win under
  `strict: false`.
- **Deferred:** an interactive "override / keep" prompt at `plugin install` /
  `upgrade` that persists the operator's choice (so non-interactive `apply` /
  `status` stay deterministic) was scoped out of this change. Until it lands,
  `strict` is the resolution mechanism: a strict conflict is an actionable
  error that names both ways out (fix upstream, or set `strict: false`).
