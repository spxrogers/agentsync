# agentsync

Centrally manage AI coding-agent configurations across Claude Code, OpenCode, Codex CLI, and Cursor.

## Quickstart

    agentsync init
    agentsync agent add claude
    agentsync agent add opencode
    agentsync mcp add github --command npx --args "-y,@modelcontextprotocol/server-github"
    agentsync apply --dry-run    # preview translation report before writing
    agentsync apply

## Install

### macOS — Homebrew

    brew tap spxrogers/tap
    brew install agentsync

### Windows — Scoop

    scoop bucket add spxrogers https://github.com/spxrogers/scoop-bucket
    scoop install agentsync

### Windows — Chocolatey

    choco install agentsync

### Linux

Debian/Ubuntu:

    curl -fsSL https://github.com/spxrogers/agentsync/releases/latest/download/agentsync.deb -o agentsync.deb
    sudo dpkg -i agentsync.deb

RPM:

    sudo rpm -i https://github.com/spxrogers/agentsync/releases/latest/download/agentsync.rpm

Arch (AUR):

    yay -S agentsync

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
- **Cursor user-level rules**: Cursor stores user-level rules in app-local storage (not the filesystem). agentsync's Cursor adapter manages project-scope rules only.
- **LSP projection beyond Claude**: OpenCode/Codex/Cursor LSP support is deferred. Claude plugins that include LSP servers install correctly on Claude itself; on other agents you'll see `lsp server X skipped` in the apply translation report.
- **codex / cursor agent registration**: `agent add codex` and `agent add cursor` are rejected in v1.0 because their adapters are noops. Set `AGENTSYNC_ALLOW_UNIMPLEMENTED=1` to register anyway (apply will silently emit zero ops for them).
- **TOML / JSONC comment preservation**: comments in `~/.agentsync/mcp/*.toml` and in agent-side `opencode.json` are NOT preserved across reconcile `[w]`rite-back or import. Hand-edited comments survive in unrelated sections; the rewritten section will be re-emitted without comments. Deferred to v1.x.
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
| `AGENTSYNC_ALLOW_UNIMPLEMENTED=1` | Accept `agent add codex`/`cursor` despite the noop adapters. |
| `AGENTSYNC_ALLOW_PLUGIN_DRIFT=1` | Bypass the plugin-cache manifest-SHA check (after hand-editing). |
| `AGENTSYNC_ALLOW_OFFLINE_VERIFY=1` | Skip `${secret:…}` resolution in `agentsync verify` (CI without an age key). |
| `AGENTSYNC_AGE_SKIP_PERM_CHECK=1` | Skip the 0600 mode check on the age identity file (ACL'd NFS). |
| `AGENTSYNC_MAX_TARBALL_MB=<N>` | Override the per-tarball decompressed-bytes cap (default 512). 0 disables. |
| `AGENTSYNC_TEST_IN_CONTAINER=1` | Bypass the host test guard (use only with `go test -run` for a single case). |

## Troubleshooting

- **First apply on a populated machine**: agentsync sees pre-existing native config files and triggers `foreign-collision`. The original is backed up to `~/.agentsync/.state/backups/<ts>/` before the new content lands. Recommend `agentsync apply --dry-run` first to preview the translation report.
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
