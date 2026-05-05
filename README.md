# agentsync

Centrally manage AI coding-agent configurations across Claude Code, OpenCode, Codex CLI, and Cursor.

## Quickstart

    agentsync init
    agentsync agent add claude
    agentsync agent add opencode
    agentsync mcp add github --command npx --args "-y,@modelcontextprotocol/server-github"
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

## Known limits in v1.x

- **OpenCode hooks**: OpenCode hooks are JS/TS plugins, not declarative shell commands. agentsync v1 does NOT auto-translate Claude hooks to OpenCode. Hand-author a small JS/TS plugin if you need a hook on OpenCode.
- **Cursor user-level rules**: Cursor stores user-level rules in app-local storage (not the filesystem). agentsync's Cursor adapter manages project-scope rules only.
- **LSP projection beyond Claude**: OpenCode/Codex/Cursor LSP support is deferred. Claude plugins that include LSP servers install correctly on Claude itself; on other agents you'll see `lsp server X skipped` in the apply translation report.
- **Continue, Gemini CLI, Aider**: not on the v1.x roadmap.

## Troubleshooting

- **First apply on a populated machine**: agentsync sees pre-existing native config files and triggers `foreign-collision`. The original is backed up to `~/.agentsync/.state/backups/<ts>/` before the new content lands. Recommend `agentsync apply --dry-run` first to preview the translation report.
- **`agentsync update` fails to fetch a marketplace**: verify the marketplace URL with `git ls-remote`. agentsync uses `go-git` and falls back to system `git` for sparse clones if needed.
- **`${secret:foo}` not resolving**: run `agentsync secrets get foo` to verify the key exists in the decrypted file. age library errors will surface here.

## Testing

Three layers, each meant for a different question.

| Layer                                      | Question it answers                          | Command              |
| ------------------------------------------ | -------------------------------------------- | -------------------- |
| Unit + integration (`internal/*/*_test.go`) | Did I break an internal contract?            | `just test`          |
| Lifecycle e2e (`test/e2e`, build tag `e2e`) | Does the binary survive the v1 happy path?   | `just test-e2e`      |
| BDD Gherkin lock (`test/bdd`, tag `bdd`)    | Are the spec's north-star behaviours intact? | `just test-bdd`      |
| Hermetic container suite                   | Can I safely cut a release right now?        | `just test-container` |

The container runner picks **podman** first, then docker. The repo is
mounted read-only, the network is off by default, and every Scenario runs
against a fresh tmpdir, so the suite cannot touch your real `~/.claude.json`,
`~/.config/opencode/`, or `~/.agentsync/`.

If `just test-container` is green, ship.

## License

MIT.
