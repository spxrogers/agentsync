# Contributing to agentsync

Thanks for your interest! agentsync is **personal-first, OSS-shareable**: built
well enough to share, not chasing breadth. Pull requests are welcome, but there's
no support SLA — be patient, and prefer small, focused changes.

If you're new to the codebase, read [`docs/concepts.md`](docs/concepts.md) then
[`docs/architecture.md`](docs/architecture.md) first. The
[component map](docs/components.md) tells you where things live.

## Prerequisites

- **Go** — the version in [`go.mod`](go.mod)'s `go` directive (currently 1.26.2).
- **[`just`](https://github.com/casey/just)** — the task runner. `just` with no
  args lists every recipe.
- **podman** (preferred) or **docker** — the test suite runs in a hermetic
  container (see below). Only the pure-unit and live cohorts run on the host.
- **golangci-lint v2.12.2** — match CI exactly. Its release binary is built with
  Go 1.26 so it can parse this module's export data; an older local build will
  refuse to run.

## Build

```bash
just build          # → ./bin/agentsync
```

## Test

Every `just test*` recipe runs **inside a hermetic container** (podman first,
docker fallback) — except the two explicit on-host opt-ins below. The repo is
mounted read-only, the network is off, and each test's `HOME` is a fresh tmpdir,
so the suite can never touch your real `~/.claude.json`, `~/.config/opencode/`,
or `~/.agentsync/`.

| Recipe | What it runs |
|---|---|
| `just test-fast` | Pure-unit packages on the host (no container, no FS). Fast iteration. |
| `just test` | Unit + integration in the container. |
| `just test-e2e` | Lifecycle end-to-end (build tag `e2e`). |
| `just test-bdd` | Gherkin behaviour lock (build tag `bdd`). |
| **`just test-release`** | **All layers in one container run. This is the bar — if it's green, the change is shippable.** |
| `just test-live` | Network-dependent live cohort (build tag `live`); opt-in, **not** part of the release gate. |

FS-touching tests refuse to run on the host. To run a single one outside the
container during debugging:

```bash
AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/cli/ -run TestApply_FirstRun
```

## Lint & format

```bash
just lint           # golangci-lint run ./...
just fmt            # gofmt -s + gofumpt
just tidy           # go mod tidy
```

Test conventions enforced by lint (don't fight them):

- Stdlib `testing` only — no testify/gomega. Table-driven with a `name` field.
- Filesystem in tests is `afero.NewMemMapFs()` or `t.TempDir()` — never
  `os.UserHomeDir()` (a `forbidigo` rule bans it in `_test.go`; use
  `paths.HomeDir(env)`).
- `time.Now()` is banned in `internal/state` and `internal/render` — inject a
  clock for testability.

## Security-critical invariants

Before touching `internal/secrets`, `internal/capture`, or any `source.Write*`
path, read the **Secret-handling invariants** section of
[`CLAUDE.md`](CLAUDE.md) and [`SECURITY.md`](SECURITY.md). The core rule: a
*resolved cleartext secret* must never be written back into the canonical source.
The type system, a value invariant, and a lint fence all defend this — don't
weaken them. New write-backs go through `capture.Capture`; new secret-bearing
fields go only in `walkSecretFields`.

## Commit messages

Conventional commits with an explicit scope:

```
feat(adapter): project OpenCode subagent frontmatter
fix(secrets): re-reference env vars on write-back
test(drift): cover orphan-drifted at key granularity
docs(readme): document age key backup
```

Keep commits focused and self-contained; include tests with the behaviour they
cover.

## Pull requests

1. Branch from `main`.
2. Make the change; add or update tests.
3. Ensure **`just test-release`** is green and `just lint` is clean.
4. Open the PR and fill in the template (what changed, why, test plan).

For anything security-sensitive, **don't** open a public PR/issue first — use the
private reporting path in [`SECURITY.md`](SECURITY.md).

## Reporting bugs & requesting features

Use the [issue templates](.github/ISSUE_TEMPLATE/). Bug reports are far easier to
act on with `agentsync --version`, your OS, and the relevant snippet of your
`~/.agentsync/` config (with secrets redacted).
