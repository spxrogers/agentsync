<div align="center">

# agentsync

**One source of truth for every AI coding agent on your machine.**

Define your MCP servers, memory, skills, and marketplace plugins once in
`~/.agentsync/`. Run `agentsync apply`. They land — correctly translated — in
**31 agents**: nine deep adapters (Claude Code, OpenCode, Codex CLI, Cursor,
Gemini CLI, Continue, Windsurf, Roo Code, Cline) plus a breadth tier of 22 more
(amp, goose, qwen, warp, zed, kiro, junie, factory, copilot, crush, …).

[Quickstart](#quickstart) · [Install](#install) · **[Docs site → agentsync.cc](https://agentsync.cc)** · [User guide](docs/user-guide.md) · [Known limits](#known-limits)

</div>

> **Status: beta (v0.1.0).** Ships 31 agents — nine deep adapters (Claude, OpenCode, Codex, Cursor, Gemini, Continue, Windsurf, Roo, Cline) + a 22-agent breadth tier — end-to-end.
> The tool is functional and tested under `just test-release`; the canonical
> layout, CLI surface, and state schema are stabilizing toward `1.0.0` and may
> still change. A few documented trade-offs remain (see [Known limits](#known-limits)).

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
    agentsync apply --dry-run    # preview what would change vs. is already synced
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

The nine **deep adapters** (rich, agent-specific, often bidirectional):

| Agent | Status | Component coverage |
| --- | --- | --- |
| **Claude Code** | ✓ full adapter | All seven components, incl. LSP. |
| **OpenCode** | ✓ adapter | MCP, memory, skills, subagents, commands. Hooks + LSP skipped. |
| **Codex CLI** | ✓ adapter | MCP (TOML `config.toml`), memory, skills, subagents (◐), slash commands (◐, global-only), hooks (◐) + plugin import. No LSP concept. |
| **Cursor** | ✓ adapter | MCP (`.cursor/mcp.json`), memory (◐, project-scope `AGENTS.md`), skills (`.cursor/skills/`), subagents (◐), slash commands (◐), hooks (◐, `.cursor/hooks.json`). No LSP concept. |
| **Gemini CLI** | ✓ adapter | MCP + hooks (◐) in `.gemini/settings.json`, memory (`GEMINI.md`), subagents (◐), slash commands (◐, TOML). No skills (uses extensions) or LSP concept. |
| **Continue** | ✓ adapter | MCP (`.continue/mcpServers/`), memory (`.continue/rules/`), slash commands (◐, `.continue/prompts/`) — projected as Continue blocks. No skills/subagents/hooks/LSP concept. |
| **Windsurf** | ✓ adapter | MCP (`~/.codeium/windsurf/mcp_config.json`, user scope), memory (◐, `.windsurf/rules/`, project scope), slash commands (◐, `.windsurf/workflows/`, project scope). No skills/subagents/hooks/LSP concept. |
| **Roo Code** | ✓ adapter | MCP (`.roo/mcp.json`, project scope), memory (`.roo/rules/`) + slash commands (◐, `.roo/commands/`) at both scopes. No skills/subagents/hooks/LSP concept. |
| **Cline** | ✓ adapter | MCP (`~/.cline/mcp.json` CLI, user scope), memory (◐, `.clinerules/`) + slash commands (◐, `.clinerules/workflows/`) at project scope. No skills/subagents/hooks/LSP concept. |

Plus a **breadth tier** of 22 more via one data-driven generic adapter — **memory**
for all, **MCP** where the agent reads a JSON server-map (15 of 22), and **Agent
Skills** where the agent natively scans a `SKILL.md` directory (18 of 22): `amp`,
`goose`, `qwen`, `warp`, `jules`, `junie`, `openhands`, `amazonq`, `zed`,
`kilocode`, `kiro`, `trae`, `jetbrains`, `firebase`, `antigravity`, `augmentcode`,
`copilot`, `copilot-cli`, `crush`, `factory`, `pi`, `mistral`. Each is a *verified*
spec (paths cross-referenced against upstream docs + prior art), flowing through the
same drift/secrets/capture pipeline as the deep adapters. See the
[capability matrix → Breadth tier](docs/capability-matrix.md#breadth-tier).

Full ✓/◐/✗ breakdown per component: **[capability matrix](docs/capability-matrix.md)**.

## Install

### macOS — Homebrew

    brew tap spxrogers/tap
    brew install agentsync

### Linux — deb / rpm

Pick the package for your architecture (`amd64` or `arm64`).

Debian/Ubuntu:

    curl -fsSL https://github.com/spxrogers/agentsync/releases/latest/download/agentsync_linux_amd64.deb -o agentsync.deb
    sudo dpkg -i agentsync.deb

RPM:

    sudo rpm -i https://github.com/spxrogers/agentsync/releases/latest/download/agentsync_linux_amd64.rpm

### Any platform — prebuilt binary

Download the archive for your OS/arch from the
[latest release](https://github.com/spxrogers/agentsync/releases/latest)
(`linux` / `darwin` / `windows` × `amd64` / `arm64`), extract it, and put the
`agentsync` binary on your `PATH`. This is the install path for Windows until
Scoop/Chocolatey land.

### From source

    go install github.com/spxrogers/agentsync/cmd/agentsync@latest

or clone and `go build ./cmd/agentsync`.

### Coming soon

Scoop (Windows), Chocolatey (Windows), and AUR (Arch) packaging is wired in
`.goreleaser.yaml` but not published yet — tracked in
[issue #13](https://github.com/spxrogers/agentsync/issues/13). Until then, use the
prebuilt binary or `go install` above.

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

## Known limits

- **OpenCode hooks**: OpenCode hooks are JS/TS plugins, not declarative shell commands. agentsync does NOT auto-translate Claude hooks to OpenCode. Hand-author a small JS/TS plugin if you need a hook on OpenCode.
- **Codex projections are lossy where Codex differs**: subagents project to Codex's TOML agent format (the `tools`/`color` frontmatter has no target and is dropped, reported in the apply report); slash commands map to Codex *custom prompts* which are global-only, so a **project-scope** command is skipped; hooks mirror Claude's declarative hook schema as inline `[hooks.*]` tables in `~/.codex/config.toml` but only for the events Codex recognizes (Claude's `SessionEnd`/`Notification` drop). All of these are surfaced in the translation report — nothing is dropped silently.
- **Cursor projections are lossy where Cursor differs**: MCP and skills are full-fidelity (`.cursor/mcp.json` shares Claude's `mcpServers` shape; skills share the `SKILL.md` directory). Memory projects to the repo-root `AGENTS.md` at **project scope only** — Cursor keeps user-level rules in app-local storage, so user-scope memory is reported as a skip. Subagents (`.cursor/agents/`) drop Claude's `tools`/`color` (no Cursor field); slash commands (`.cursor/commands/`) are plain markdown so command frontmatter drops; hooks (`.cursor/hooks.json`) remap Claude's events to Cursor's camelCase names (events with no Cursor equivalent drop) and always carry the required top-level `version`. All losses are surfaced in the translation report.
- **Gemini CLI projections are lossy where Gemini differs**: MCP and memory are full-fidelity (`.gemini/settings.json` `mcpServers` with Gemini's `url`/`httpUrl` transport split; `GEMINI.md`). Subagents (`.gemini/agents/`) drop Claude's `tools` (Gemini's tool vocabulary differs) and `color`; slash commands become TOML (`.gemini/commands/*.toml`, `description` + `prompt`) so `argument-hint`/`allowed-tools` drop and Claude's `$ARGUMENTS` placeholder isn't auto-translated to Gemini's `{{args}}`; hooks live in `settings.json` (same nested shape as Claude) with events remapped (`PreToolUse`→`BeforeTool`, …) and unmapped events dropped. Skills have no Gemini concept (it uses extensions) and are skipped. All losses are surfaced in the translation report.
- **Continue projections are lossy where Continue differs**: Continue composes "blocks" (one file per item under `.continue/`). MCP is full-fidelity (`.continue/mcpServers/<id>.yaml`, with remote auth headers under `requestOptions.headers`); memory lands as a frontmatter-less always-apply rule (`.continue/rules/agentsync.md`, byte-clean). Slash commands become prompt blocks (`.continue/prompts/*.md`) so `argument-hint`/`allowed-tools` drop. Continue has no Agent Skills, no per-file subagents (its "agents" are top-level assistants), no declarative hooks, and no LSP config, so those are skipped. All losses are surfaced in the translation report.
- **Windsurf MCP is scope-asymmetric**: Windsurf's MCP config is global-only (`~/.codeium/windsurf/mcp_config.json`), so MCP renders at **user scope** (skipped + reported at project scope). Memory and slash commands render at **both** scopes: project memory → `.windsurf/rules/agentsync.md` (with the documented `trigger: always_on` activation frontmatter; stripped on import), user memory → the global `~/.codeium/windsurf/memories/global_rules.md` (always-on, 6k-char limit enforced by Windsurf); commands → `.windsurf/workflows/` (project) / `~/.codeium/windsurf/global_workflows/` (user), plain markdown so command frontmatter drops. All losses are surfaced in the translation report.
- **Roo Code is project-MCP-only**: Roo's clean MCP file is project-level (`.roo/mcp.json`); its *global* MCP lives in VS Code globalStorage (OS/editor-specific), which agentsync does not write (matching every other config-sync tool) — so user-scope Roo MCP is reported as a skip. Memory (`.roo/rules/`) and commands (`.roo/commands/`, which keep `description` + `argument-hint`) work at both scopes.
- **Cline targets the CLI's MCP file**: Cline has no project MCP file and its VS Code-extension MCP lives in OS/editor-specific globalStorage (which no config-sync tool writes), so agentsync targets the Cline CLI's clean `~/.cline/mcp.json` at user scope (project-scope MCP reported as a skip). Memory (`.clinerules/`) + workflows (`.clinerules/workflows/`) render at project scope (Cline's global rules are a non-XDG `~/Documents/Cline/` path agentsync does not target).
- **LSP projection beyond Claude**: OpenCode LSP support is deferred (Codex, Cursor, Gemini, Continue, Windsurf, Roo, and Cline have no LSP concept at all). Claude plugins that include LSP servers install correctly on Claude itself; on other agents you'll see `lsp server X skipped` in the apply translation report.
- **TOML / JSONC comment preservation**: comments in `~/.agentsync/mcp/*.toml`, in agent-side `opencode.json`, in Gemini's `.gemini/settings.json`, in the breadth tier's JSONC settings files (Zed's `settings.json`, Amp's `settings.json`, Copilot's `.vscode/mcp.json`), and in Codex's `~/.codex/config.toml` are NOT preserved across reconcile `[w]`rite-back or import / apply. For TOML, hand-edited comments survive in unrelated sections; for the JSONC files the whole file is re-emitted as plain JSON on the first agentsync write (foreign keys and values are preserved; the original is backed up). For Zed this is your main editor settings file — expect comments to be stripped and keys re-sorted on the first apply. Deferred to a later release.
- **Hand-edits to agentsync-owned keys** in shared agent files (e.g. an MCP server entry in `~/.claude.json` that agentsync owns): the next `apply` overwrites them with NO foreign-collision backup, because agentsync considers them its own. Use `agentsync reconcile` (the drift classifier catches the edit and offers `[w]`rite-back) BEFORE the next apply if you want to keep them.
- **Plain-http / git:// plugin sources** are rejected by default to prevent MITM swap. Set `AGENTSYNC_ALLOW_INSECURE_URLS=1` for internal mirrors.
- **Symlinked destinations** (e.g. `~/.claude.json` is a chezmoi symlink into your dotfiles repo) are rejected by default — a rename onto the path would replace the symlink with a regular file and strand your linked source. Set `AGENTSYNC_ALLOW_SYMLINK_DEST=1` to write through the symlink instead (the underlying file is updated in place; the link survives).
- **Aider** and **Firebender**: deliberately deferred — no faithful generic projection (Aider has no MCP and only an `.aider.conf.yml` `read:` pointer for memory; Firebender's config is unverified).

## Environment overrides

| Env var | Purpose |
| --- | --- |
| `AGENTSYNC_HOME` | Override `~/.agentsync/` location (absolute path). |
| `AGENTSYNC_TARGET_ROOT` | Redirect `$HOME` for testing (used by the hermetic test container). |
| `AGENTSYNC_ALLOW_SYMLINK_DEST=1` | Permit writes to symlinked destination files (resolves the link first). |
| `AGENTSYNC_ALLOW_INSECURE_URLS=1` | Accept http:// and git:// plugin / marketplace sources. |
| `AGENTSYNC_ALLOW_UNIMPLEMENTED=1` | Register an agent that has no implemented adapter yet (none today — every valid agent is real). |
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
| Lifecycle e2e (`test/e2e`, build tag `e2e`) | Does the binary survive the happy path?      | `just test-e2e`     |
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
