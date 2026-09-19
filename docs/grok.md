# Grok Build

Enable the dedicated adapter with `agentsync agent add grok`, then run
`agentsync apply`. For a project, use the existing `--scope project --project
<root>` flags on `init`, `agent add`, and `apply`.

| Component | User scope | Project scope |
|---|---|---|
| Instructions | `~/.grok/AGENTS.md` | `<root>/AGENTS.md` |
| Skills and bundled files | `~/.grok/skills/<name>/` | `<root>/.grok/skills/<name>/` |
| Slash commands | `~/.grok/commands/<name>.md` | `<root>/.grok/commands/<name>.md` |
| MCP | `~/.grok/config.toml` | `<root>/.grok/config.toml` |
| Command hooks | `~/.grok/hooks/agentsync.json` | `<root>/.grok/hooks/agentsync.json` |

An absolute `GROK_HOME` replaces `~/.grok` at user scope. Project paths stay
project-local. `AGENTSYNC_TARGET_ROOT` takes precedence over `GROK_HOME` to
keep redirected runs isolated (it is read through `paths.AgentHomeOverride`,
never a raw `os.Getenv`). Detection checks the resolved config directory or the
`grok` executable on `PATH`.

Two values are refused outright, on every command: a `GROK_HOME` of `/` and a
`GROK_HOME` that is your home directory — by *directory*, not spelling, so a
symlink to `$HOME` or a case-varied spelling on macOS is refused too. Neither
is ever a real Grok config dir (`/config.toml`, `~/AGENTS.md`, `~/skills/…`),
and either would have the apply tail's de-nesting pass fold every other agent's
directory into it and `git init` your home directory or the filesystem root,
breaking the invariant that agentsync never inits a repo at `$HOME`. The error
names `GROK_HOME` and suggests `~/.grok` (`TestGrokHomeRefusal`,
`TestGrokHomeRefusal_SymlinkedHome`, `TestGrokValidateHome_RefusalIsUniform`).
A `GROK_HOME` outside `$HOME` is accepted and versioned by destination git
backup like any other root. A `GROK_HOME` that is some *other* ancestor of
`$HOME` (`/home`, `/Users`) is accepted for rendering and capture; the apply
tail's central never-at-or-above-`$HOME` guard then drops it from git backup
and says so in a warning, and `doctor` reports it
(`TestEnabledVersionRoots_NeverAtOrAboveHome`).

## MCP and ownership

MCP uses `[mcp_servers.<name>]` tables, with `command`, `args`, and `env` for
local processes and `url` plus `headers` for remote servers. Grok's header key
is `headers`, unlike Codex's `http_headers`. Native extra fields such as
`startup_timeout_sec` survive capture and apply, including numeric types.
Disabled canonical servers and agent allowlists follow the existing conventions.

Only rendered server keys are owned. Other servers and unrelated model,
plugin, permission, and compatibility settings survive apply and cleanup.
TOML comments and formatting are not preserved by the shared TOML merger.
Removing the last MCP server or hook and `agent disable grok --purge` use the
destination's format, so TOML and JSON cleanup remain independent.

Grok does not store a separate SSE transport label in these tables. A canonical
`sse` server renders as a URL server with a reduced-coverage report and captures
as `http`. Grok expands native `${VAR}` references in MCP fields at load time;
the adapter writes values verbatim and reports fields containing `${` without
printing their values. AgentSync's own secret resolution and capture backstop
remain unchanged. OAuth credentials are never managed.

## Instructions, skills, and commands

Instructions use the usual managed banner and fragment expansion. Capture strips
the banner. Skills preserve frontmatter and their entire bundled file tree,
including binary content and executable modes. Legacy command Markdown preserves
frontmatter and body at both scopes; nested command directories are not captured
and produce a warning. Argument placeholder syntax is not rewritten.

Grok accepts some frontmatter as metadata without enforcing it. In particular,
`allowed-tools` does not grant or restrict tools; commands carrying that field
receive a reduced-coverage report. Skill frontmatter is preserved verbatim,
including fields Grok ignores. The adapter imports its listed native paths,
not compatibility-vendor directories, alternate instruction filenames, or extra
configured search roots.

## Hooks

Supported events are `SessionStart`, `SessionEnd`, `UserPromptSubmit`,
`PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PermissionDenied`, `Stop`,
`StopFailure`, `Notification`, `SubagentStart`, `SubagentStop`, `PreCompact`,
and `PostCompact`. Unsupported events or non-command handler types are reported
as skips. Consecutive handlers sharing an event and matcher keep their order
within one group.

The adapter owns only the events it renders in `hooks/agentsync.json`. Other
events in that file survive. Other `hooks/*.json` files are not imported and
produce a warning: copying their handlers into `agentsync.json` while leaving
the originals active would execute them twice. To manage an existing hook,
move it into `agentsync.json` before importing it.

Native handlers with fields the canonical model cannot represent, such as
`timeout` or an HTTP handler's `url`, are refused as a whole event with a warning.
Import retires previously captured stale events and relinquishes ownership, so
the next apply preserves the enriched native hook. Malformed handlers warn and
are not captured, but do not trigger destructive retirement.

Project hooks require Grok's own trust approval. AgentSync never writes trust
decisions or changes permissions. Hook commands and matchers are copied without
rewriting scripts: Grok's stdin event fields, environment, and blocking behavior
differ from Claude's, so scripts must support Grok's hook contract.

## Claude compatibility (duplicate execution warning)

Grok natively reads `CLAUDE.md`, `~/.claude/skills/`, and `.claude/settings.json`
hooks out of the box. Anyone running Claude Code and Grok Build on the same
machine will have instructions loaded twice, skills duplicated, and hook commands
executed twice unless Grok's built-in Claude compatibility is disabled.

When managing both agents with AgentSync, avoid duplicate execution by:
- Disabling Grok's native Claude compatibility in Grok configuration
  (`[compat.claude]` in `config.toml` or `GROK_CLAUDE_*_ENABLED=false` environment
  variables), OR
- Restricting components in AgentSync using `agents` allowlists and `native_agents`
  exclusions in canonical configuration.

## Deferred features and verification

Subagent and LSP projection are explicitly skipped. Native plugin/marketplace
discovery, general settings synchronization, managed policy, credentials, and
trust configuration are outside this adapter. Plugin components already projected
by AgentSync use the supported component paths; native plugin enable-state is
never written.

`internal/adapter/grok/grok_test.go` starts from native on-disk fixtures and
checks both scopes, bundled skill content/modes, commands, MCP extras, hook
grouping, detection, and error preservation. `internal/cli/grok_integration_test.go`
checks ownership, backups, convergence, both-format cleanup/purge, MCP import and
reconciliation with secret references, and enriched-hook retirement. These tests
exercise AgentSync, not a live Grok session.

Upstream contracts checked against the official documentation:

- [Instruction discovery](https://docs.x.ai/build/features/project-rules)
- [MCP configuration](https://docs.x.ai/build/features/mcp-servers)
- [Hooks and script contract](https://docs.x.ai/build/features/hooks)
- [Skills and frontmatter behavior](https://docs.x.ai/build/features/skills-plugins-marketplaces)
- [Native skill and legacy command locations](https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/08-skills.md)
- [GROK_HOME and settings](https://docs.x.ai/build/settings/reference)
