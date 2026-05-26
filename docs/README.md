# agentsync documentation

> 🌐 **Prefer a browsable site with search?** These docs are published — expanded,
> with full-text search and rendered diagrams — at **[agentsync.cc](https://agentsync.cc)**.
> The source lives in [`../website/`](../website/); the four contract docs below
> (concepts, architecture, components, capability matrix) are mirrored there
> verbatim at build time, so this directory stays the source of truth.

Start here. The docs are layered — read top-to-bottom to go from zero to fluent,
or jump to whatever you need.

| Doc | Read it when you want to… | Audience |
|---|---|---|
| **[User guide](user-guide.md)** | Install agentsync and go from 0→100: first sync, daily loop, secrets, plugins, project config. | Users |
| **[Concepts & glossary](concepts.md)** | Understand the three-state model, drift, reconcile, and every term in one page. | Everyone |
| **[Capability matrix](capability-matrix.md)** | Know exactly what each agent supports and what's lossy or deferred. | Users · contributors |
| **[Architecture](architecture.md)** | See how the apply/capture pipelines, drift classifier, and secret invariants work. | Contributors |
| **[Component map](components.md)** | Navigate the codebase package by package. | Contributors |

**Repo-root docs:**

- **[README](../README.md)** — quickstart, install, known limits, full env-var table.
- **[CONTRIBUTING](../CONTRIBUTING.md)** — how to build, test, and submit changes.
- **[SECURITY](../SECURITY.md)** — threat model and vulnerability reporting.
- **[CLAUDE.md](../CLAUDE.md)** — project memory for AI agents working on the code.
- **[CHANGELOG](../CHANGELOG.md)** — what changed, release by release.

**Suggested reading order**

1. New user → [User guide](user-guide.md) (skim [Concepts](concepts.md) when a term is unfamiliar).
2. Evaluating fit → [Capability matrix](capability-matrix.md).
3. Contributing → [Concepts](concepts.md) → [Architecture](architecture.md) → [Component map](components.md) → [CONTRIBUTING](../CONTRIBUTING.md).

---

### Internal design history

`docs/superpowers/` (the original v1.0 design spec and milestone plans) and
`docs/decisions/` (architecture decision records) are kept for provenance. The
docs above supersede them for day-to-day use; the spec remains the authoritative
record of *why* v1.0 is shaped the way it is.
