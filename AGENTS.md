# CLAUDE.md

Project memory for Claude Code / agent sessions working on agentsync.

## What this is

agentsync is a single-machine Go CLI that centrally manages AI coding-agent
configurations (Claude Code, OpenCode, Codex, Cursor, and Gemini CLI). The
user keeps a canonical config in `~/.agentsync/` (small TOML + markdown,
committable to a dotfiles repo); `agentsync apply` renders it into each agent's
native config. It's bidirectional: native edits are detected as drift and merged
back via `reconcile`/`import`. Secrets are `${secret:…}`/`${env:…}` references
resolved at apply time from an age-encrypted vault.

**Read the docs before large changes:**
- [`docs/concepts.md`](docs/concepts.md) — the three-state model + every term.
- [`docs/architecture.md`](docs/architecture.md) — pipelines, drift classifier,
  secret invariants, package layering.
- [`docs/components.md`](docs/components.md) — package-by-package map.
- [`docs/capability-matrix.md`](docs/capability-matrix.md) — per-agent support.
- [`docs/superpowers/specs/2026-05-04-agentsync-design.md`](docs/superpowers/specs/2026-05-04-agentsync-design.md)
  — the authoritative v1.0 design. Note: a few items in its §"CLI surface" were
  aspirational and not wired in v1.0 (`apply --strict/--force/--agent` flags, an
  `agentsync skill` command) — trust the code over the spec on the CLI surface.

## This repo is agentsync-managed — change `.agentsync/`, not the rendered files

agentsync dogfoods itself: this repository's own agent configuration is managed
by agentsync ([agentsync.cc](https://agentsync.cc)). The canonical source of
truth is the project-scope **`.agentsync/`** tree at the repo root — memory
(`.agentsync/memory/AGENTS.md`), skills (`.agentsync/skills/<name>/SKILL.md`),
MCP servers, subagents, commands, hooks, and `agentsync.toml`.

The native agent files are **rendered** from that tree by `agentsync apply
--scope project`; they are build output, not sources — do not hand-edit them:

- this file — `CLAUDE.md` (claude memory) and `AGENTS.md` (codex memory) — is a
  passthrough render of `.agentsync/memory/AGENTS.md`;
- `.claude/skills/…` and `.agents/skills/…` render from `.agentsync/skills/…`.

**To change memory, skills, MCP config, or any agent setting in this repo, edit
the canonical file under `.agentsync/` and re-render with `agentsync apply
--scope project`.** A direct edit to a rendered file is overwritten on the next
apply and surfaces as drift in `agentsync status --scope project`; to fold an
edit already made in a native file back into the canonical source, capture it
with `agentsync import`/`reconcile --scope project` instead.

## Keep the docs in sync — non-negotiable

Docs are part of the contract, not an afterthought. **No commit may change an
interface, a contract, the canonical schema, the CLI surface, or load-bearing
logic and leave the docs out of date.** If you change behavior, update the docs
in the *same* commit. A reviewer should never have to wonder whether the prose
or the code is the source of truth. Treat a stale doc as a bug.

When you touch… | …also update in the same commit
--- | ---
the `Adapter` interface / `DestWriter` / render or capture contracts | `docs/architecture.md` (§3–§5), `docs/components.md`
a CLI command, subcommand, or flag | `docs/user-guide.md` command reference, `README.md` quickstart, `website/src/content/docs/reference/cli.mdx`
agent/component coverage (a `Skip` goes native, a new adapter, a new component) | `docs/capability-matrix.md` (mirrored to the website), the matrices in `README.md` + `docs/user-guide.md`
the canonical schema / `~/.agentsync/` layout | `docs/concepts.md`, `docs/architecture.md` (§2), the layout block in `docs/user-guide.md`
the secret-handling invariants | the section below, `SECURITY.md`
anything user-visible | `CHANGELOG.md` (under `[Unreleased]`)

If a change makes a sentence in those docs false, the change is not done until
the sentence is fixed.

**The docs website (`website/`).** The published site at
[agentsync.cc](https://agentsync.cc) is an Astro Starlight project. Its four
**contract pages** (concepts, architecture, components, capability matrix) are
*generated* from `docs/*.md` by `website/scripts/sync-docs.mjs` at build time, so
they can never drift — never hand-edit the generated copies (they're gitignored).
The rest of the site (getting-started, guides, recipes, reference, FAQ) is
authored prose that is the source of truth for *itself*; when you change the CLI
surface or agent coverage, update the website pages listed in the table above in
the same commit, just like the `docs/` files. See `website/README.md`.

## Models must stay faithful to their on-disk artifacts — non-negotiable

A whole class of silent bug: a canonical struct models a *subset* of the artifact
it claims to represent, and every downstream piece (loader, adapters, writer,
projection) faithfully mirrors that subset — so nothing fails a test, no
invariant breaks, no lint fires, yet data is dropped. `source.Skill` once held
only `SKILL.md` (frontmatter + body) and silently discarded the rest of the skill
*directory* (`scripts/`, `references/`, `assets/`, nested files). It survived
because the round-trip tests' oracle was the parsed model, not the filesystem —
and a round-trip over an impoverished model is trivially "lossless." **The
on-disk artifact — what the upstream spec actually defines — is the source of
truth, never the struct that parses it.** Three rules follow:

- **Fidelity tests anchor to the artifact, not the model.** For any component
  backed by a file tree or a structured file, the round-trip test MUST start from
  a *spec-complete* on-disk fixture (a skill with bundled files; an MCP server
  with an unmodeled native key; memory with `fragments/`) and assert the *on-disk
  result* survives — byte-for-byte where applicable. Asserting "the parsed model
  round-trips" proves nothing about what the model can't see.
- **Capture it or acknowledge it — never drop it silently.** If the model can't
  represent part of an artifact, the loss MUST surface: through the translation
  report, an `adapter.Skip`, a `◐` in the capability matrix, or (at minimum) an
  explicit code comment + doc note. Reflective guards (cf. `TestNewSecretFieldGuard`)
  are the gold standard. A silent drop is a bug, full stop. A *deliberate* subset
  is fine only if it is written down where a reviewer will see it.
- **Doc claims are not self-certifying.** Generation keeps the contract pages in
  sync with `docs/*.md`, but nothing proves a prose *capability* claim is true in
  code (the skills matrix asserted `scripts/`/`references/`/`assets/` support the
  code never had). When you assert a capability, point to — or add — the test
  that backs it.

## Cross-reference upstream harness docs before guessing — non-negotiable

agentsync targets third-party agent harnesses we don't control. What each one
*supports*, and *where* that support lives (config-file locations, schema keys,
default paths, capability coverage, native behavior), is defined by its upstream
docs — not by us, and not by what we remember. Before you write or change an
adapter, functionality, or documentation — or assert ANYTHING new about what a
harness supports or where its config lives — cross-reference the canonical
upstream documentation rather than guessing or extrapolating from memory.

| Harness | Canonical docs |
| --- | --- |
| Claude Code | https://code.claude.com/docs/ |
| Codex | https://developers.openai.com/codex/ |
| OpenCode | https://opencode.ai/docs/ |
| Cursor | https://cursor.com/docs |
| Gemini CLI | https://geminicli.com/docs/ |

This is a guard against *new or unverified* claims, not a tax on every edit.
Don't network-fetch these docs for routine iteration on an already-implemented,
well-understood feature — only when you're about to introduce information that
isn't already settled in the code or our own docs: a new config path, a newly
claimed capability, a behavioral assumption about the native tool. When in doubt
about something a harness does or where it keeps it, check the source above
before writing it down.

## Mental map of the code

- **`internal/source`** — the canonical model (`source.Canonical`). The TOML
  structs here *are* the schema; there is no separate IR. Loaders + `Write*` helpers.
- **`internal/adapter`** (+ `claude`, `opencode`, `noop`) — the per-agent
  `Adapter` interface. `Render` takes `secrets.Resolved` (not raw source);
  `Apply` writes only through `DestWriter`.
- **`internal/render`** — the apply pipeline (plan → classify → write → record
  state → translation report).
- **`internal/capture`** — the *single* dest→source write-back funnel.
- **`internal/secrets`** — resolve / re-reference / mask. The leak guards live here.
- **`internal/drift`** — the pure 3-way classifier (9 cases).
- **`internal/marketplace`** — fetch + project plugins into components.
- **`internal/{state,project,iox,jsonkeys,paths,log,testenv}`** — state file,
  project overlay, atomic IO + lock, JSON-pointer merge, path resolution,
  logging, test container guard.

The registered command tree (`internal/cli/root.go`): `init`, `agent`, `apply`,
`status`, `diff`, `reconcile`, `import`, `doctor`, `verify`, `mcp`, `plugin`,
`marketplace`, `update`, `secrets`, `explain`.

## Secret-handling invariants (read before touching secrets / capture / source writers)

agentsync resolves `${secret:…}` / `${env:…}` references into native agent
config at apply time, and captures native edits back into the canonical source
(`~/.agentsync/`). The dangerous bug class is **a resolved cleartext secret
being persisted into the canonical source** (often a committed dotfiles repo).
The architecture below makes that hard to do by accident. Do not weaken it.

**1. One field list.** Every secret-bearing canonical field is enumerated in
exactly one place: `walkSecretFields` in `internal/secrets/walk.go` (MCP/LSP
`Command,URL,Args,Env,Headers`; Hook `Command`; recursive `Project`).
`SubstituteCanonical`, `CollectResolved`, `UnresolvedSecretRefs`, and
`ReReferenceCanonical` all delegate to it. **Add a new secret-bearing field ONLY
there** — every operation then picks it up automatically. `TestNewSecretFieldGuard`
(reflect-based) fails if a string-shaped field is added to those structs without
being classified.

> **Deliberate exception — `MCPServerSpec.Extra` / `LSPServerSpec.Extra`.** These
> `map[string]any` passthrough maps hold unmodeled native fields verbatim and are
> intentionally NOT in `walkSecretFields`: their values are never secret-resolved
> (a `${secret:…}` there is written literally, not substituted) and never
> re-referenced. The guard does not flag them (a `map[string]any` is not
> "string-shaped"). The safety they'd otherwise lose is restored by the capture
> leak backstop, which scans `Extra` (`secrets.ResidualSecretCleartext` →
> `scanExtraResidual`) and refuses any write that would persist a live secret
> value through it. If you ever make `Extra` secret-resolving, it MUST join
> `walkSecretFields` (and the paired re-reference) like every other field.

**2. One dest→source path.** All write-backs go through `capture.Capture`
(`internal/capture`). It re-references secrets (`secrets.ReReferenceCanonical`),
preserves source-only fields the rendered dest never carries (MCP/LSP
`agents`/`enabled`), and writes via `internal/source/writer.go`. `import` and
`reconcile` write-back both call it. **Do not add a new dest→source write that
bypasses it.** After re-referencing, `capture.Capture` runs a **fail-closed
backstop** (`secrets.ResidualSecretCleartext`): re-reference matches by value and
cannot tell a *moved/rotated* secret from a deliberate literal edit, so if a live
vault secret value would still be written verbatim, or a `${secret:K}` the source
group referenced is now absent from the ingested group (rotated/edited away),
Capture **refuses the whole write** rather than persist cleartext. It errs toward
refusing; the user updates the vault or edits the canonical source directly.

**3. Resolved vs templated types.** `secrets.SubstituteCanonical` returns
`secrets.Resolved` (a wrapper, NOT assignable to `source.Canonical`); it is the
only thing adapters render from. `source.Write*` / `capture.Capture` accept only
the templated `source.Canonical`. So passing the resolved apply model directly
to a source writer is a **compile error**.

### How the leak is actually prevented (three tiers)

- **Compile-enforced (load-bearing):** can't pass a `secrets.Resolved` directly
  to `source.Write*` / `capture.Capture`. Proven by `internal/capture/leak_fixture.go`
  + `TestResolvedIsNotWritableToSource`.
- **Value-invariant (load-bearing):** `cloneForResolve` detaches the resolved
  copy (no aliasing back to the caller's templated canonical), and the walker
  only visits MCP/LSP/Hook fields — so text components (skill/subagent/command/
  memory) and `reconcile.writeBackFileItem` physically cannot carry a substituted
  secret.
- **Lint fence (defense-in-depth):** a `forbidigo` rule forbids
  `secrets.Resolved.Canonical` outside the adapter Render egress sites (one
  line-scoped `//nolint` per adapter's `Render`). Keep `iox.AtomicWrite` exclusions text-scoped so they
  never also exempt the `Canonical` rule.

### Accepted residual — WATCH OUT FOR THIS

The lint fence is a static matcher, so **interface dispatch, struct embedding,
and reflection defeat it**, and `source.Write*` is itself not fenced. A
*deliberate* two-step laundering — defeat the fence to obtain a writable
`source.Canonical`, then call a source writer (which does not re-reference) —
would leak. No innocent mistake produces this, and `capture.Capture` always
re-references, so real flows are safe. **If you ever find yourself unwrapping a
`secrets.Resolved` (via `.Canonical()` or any indirection) outside an adapter's
`Render`, stop — you almost certainly want `capture.Capture`.** Fencing the whole
`source.Write*` API was considered and declined (it fights the legitimate
templated-write path and is bypassable one level down).

See also: `internal/secrets/resolved.go`, `internal/source/writer.go` package
doc, `.golangci.yml` (forbidigo rules), and `SECURITY.md`.

## Build / test / lint

- `just build` / `just test-fast`; full gate `just test-release` (hermetic container).
- FS-touching tests refuse to run on host without `AGENTSYNC_TEST_IN_CONTAINER=1`.
- Lint/format/tidy with `just lint` — the single dev entry point. It rewrites Go
  sources (`gofmt -s` + gofumpt) and deliberately tidies `go.mod`/`go.sum`
  (`go mod tidy`) in place, then runs golangci-lint via `go run
  github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...`. The
  linter self-bootstraps (no separate install or PATH step); `go run pkg@version`
  resolves gofumpt and golangci-lint outside the main module, so *those* never
  land in `go.mod`, and golangci-lint compiles with the local Go toolchain (≥
  go.mod's **1.26.2**) so it parses our export data natively. CI runs this exact
  recipe then `git diff --exit-code`, so an uncommitted format/tidy change fails
  the build — local and CI can't drift.
  - **After running `just lint`, always re-stage any files it rewrote before
    committing.** Because `gofmt -w`, `gofumpt -w`, and `go mod tidy` mutate
    files in place, lint may silently reformat code that was already staged.
    Check with `git diff --stat` (or `git diff`) after lint runs; if anything
    changed, run `git add -u` (or `git add <file>…`) to stage the post-linted
    versions. Pushing without re-staging means CI's `git diff --exit-code` step
    will detect the same mutations and fail the lint job.
  - **Container/cloud sessions — the golangci-lint toolchain quirk (READ THIS).**
    In CI the base toolchain already matches go.mod, so `just lint` "just works"
    with no `GOTOOLCHAIN` override. In a remote/cloud container the base `go` on
    PATH is often an older **bootstrap** (e.g. go1.24.7); the go1.26.2 we
    build/test with is itself an auto-downloaded toolchain selected from go.mod.
    `golangci-lint@v2.12.2` pins `toolchain go1.25.11` in *its* module, so
    `GOTOOLCHAIN=auto` builds the linter with **1.25.11** — and a linter built
    with 1.25 then refuses our 1.26 export data with: *"the Go language version
    (go1.25) used to build golangci-lint is lower than the targeted Go version
    (1.26)."* This is NOT a code problem and NOT a reason to skip linting. Fix:
    run it (and `go mod tidy`) with the **exact** matching toolchain forced —
    `GOTOOLCHAIN=go1.26.2 go run
    github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...`.
    (`GOTOOLCHAIN=local` is the wrong lever — it drops to the bootstrap go, which
    is *below* golangci-lint's `>=1.25` floor and fails differently. Force the
    `go1.<minor>.<patch>` from go.mod's `go` directive; bump this alongside it.)

- `just test` (full unit/integration in container), `just test-e2e`,
  `just test-bdd`, `just test-live` (network, opt-in, not in the release gate).
- Go version is `go.mod`'s `go` directive (currently **1.26.2**); CI reads it via
  `go-version-file`. Bump in one place.
- **CI checks all failing within seconds with zero steps run** (run annotation:
  "The job was not started because recent account payments have failed or your
  spending limit needs to be increased") is GitHub Actions quota / spending-limit
  exhaustion, not a code or workflow problem. Call it out and move on — don't dig
  through logs or spend tokens trying to debug it. 🥲

## Code conventions

- **Stdlib testing only** — no testify/gomega. Table-driven with a `name` field
  and `t.Run`. `t.Helper()` in helpers that call `t.Fatal/Errorf`.
- **Filesystem in tests** is always `afero.NewMemMapFs()` or `t.TempDir()`, never
  `os.UserHomeDir()` — a `forbidigo` rule bans it in `_test.go` (use
  `paths.HomeDir(env)`). FS-touching tests must run in the container; call
  `testenv.RequireContainer(t)` / `MustRunInContainer()`.
- **Errors** wrap with `fmt.Errorf("doing X: %w", err)`; match with `errors.Is/As`.
  No `pkg/errors`.
- **Imports** grouped stdlib / third-party / internal; gofumpt + goimports
  formatting (applied by `just lint`).
- **`time.Now()`** in `internal/render`/`internal/state` is confined to
  informational metadata (state `AppliedAt`, backup-dir names) — it never feeds a
  content hash or the drift classifier, so it calls `time.Now().UTC()` directly
  rather than through an injected clock. Keep any new timestamp in these packages
  out of hashed/compared content for the same reason. (There is no `forbidigo`
  rule enforcing this, and `internal/cli` uses `time.Now().UTC()` freely for
  fetch/import timestamps.)
- **Commits**: conventional commits with scope, e.g.
  `feat(adapter): …`, `fix(secrets): …`, `test(drift): …`, `docs(readme): …`.

## Adding things (where the invariants bite)

- **New secret-bearing canonical field** → add it ONLY to `walkSecretFields`
  (`internal/secrets/walk.go`). Every secret operation then picks it up; the
  reflect-based `TestNewSecretFieldGuard` fails if you forget.
- **New agent** → add `internal/adapter/<name>/` implementing the `Adapter`
  interface and register it. `Render` must take `secrets.Resolved`; all writes
  go through `DestWriter`. The canonical schema does not change.
- **New `PluginIngester`** → if the agent has a native plugin concept,
  implement `IngestPlugins` (read side: native enable-state → canonical).
  **Do NOT add a Render-side counterpart.** Apply fans out the plugin's
  *components* (skills, MCP, commands, …) to the agent's native paths via
  the normal Render path. Enablement metadata
  (Claude's `enabledPlugins` / `extraKnownMarketplaces`, Codex's
  `[plugins."x@y"]`, Cursor's TBD-equivalent) is NEVER written back —
  doing so picks a fight with the agent's own `/plugin disable` UI and
  double-installs against the agent's own plugin install dir. The asymmetry
  is the invariant; see
  [`docs/architecture.md`](docs/architecture.md#pluginingester-read-only) §
  "PluginIngester (read-only)" and the doc comment on the interface itself.
- **New dest→source write** → it must go through `capture.Capture`. Do not call
  `source.Write*` directly from a write-back path (it does not re-reference
  secrets). See the "Accepted residual" note above.
- **New component field on the canonical model** → edit the structs in
  `internal/source/schema.go`; teach each adapter's `Render`/`Ingest` and the
  capability bitmask if it's a new component kind.
