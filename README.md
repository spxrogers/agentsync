<div align="center">

# agentsync

**One source of truth for every AI coding agent on your machine.**

Define your MCP servers, memory, skills, and marketplace plugins once in
`~/.agentsync/`. Run `agentsync apply`. They land — correctly translated — in
Claude Code, OpenCode, and Codex CLI (with Cursor planned).

[Quickstart](#quickstart) · [Install](#install) · **[Docs site → agentsync.cc](https://agentsync.cc)** · [User guide](docs/user-guide.md) · [Known limits](#known-limits-in-v1x)

</div>

> **Status: beta.** v1.0 ships Claude Code + OpenCode end-to-end. The tool is
> functional and tested under `just test-release`; package-manager distribution
> and a few documented trade-offs are still being finalized.

---

## What it does

If you use more than one AI coding agent, you keep re-entering the same config in
each one's native format, you forget to install a plugin everywhere, and your
configs quietly drift apart. agentsync fixes the fan-out:

- **Edit once, apply everywhere.** Add an MCP server or install a plugin a single
  time; it's projected into each agent's native config — fully where the agent
  has the concept, lossily-but-reported where it doesn't, skipped (never
  silently) where it can't.
- **Bidirectional, chezmoi-style.** When an agent edits its own config, agentsync
  detects the drift and offers a merge: adopt the edit into your source, or
  re-impose the source. Nothing is overwritten behind your back.
- **Secrets stay secret.** Reference `${secret:github.token}`; agentsync resolves
  it at apply time from an age-encrypted vault and **never** writes the cleartext
  back into your (committable) source.

New here? The **[User guide](docs/user-guide.md)** takes you 0→100.

## Quickstart

    agentsync init
    agentsync agent add claude
    agentsync agent add opencode
    agentsync mcp add github --command npx --args "-y,@modelcontextprotocol/server-github"
    agentsync apply --dry-run    # preview translation report before writing
    agentsync apply

## Documentation

The full docs are published with search and rendered diagrams at
**[agentsync.cc](https://agentsync.cc)** (source in [`website/`](website/)). The
canonical markdown also lives in [`docs/`](docs/):

| Doc | What it covers |
| --- | --- |
| **[User guide](docs/user-guide.md)** | Install → first sync → daily loop → secrets → plugins → project config. |
| [Concepts & glossary](docs/concepts.md) | The three-state model, drift, reconcile — every term in one page. |
| [Capability matrix](docs/capability-matrix.md) | Exactly what each agent supports, and what's lossy or deferred. |
| [Architecture](docs/architecture.md) | The apply/capture pipelines, drift classifier, and secret invariants. |
| [Component map](docs/components.md) | The codebase, package by package. |
| [CONTRIBUTING](CONTRIBUTING.md) · [SECURITY](SECURITY.md) · [CHANGELOG](CHANGELOG.md) | Contributing, threat model, release history. |

## Supported agents at a glance

| Agent | Status | Component coverage |
| --- | --- | --- |
| **Claude Code** | ✓ full adapter | All seven components, incl. LSP. |
| **OpenCode** | ✓ adapter | MCP, memory, skills, subagents, commands. Hooks + LSP skipped. |
| **Codex CLI** | ✓ adapter | MCP (TOML `config.toml`), memory, skills, subagents (◐), slash commands (◐, global-only), hooks (◐) + plugin import. No LSP concept. |
| **Cursor** | planned | No-op today; will project skills + subagents (`.cursor/skills/`, `.cursor/agents/`) and project-scope rules. |

Full ✓/◐/✗ breakdown per component: **[capability matrix](docs/capability-matrix.md)**.

## Install

> **Pre-release:** the package-manager channels below are wired in
> `.goreleaser.yaml` but are published starting with the first tagged release.
> Until then, **build from source**:
>
>     go install github.com/spxrogers/agentsync/cmd/agentsync@latest
>
> or clone and `go build ./cmd/agentsync`.

### macOS — Homebrew

    brew tap spxrogers/tap
    brew install agentsync

### Windows — Scoop

    scoop bucket add spxrogers https://github.com/spxrogers/scoop-bucket
    scoop install agentsync

### Windows — Chocolatey

    choco install agentsync

### Linux

Pick the package for your architecture (`amd64` or `arm64`).

Debian/Ubuntu:

    curl -fsSL https://github.com/spxrogers/agentsync/releases/latest/download/agentsync_linux_amd64.deb -o agentsync.deb
    sudo dpkg -i agentsync.deb

RPM:

    sudo rpm -i https://github.com/spxrogers/agentsync/releases/latest/download/agentsync_linux_amd64.rpm

Arch (AUR):

    yay -S agentsync-bin

## Cross-machine sync

agentsync is single-machine. To sync `~/.agentsync/` across machines, use chezmoi (or any dotfile manager):

    chezmoi add ~/.agentsync

## Secrets — age key backup

If you lose your age private key, you lose access to all encrypted secrets. Recommended: store the key in a 1Password Secure Note or your machine-setup repo. agentsync does not back up the key for you.

`agentsync secrets set` accepts the value three ways:

    agentsync secrets set github.token --stdin    # value from stdin (best for scripts / 1Password CLI)
    agentsync secrets set github.token            # prompt with echo off
    agentsync secrets set github.token=ghp_…      # back-compat; warns — argv is visible to ps(1) and shell history

`agentsync diff` redacts every resolved `${secret:…}` value before printing, so a piped diff doesn't leak credentials to logs.

## Known limits in v1.x

- **OpenCode hooks**: OpenCode hooks are JS/TS plugins, not declarative shell commands. agentsync v1 does NOT auto-translate Claude hooks to OpenCode. Hand-author a small JS/TS plugin if you need a hook on OpenCode.
- **Codex projections are lossy where Codex differs**: subagents project to Codex's TOML agent format (the `tools`/`color` frontmatter has no target and is dropped, reported in the apply report); slash commands map to Codex *custom prompts* which are global-only, so a **project-scope** command is skipped; hooks mirror Claude's declarative hook schema as inline `[hooks.*]` tables in `~/.codex/config.toml` but only for the events Codex recognizes (Claude's `SessionEnd`/`Notification` drop). All of these are surfaced in the translation report — nothing is dropped silently.
- **Cursor adapter**: not implemented in v1 — registers as a no-op adapter (planned), and `agent add cursor` is rejected unless `AGENTSYNC_ALLOW_UNIMPLEMENTED=1`. Cursor's planned coverage includes skills and subagents (which it stores on the filesystem under `.cursor/skills/` and `.cursor/agents/`), but its user-level *rules* live in app-local storage (not the filesystem), so the adapter will manage rules at project scope only.
- **LSP projection beyond Claude**: OpenCode/Cursor LSP support is deferred (Codex has no LSP concept at all). Claude plugins that include LSP servers install correctly on Claude itself; on other agents you'll see `lsp server X skipped` in the apply translation report.
- **cursor agent registration**: `agent add cursor` is rejected in v1.0 because its adapter is a noop. Set `AGENTSYNC_ALLOW_UNIMPLEMENTED=1` to register anyway (apply will silently emit zero ops for it).
- **TOML / JSONC comment preservation**: comments in `~/.agentsync/mcp/*.toml`, in agent-side `opencode.json`, and in Codex's `~/.codex/config.toml` are NOT preserved across reconcile `[w]`rite-back or import / apply. Hand-edited comments survive in unrelated sections; the rewritten section will be re-emitted without comments. Deferred to v1.x.
- **Hand-edits to agentsync-owned keys** in shared agent files (e.g. an MCP server entry in `~/.claude.json` that agentsync owns): the next `apply` overwrites them with NO foreign-collision backup, because agentsync considers them its own. Use `agentsync reconcile` (the drift classifier catches the edit and offers `[w]`rite-back) BEFORE the next apply if you want to keep them.
- **Plain-http / git:// plugin sources** are rejected by default to prevent MITM swap. Set `AGENTSYNC_ALLOW_INSECURE_URLS=1` for internal mirrors.
- **Symlinked destinations** (e.g. `~/.claude.json` is a chezmoi symlink into your dotfiles repo) are rejected by default — a rename onto the path would replace the symlink with a regular file and strand your linked source. Set `AGENTSYNC_ALLOW_SYMLINK_DEST=1` to write through the symlink instead (the underlying file is updated in place; the link survives).
- **Continue, Gemini CLI, Aider**: not on the v1.x roadmap.

## Environment overrides

| Env var | Purpose |
| --- | --- |
| `AGENTSYNC_HOME` | Override `~/.agentsync/` location (absolute path). |
| `AGENTSYNC_TARGET_ROOT` | Redirect `$HOME` for testing (used by the hermetic test container). |
| `AGENTSYNC_ALLOW_SYMLINK_DEST=1` | Permit writes to symlinked destination files (resolves the link first). |
| `AGENTSYNC_ALLOW_INSECURE_URLS=1` | Accept http:// and git:// plugin / marketplace sources. |
| `AGENTSYNC_ALLOW_UNIMPLEMENTED=1` | Accept `agent add cursor` despite the noop adapter. |
| `AGENTSYNC_ALLOW_PLUGIN_DRIFT=1` | Bypass the plugin-cache manifest-SHA check (after hand-editing). |
| `AGENTSYNC_ALLOW_OFFLINE_VERIFY=1` | Skip `${secret:…}` resolution in `agentsync verify` (CI without an age key). |
| `AGENTSYNC_AGE_SKIP_PERM_CHECK=1` | Skip the 0600 mode check on the age identity file (ACL'd NFS). |
| `AGENTSYNC_MAX_TARBALL_MB=<N>` | Override the per-tarball decompressed-bytes cap (default 512). 0 disables. |
| `AGENTSYNC_TEST_IN_CONTAINER=1` | Bypass the host test guard (use only with `go test -run` for a single case). |

## Troubleshooting

- **First apply on a populated machine**: agentsync sees pre-existing native config files and triggers `foreign-collision`. The original is backed up to `~/.agentsync/.state/backups/<ts>/` before the new content lands. Recommend `agentsync apply --dry-run` first to preview the translation report.
- **One-time backup churn after upgrading**: state keys are now stored `${HOME}`-relative (portable across machines) instead of with absolute paths. If you ran a pre-portability build, the first `apply` after upgrading will not recognize the old absolute-path entries, so every managed destination is treated as a `foreign-collision` once: the current content is backed up to `~/.agentsync/.state/backups/<ts>/` and then re-owned. This is expected, non-destructive (nothing is lost — it's backed up), and self-heals after that single apply. Run `agentsync apply --dry-run` first if you want to see which files will be backed up.
- **`agentsync update` fails to fetch a marketplace**: verify the marketplace URL with `git ls-remote`. agentsync uses `go-git` and falls back to system `git` for sparse clones if needed.
- **`${secret:foo}` not resolving**: run `agentsync secrets get foo` to verify the key exists in the decrypted file. age library errors will surface here.

## Testing

Every `just test*` recipe runs **inside a hermetic container** (podman first,
docker fallback) — except the two explicit on-host opt-ins (`test-fast` and
`test-live`) called out below. The repo is mounted read-only, the network
is off, and every test's `HOME` is a fresh tmpdir — the suite cannot touch
your real `~/.claude.json`, `~/.config/opencode/`, or `~/.agentsync/`.

| Layer                                       | Question it answers                          | Command             |
| ------------------------------------------- | -------------------------------------------- | ------------------- |
| Unit + integration (`internal/*/*_test.go`) | Did I break an internal contract?            | `just test`         |
| Lifecycle e2e (`test/e2e`, build tag `e2e`) | Does the binary survive the v1 happy path?   | `just test-e2e`     |
| BDD Gherkin lock (`test/bdd`, tag `bdd`)    | Are the spec's north-star behaviours intact? | `just test-bdd`     |
| **All layers in one container run**         | Can I safely cut a release right now?        | `just test-release` |

If `just test-release` is green, ship.

For fast in-place iteration without spinning up the container, `just test-fast`
runs the unit/integration layer directly on the host. The existing tests
already redirect `HOME` via `AGENTSYNC_TARGET_ROOT`, so they are still safe;
the container is the release gate.

If you reach for the obvious `go test ./...`, the filesystem-touching
packages refuse to run on the host and print a long banner pointing you
at the recipes above. To bypass the guard manually (e.g. for `go test -run`
on a single test), set `AGENTSYNC_TEST_IN_CONTAINER=1`:

    AGENTSYNC_TEST_IN_CONTAINER=1 go test ./internal/cli/ -run TestApply_FirstRun

`just test-live` runs the **live cohort** (build tag `live`) — currently the
`obra/superpowers` projection check, which clones the real upstream plugin
to verify agentsync's projector keeps working as the upstream evolves. It
runs on host because it needs network access; it is opt-in and **NOT** part
of `test-release`, so the release gate stays hermetic and offline.

## License

MIT.
