# BDD Test Suite

This directory holds the **authoritative behaviour lock** for agentsync. The
suite is written in [Gherkin](https://cucumber.io/docs/gherkin/) and
executed by [godog](https://github.com/cucumber/godog) — every Scenario
exercises the real `agentsync` binary against a hermetic tmpdir.

## Why BDD here?

The unit tests under `internal/*` lock implementation details. The Gherkin
features here lock **observable behaviour** — what an engineer or operator
sees when they run a command. If a refactor breaks behaviour, this suite
fails; if a refactor only changes internals, this suite stays green.

The features map 1:1 to the design spec's "north stars":

| Feature file                          | North star locked                                        |
| ------------------------------------- | -------------------------------------------------------- |
| `01_bootstrap.feature`                | `init` / `doctor` / `verify` / `--version`               |
| `02_agents.feature`                   | agent registry CRUD + `disable --purge`                  |
| `03_mcp_fanout.feature`               | one MCP file fans out to every enabled agent             |
| `04_drift.feature`                    | 3-way drift classifier + reconcile + diff                |
| `05_secrets.feature`                  | `${secret:foo.bar}` resolved at apply, never in source   |
| `06_marketplace_plugins.feature`      | local marketplace + plugin install + cross-agent fanout  |
| `07_safety.feature`                   | `AGENTSYNC_TARGET_ROOT` redirect, apply.lock concurrency |
| `08_release_smoke.feature`            | top-level CLI surface + lifecycle smoke                  |

## Running

```bash
# Run the BDD suite alone (still hermetic — runs in container).
just test-bdd

# Full release gate: every layer (vet → build → race → e2e → bdd → smoke)
# in one hermetic container run.
just test-release
```

Both invocations route through `scripts/test-in-container.sh` (podman first,
docker fallback). The repo is mounted read-only and the network is off;
the suite cannot touch your real `~/.claude.json`, `~/.config/opencode/`,
or `~/.agentsync/`.

The build tag `bdd` keeps this suite out of `go test ./...`, so the fast
in-place iteration loop (`just test-fast` or plain `go test`) stays under
two seconds.

## Adding a scenario

1. Pick the right feature file (or create a new one matching a north star).
2. Write the Scenario in plain English, using existing step phrases where
   possible. Reuse keeps the step library small and the Scenarios skimmable.
3. If you need a new phrase, add a step definition in
   `support/steps.go`. Step phrases use Go regular expressions; the
   convention here is `^…$` for full-string matches and `"([^"]+)"` for
   string captures.

## Hermeticity

Every Scenario runs against a fresh tmpdir; both `HOME` and
`AGENTSYNC_TARGET_ROOT` point there, so the binary cannot reach the
engineer's real `~/.claude.json` or `~/.config/opencode/`. The
`no files exist outside of HOME` step is the explicit assertion of that
property and runs at the end of the safety scenario.

The container runner (`scripts/test-in-container.sh`) doubles down by
mounting the repo read-only and running with `--network=none` by default,
so a misbehaving test can't damage the working tree or reach the network.
