# agentsync

Centrally manage AI coding-agent configurations. Keep one canonical config in
`~/.agentsync/` (small TOML + markdown, committable to a dotfiles repo), and
`agentsync apply` renders it into each agent's native config: Claude Code,
Codex, Cursor, OpenCode, Gemini CLI, Continue, Windsurf, Roo Code, Cline, Grok
Build, plus a breadth tier of 20+ more.

**Docs:** [agentsync.cc](https://agentsync.cc) · **Source:** [github.com/spxrogers/agentsync](https://github.com/spxrogers/agentsync)

## Run it without installing

```bash
npx agentsync.cc --version
bunx agentsync.cc doctor
npx agentsync.cc@latest apply
```

## Install

```bash
npm install -g agentsync.cc   # or: bun add -g agentsync.cc / pnpm add -g agentsync.cc
agentsync --version
```

The npm package name is `agentsync.cc`; the command it installs is `agentsync`.

## How it works

agentsync is a single Go binary. This package is a small Node launcher; the
binary itself comes from one platform package (`agentsync.cc-<platform>-<arch>`,
for linux, darwin, and win32 on x64 and arm64) that your package manager picks
as an optional dependency. No install script runs. The binaries are the same
ones attached to the matching
[GitHub Release](https://github.com/spxrogers/agentsync/releases): they're
checked against the release's `checksums.txt` before they're packed.

If the launcher reports that the platform package is missing, you installed with
`--omit=optional` / `--no-optional`, or with a lockfile generated on a different
platform. Reinstall without them.

Other install options (Homebrew, Scoop, Chocolatey, deb/rpm, `go install`) are
listed at [agentsync.cc/getting-started/install](https://agentsync.cc/getting-started/install/).

## License

MIT
