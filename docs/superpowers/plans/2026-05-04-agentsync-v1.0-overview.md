# agentsync v1.0 — Implementation Plan Overview

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement these plans task-by-task. Each milestone has its own plan file with checkbox (`- [ ]`) steps.

**Source spec:** [`docs/superpowers/specs/2026-05-04-agentsync-design.md`](../specs/2026-05-04-agentsync-design.md)

**Goal:** Ship `agentsync` v1.0 — a Go CLI that centrally manages AI coding-agent configurations across Claude Code and OpenCode, with marketplace plugin fanout, bidirectional drift detection, project-local overlays, and age-encrypted secrets.

**Tech stack:** Go 1.22+, cobra (CLI), `pelletier/go-toml/v2` (TOML AST), `tailscale/hujson` (JSONC), `gofrs/flock` (lock), `spf13/afero` (FS test fakes), `filippo.io/age` (secrets), `go-git/v5` (marketplace fetch), `log/slog` (logging), `golangci-lint`, `goreleaser`.

---

## Milestone roadmap

| # | Milestone | Plan file | Ships |
|---|---|---|---|
| **M0** | Skeleton | [`m0-skeleton.md`](2026-05-04-agentsync-m0-skeleton.md) | Module bootstrap, paths, atomic IO, lock, adapter interface, NoopAdapter, cobra CLI exposing `init` / `agent` / `doctor` / `verify` / `apply --dry-run`. CI green. |
| **M1** | Claude adapter | [`m1-claude.md`](2026-05-04-agentsync-m1-claude.md) | First real adapter. All 7 components (MCP, memory, skill, subagent, command, hook, LSP). Per-key merge into `~/.claude/settings.json` and `~/.claude.json`. Renders + ingests round-trip. |
| **M2** | OpenCode adapter | [`m2-opencode.md`](2026-05-04-agentsync-m2-opencode.md) | Second adapter. JSONC for `opencode.json`. Skills written to shared `.claude/skills/` path. Subagent + slash-command projection (markdown↔markdown w/ frontmatter munging). Hook/LSP `✗ skip(warn)`. |
| **M3** | Drift / status / diff / reconcile | [`m3-drift.md`](2026-05-04-agentsync-m3-drift.md) | 3-way classifier, file + key levels, `status` / `diff` commands, interactive reconcile loop, bulk hotkeys, `--auto-*` flags, foreign-key reporting. |
| **M4** | Marketplaces + plugins | [`m4-marketplaces.md`](2026-05-04-agentsync-m4-marketplaces.md) | All 5 plugin sources (relative, github, url, git-subdir, npm). go-git fetch, npm tarball fetch, sparse clone for git-subdir. `strict` mode. `${CLAUDE_PLUGIN_ROOT}` resolution. Per-component projection, translation report, sha pinning, update modes. |
| **M5** | Project-local | [`m5-project.md`](2026-05-04-agentsync-m5-project.md) | `.agentsync.toml` walk-up, overlay merge, `--project` flag, project-scoped state. |
| **M6** | Secrets | [`m6-secrets.md`](2026-05-04-agentsync-m6-secrets.md) | age library integration, `${secret:foo.bar}` resolution, `secrets {edit,get,set}`. |
| **M7** | Polish + release | [`m7-polish.md`](2026-05-04-agentsync-m7-polish.md) | `explain`, `import`, `agent disable --purge`, goreleaser cross-platform, Homebrew tap, Scoop manifest, Chocolatey package, README docs (age key backup, first-apply caveats, OpenCode hook gap, Cursor user-rule limit). |

**Beyond v1.0:**
- **v1.1** — Codex adapter (TOML config, markdown→TOML subagent conversion, hook event mapping). Plan written after v1.0 ships.
- **v1.2** — Cursor adapter (JSON `mcp.json`, AGENTS.md, hooks.json with event mapping, project-scope rules). Plan written after v1.1 ships.

## Conventions used across all milestone plans

These apply uniformly; each plan refers back here rather than re-stating.

### TDD cycle

Every task that introduces behavior follows this cycle:

1. Write the failing test
2. Run it; verify it fails for the expected reason (test discovers missing function/symbol)
3. Write the minimal implementation
4. Run the test; verify it passes
5. Commit (test + impl together)

Tasks that introduce *only* types (interfaces, struct definitions, schemas) skip steps 1–4 and have a single "create file" step + commit, since there's no behavior to test in isolation. Their tests live in the next behavioral task.

### Test framework

- Stdlib `testing` package only. No `testify`, no `gomega`.
- Table-driven tests for branching logic. Each table row has a `name` field; subtests via `t.Run(tt.name, ...)`.
- Filesystem in tests: **always** `afero.NewMemMapFs()` or `t.TempDir()`. Never `os.UserHomeDir()`. Lint rule (`forbidigo`) enforces this.
- `t.Helper()` in any helper function that calls `t.Fatal`/`t.Errorf`.
- Integration tests for command-level behavior live in `internal/cli/*_test.go` and exercise the full cobra command path.

### Error wrapping

```go
return fmt.Errorf("loading source from %s: %w", path, err)
```

Stdlib only. No `pkg/errors`. `errors.Is` and `errors.As` for matching.

### Imports order

```go
import (
    // stdlib
    "fmt"
    "os"

    // third-party
    "github.com/spf13/cobra"

    // internal
    "github.com/spxrogers/agentsync/internal/paths"
)
```

Goimports / gofumpt formatting.

### Commit messages

Conventional commits with explicit scope:

- `feat(paths): resolve AGENTSYNC_HOME with target-root override`
- `test(state): cover atomic write under simulated crash`
- `fix(adapter): preserve foreign keys on settings.json merge`
- `refactor(cli): extract path helper for tests`
- `docs(readme): document age key backup`
- `ci: add forbidigo rule for os.UserHomeDir in tests`
- `chore: bump go.mod to 1.22`

Every commit ends with the AI co-authorship footer (per repo convention):

```
Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

Use HEREDOC form when the message has multiple lines (so newlines render correctly):

```bash
git commit -m "$(cat <<'EOF'
feat(paths): resolve AGENTSYNC_HOME with target-root override

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Lint rules (`.golangci.yml`)

Configured in M0 Task 0; expected to stay stable across milestones:

- govet, staticcheck, errcheck, gocritic, ineffassign, gofumpt
- `forbidigo` rules:
  - `os.UserHomeDir` in `*_test.go` files (must use `paths.HomeDir(env)` instead)
  - `time.Now()` in `internal/state/` and `internal/render/` (must accept a clock interface for testability)

### File-modification etiquette

- New file → create with the full content shown.
- Existing file modification → show the **exact** before/after for the changed region. Lines outside the change region are unaffected.
- When a task touches multiple files, list them all under **Files** at the top of the task.

### Cross-plan references

A task in M1 can rely on a helper introduced in M0 by referencing it as `(see M0 Task 4: iox.AtomicWrite)`. The helper's contract is given in the M0 task; it is treated as load-bearing API for downstream milestones. If M1 needs to extend the helper, M1 has its own task to do so explicitly.

### Milestone exit criteria

Each milestone plan has a **Done When** section at the end listing user-observable behavior the engineer can demo. CI must be green; lint clean; tests passing under `-race`. A milestone is not "done" until those checks pass.

## Dependency graph

```
M0 ──┬── M1 ──┬── M3 ── M4 ── M5 ── M7
     │        │
     └── M2 ──┘
              │
              └── M6   (M6 has no hard dependency on M3/M4 but ships after them
                        for ergonomic reasons; can be reordered if needed)
```

- M0 is foundational; everything depends on it.
- M1 and M2 can be implemented in either order after M0; both provide the adapter surface that M3 (drift), M4 (plugins), and M5 (project) use.
- M3 needs at least one adapter to exercise.
- M4 depends on M3's classifier (per-component drift).
- M5 depends on M4 (project overlays interact with plugin enablements).
- M6 (secrets) is mostly independent but expected after M3 so the `${secret:...}` resolution can be tested under realistic apply paths.
- M7 is release polish; depends on everything.

## How to use these plans

- Each plan opens with **Goal**, **Architecture**, **Tech stack**, **Files created/modified in this milestone**, then numbered tasks.
- Tasks are bite-sized (5 sub-steps; ~15–30 minutes per task). The engineer should be able to execute one task in a focused work block, commit, and move on.
- Don't skip ahead. Tasks within a milestone build on prior tasks' code. The plans assume the engineer reads sequentially within each plan file.
- If a task references code from an earlier task in the same plan, the prior code is repeated where it matters for clarity, but the engineer is expected to have written it.

## Done When (v1.0 overall)

The user can run end-to-end:

```bash
agentsync init
agentsync agent add claude
agentsync agent add opencode
agentsync mcp add github --command npx --args "-y,@modelcontextprotocol/server-github" --env GITHUB_TOKEN='${secret:github.token}'
agentsync secrets edit                     # paste token, save
agentsync marketplace add github:anthropics/claude-plugins-official
agentsync plugin install atlassian@anthropic
agentsync apply

# verify
jq '.mcpServers.github' ~/.claude/settings.json
jq '.mcp.github' ~/.config/opencode/opencode.json

# drift
claude /plugin install some-other-plugin    # outside agentsync
agentsync status                            # detects new plugin as foreign-managed
agentsync import claude:plugin:some-other-plugin
agentsync apply                             # propagates to opencode

# project-local
cd ~/code/myproject && touch .agentsync.toml
echo '[[mcp]]' >> .agentsync.toml
echo 'id = "company-api"' >> .agentsync.toml
echo 'type = "stdio"' >> .agentsync.toml
echo 'command = "npx"' >> .agentsync.toml
echo 'args = ["-y", "@company/mcp"]' >> .agentsync.toml
agentsync apply
ls .claude/settings.json                    # project-scope MCP landed
```

All this works on a clean macOS / Linux / Windows machine with only the `agentsync` binary installed (via Homebrew / Scoop / Chocolatey / native package). No Go toolchain required at runtime.
