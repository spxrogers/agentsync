# CLAUDE.md

Project memory for Claude Code / agent sessions working on agentsync.

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

**2. One dest→source path.** All write-backs go through `capture.Capture`
(`internal/capture`). It re-references secrets (`secrets.ReReferenceCanonical`),
preserves source-only fields the rendered dest never carries (MCP/LSP
`agents`/`enabled`), and writes via `internal/source/writer.go`. `import` and
`reconcile` write-back both call it. **Do not add a new dest→source write that
bypasses it.**

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
  `secrets.Resolved.Canonical` outside the two adapter Render egress sites
  (line-scoped `//nolint`). Keep `iox.AtomicWrite` exclusions text-scoped so they
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
- `golangci-lint` 2.5.0 panics on Go ≥ 1.26 host toolchains; run lint as
  `GOTOOLCHAIN=go1.25.10 golangci-lint run ./...` until the linter is upgraded.
