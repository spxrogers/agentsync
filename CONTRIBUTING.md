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
AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/cli/ -run TestApply_FirstRunBacksUpForeignFile
```

### Test-harness environment signals

These are read by the test harness only, never by the `agentsync` binary, so
they are deliberately absent from the user-facing environment tables in
`README.md` and the website — all but `AGENTSYNC_TEST_IN_CONTAINER`, which
those tables keep because a user debugging a single test is told to set it. A
guard, `TestEnvOverridesDocumented`, keeps the two tables complete, in step
with the code, and free of every other signal listed here. The harness scrubs
every other `AGENTSYNC_*` variable from the environment at init; these survive.

| Signal | Read by | Purpose |
|---|---|---|
| `AGENTSYNC_TEST_IN_CONTAINER=1` | `internal/testenv` | Hermeticity signal exported by the container entrypoint; FS-touching tests refuse to run without it. |
| `AGENTSYNC_TEST_CONFIGURED_ENV=1` | `scripts/test-in-container.sh`, `test/container/entrypoint.sh`, `justfile` | Run the configured-environment leg (exported `AGENTSYNC_HOME`/`GROK_HOME`, a stub for every agent binary on `PATH`). `just test-release-configured` sets it. |
| `AGENTSYNC_TEST_AMBIENT_BIN` | `test/container/entrypoint.sh` → `internal/testenv` | Marker the entrypoint exports on the configured leg naming the stub-binary dir; `TestConfiguredLegIsLive` asserts `PATH` resolves into it. |
| `AGENTSYNC_TEST_DEBUG=1` | `scripts/test-in-container.sh`, `test/container/entrypoint.sh` | `set -x` in the runner and entrypoint. |
| `AGENTSYNC_TEST_ALLOW_NETWORK=1` | `scripts/test-in-container.sh` | Drop `--network=none` for the container run. |
| `AGENTSYNC_TEST_SKIP_LINT=1`, `AGENTSYNC_TEST_SKIP_GORELEASER=1` | `test/container/entrypoint.sh` | Skip the optional in-container lint / goreleaser gates when those tools are on `PATH`. |
| `AGENTSYNC_LIVE_PLUGIN_TEST=1` | `internal/marketplace` live tests | Opt into the network-dependent live cohort (`just test-live`). |
| `AGENTSYNC_TEST_GITIGNORE_*` | `internal/cli` tests | Re-exec plumbing for the git-ignore helper tests. |

## Lint & format

```bash
just lint           # format (gofmt -s + gofumpt) + tidy (go mod tidy) + golangci-lint
```

`just lint` is the single pre-commit entry point — it rewrites your Go sources
and `go.mod`/`go.sum` in place, then runs the linter. CI runs this exact recipe
followed by `git diff --exit-code`, so commit whatever it changes.

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

**Squash-merge hygiene.** When a PR is squash-merged, the squash commit's
message is the only record `git log` keeps of it — so it must follow the same
conventional-commit form as any other commit, and for a multi-commit PR the
squash **body must preserve the per-commit messages** (GitHub's default
"concatenate commit messages" body is fine; a title-only squash is not). The
v1.0 remediation waves 1/2/4 landed title-only while wave 3 kept its full body:
only wave 3's contents are reconstructible from the repository alone — the
others require chasing PR descriptions. Don't repeat that: if the detail lives
only in the PR body, copy it into the squash message before merging.

For anything security-sensitive, **don't** open a public PR/issue first — use the
private reporting path in [`SECURITY.md`](SECURITY.md).

## Cutting a release

Releases are an annotated `vX.Y.Z` tag on a green commit; pushing the tag fires
`.github/workflows/release.yml`, which runs GoReleaser (GitHub Release +
Homebrew tap), publishes the npm packages, and redeploys the docs site. Two
equivalent ways to trigger it:

- **From a laptop:** `just release vX.Y.Z` — validates `v`+semver, then tags and
  pushes.
- **From the GitHub UI / mobile app (no laptop):** Actions → **release** → **Run
  workflow**, enter the version. The workflow validates it, creates and pushes
  the tag at the selected ref's HEAD, and publishes in the same run.

Both paths share one validator, `scripts/release-tag.sh` (CI self-tests it via
`--self-test`), so the version rule can't drift between them.

Either way, make sure the commit you're tagging is green and the `CHANGELOG.md`
`[Unreleased]` section has been promoted to the new version first.

**If a publish fails after the tag was pushed.** CI runs GoReleaser only in
`--snapshot --skip publish` mode, so the real publish stanza (GitHub Release
upload, the Homebrew-tap push) is first exercised at release time — a broken
`.goreleaser.yaml` publish step or a missing `HOMEBREW_TAP_GITHUB_TOKEN` can fail
*after* the tag already exists. The tag-exists guard then blocks a re-run for
that version. To recover: fix the cause, delete the tag locally and on the
remote (`git push origin :vX.Y.Z`), and re-cut it — or, if the tag is fine and
only publishing failed, re-run GoReleaser against the existing tag
(`goreleaser release --clean`).

### npm (`npx agentsync.cc`)

The release's `npm` job calls `.github/workflows/npm-publish.yml`, which
downloads the new Release's archives and `checksums.txt`, and runs
`npm/build.mjs`. That script verifies each archive and publishes seven packages:
one `@spxrogers/agentsync.cc-<platform>-<arch>` package per binary, then the
`agentsync.cc` launcher. The platform packages live in the maintainer's npm
scope so nobody else can publish under their names; users only type
`agentsync.cc`.

A name@version already on the registry is skipped if its tarball is identical to
ours, so **re-running the failed job**, or dispatching **npm-publish** with the
release tag, is how you recover a failed npm step. It never re-runs GoReleaser,
and it doesn't matter which branch you dispatch from: the job checks out the
*tag* and packages with the `npm/` tooling that tag carries, on a pinned Node, so
the rebuilt tarballs are byte-identical. A tag cut before `npm/` existed falls
back to the default branch's tooling; that is how you backfill an old release.
If a registry copy *differs*, the run fails before the launcher is published,
and the error names who published it. If that isn't you, stop and investigate.
If it is, something changed between attempts (the default branch's tooling for a
backfill, or the pinned Node version). If a re-run can't fix it, cut a patch
release. A stable version older than the current `latest` goes to the
`backfill` dist-tag, so backfilling never downgrades `npx agentsync.cc`.

CI builds the packages from every goreleaser snapshot and installs them through
npx and bunx (`npm/smoke.sh`); `node --test npm/*.test.mjs` runs the unit tests.
`just ci` runs both, so it needs Node (and optionally Bun) alongside Go.

Publishing is gated on a credential, like Chocolatey. With none configured the
job builds and dry-runs the packages, then stops. To go live:

1. **Bootstrap with a token.** npm only allows trusted publishing on packages
   that already exist. Create a granular npm access token for the account that
   owns the `@spxrogers` scope, with **read and write** on **all packages**
   (the seven names don't exist yet, so they can't be selected individually)
   and **Bypass two-factor authentication** checked, since CI can't enter a
   one-time password.
   Save it as the `NPM_TOKEN` repository secret, then run **npm-publish** with
   the latest tag to create all seven packages (a tag cut before `npm/` existed
   uses the default branch's tooling automatically). Nobody else can register the
   scoped platform names, but `agentsync.cc` itself is unscoped, so publish it
   promptly.
2. **Switch to trusted publishing (no stored secret).** On npmjs.com, add a
   GitHub Actions trusted publisher to each of the seven packages, for this
   repository with workflow `release.yml` **and** again with workflow
   `npm-publish.yml`. npm matches the workflow that *started* the run, which is
   `release.yml` when the npm job is called from a release. Set the repository
   variable `NPM_TRUSTED_PUBLISHING=true`, then delete the `NPM_TOKEN` secret.
   The token takes precedence while it exists.

## Reporting bugs & requesting features

Use the [issue templates](.github/ISSUE_TEMPLATE/). Bug reports are far easier to
act on with `agentsync --version`, your OS, and the relevant snippet of your
`~/.agentsync/` config (with secrets redacted).
