# agentsync — Design Spec

**Date:** 2026-05-04
**Status:** Design approved, implementation plan to follow.
**Supersedes:** PRs #1, #2, #3 — closed; their content was input material for this fresh spec.

---

## Summary

agentsync is a single-machine CLI that centrally manages AI coding-agent configurations across **Claude Code, OpenCode, Codex CLI, and Cursor**. The user commits canonical config (MCP servers, marketplace plugin enablements, memory snippets, secrets) once into `~/.agentsync/`, runs `agentsync apply`, and every registered agent picks it up. When an agent's config drifts from what agentsync recorded, agentsync surfaces the drift and offers chezmoi-style merge resolution per file or per key.

The product is **personal-first, OSS-shareable**: built well enough to share, not chasing breadth. v1.0 ships Claude Code + OpenCode end-to-end. v1.1 adds Codex CLI. v1.2 adds Cursor.

---

## Locked decisions

| Topic | Decision | Rationale |
|---|---|---|
| Audience | Personal-first, OSS-shareable. No breadth chasing. | User uses Claude/OpenCode/Codex; coworker uses Cursor. Continue/Gemini/Aider not on the user's stack. |
| v1 agents | Claude Code + OpenCode in v1.0. Codex in v1.1. Cursor in v1.2. | Top user pain (plugin/marketplace fanout) needs ≥ 2 agents to exercise. Claude + OpenCode are the two with native marketplace concept (or close to it) — translation gymnastics are deferred to v1.1/v1.2 where they're harder. |
| Pain ranking (v1 must address) | (1) plugin/marketplace fanout, (2) drift detection, (3) MCP fanout, (4) project-local, (5) memory dedup. | User-supplied; informs milestone ordering inside v1.0. |
| Source-of-truth model | Bidirectional. `~/.agentsync/` is canonical; agent edits are detected as drift; reconcile UX merges. | User explicitly invoked the chezmoi mental model: "if I edit `.zshrc` directly, chezmoi prompts for merge resolution." |
| Authoring surface | CLI-driven (`agentsync mcp add`, etc.) but on-disk shape is hand-vim-editable: small TOML files, AST round-trip preserves comments + key order, no `# DO NOT EDIT` markers. | User: "as the owner who might `vim` edits manually, I want something that doesn't suck." |
| Drift granularity | Key-level for structured (JSON/JSONC/TOML), file-level for everything else, file-level fallback when parse fails. | `~/.claude/settings.json` and `~/.claude.json` are shared between Claude Code, the user's hand-edits, and agentsync. File-level would false-positive constantly; only per-key writes are polite. |
| Secrets | env + age-encrypted file. `${secret:foo.bar}` resolution at apply-time. | User wants single-artifact `~/.agentsync/` (chezmoi dream). age is pure-Go, file-format-stable, cross-platform, integrates naturally with chezmoi if used. |
| Project marker | `.agentsync.toml` at repo root. Walk-up resolution from cwd. | User: "directory paths aren't durable across machines or clones; a directory should declare itself." |
| Cross-machine sync | Delegated to chezmoi or whichever dotfile manager. agentsync stays single-machine. | User confirmed; out of scope. |
| Daemon | None. `agentsync update` polls on demand. Recurring runs delegated to user's cron/launchd/systemd. | User: scheduled refresh is "nice and fluffy but not critical path." |
| Language | Go. Single static binary. | Both prior plans agreed; age library is Go-native; TOML AST round-trip via `pelletier/go-toml/v2` is mature. |
| Distribution | Brew (macOS), scoop + chocolatey (Windows), apt/yum/AUR (Linux). No `go install`, no npm, no curl-bash. | User explicit. |

---

## Architecture

agentsync inherits chezmoi's three-state model:

- **Source** — what the user committed in `~/.agentsync/` (a git repo, optionally chezmoi-managed for cross-machine).
- **Target** — the rendered output of templating + per-component projection through each adapter, computed in memory at apply time.
- **Destination** — what is actually on disk under each agent's config dirs right now.

```
+-----------------+           +----------------+           +-----------------+
| Source          |           | Target         |           | Destination     |
| ~/.agentsync/    |  render   | (in-memory)    |  write    | ~/.claude/...   |
|                 |---------->|                |---------->| ~/.config/...   |
| Hand-edited     |           | TOML/MD/JSON   |           | (per-agent dirs)|
| TOML + .md      |           | per-agent ops  |           |                 |
+-----------------+           +----------------+           +-----------------+
        ^                                                          |
        |                          import                          |
        +----------------------------------------------------------+
                            (capture drift back into source)
```

**Drift** = `hash(destination) ≠ hash(applied)` recorded in `.state/targets.json`.
**Source change** = `hash(target) ≠ hash(applied)`.
Both → conflict (user must reconcile).

### TOML structs are the canonical model

There is no separate "internal IR" translation layer. The Go structs that parse the TOML files in `~/.agentsync/` ARE the canonical model. Each adapter implements:

```go
type Adapter interface {
    Name() string
    Capabilities() Capability
    Detect(env Env) (bool, error)
    Paths(scope Scope, project string) Paths
    Render(canonical CanonicalModel, scope Scope, project string) ([]FileOp, []Skip, error)
    Ingest(scope Scope, project string) (CanonicalModel, error)   // for drift + import
    Apply(ops []FileOp) error
}
```

Adding an agent = adding `internal/adapter/<name>/` implementing this interface. The canonical schema is unchanged.

---

## Source repo layout

`~/.agentsync/` (overridable via `AGENTSYNC_HOME`):

```
~/.agentsync/
├── agentsync.toml              # global: agent registry, default update mode, secrets backend
├── mcp/<server>.toml          # one MCP server per file
├── marketplaces/<name>.toml   # one marketplace registry per file
├── plugins/<id>.toml          # one plugin enable + version pin per file
├── memory/
│   ├── AGENTS.md              # canonical memory; rendered per agent
│   └── fragments/*.md         # @-importable from above
├── skills/<name>/SKILL.md     # standalone skills (outside plugins)
├── secrets/secrets.age        # age-encrypted; agentsync secrets {edit,get,set}
├── ignore.toml                # paths to suppress from drift reporting
└── .state/                    # gitignored
    ├── targets.json           # last-applied hashes
    ├── apply.lock             # gofrs/flock
    ├── staging/               # two-phase write tmpdir
    ├── backups/<ts>/          # first-apply backups
    └── cache/
        ├── marketplaces/<slug>/
        └── plugins/<id>/
```

### Canonical schema (representative TOML)

```toml
# agentsync.toml
[agents]
claude   = { enabled = true,  scope = "user" }
opencode = { enabled = true,  scope = "user" }
codex    = { enabled = false }   # v1.1
cursor   = { enabled = false }   # v1.2

[updates]
default_mode     = "track"        # pinned | track | manual
default_interval = "24h"

[secrets]
backend       = "age"
file          = "secrets/secrets.age"
recipient     = "age1abc..."
identity_file = "${env:HOME}/.config/agentsync/age.key"
```

```toml
# mcp/github.toml
[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
agents  = ["*"]                  # or ["claude","opencode"]; empty/"*" = all enabled

[server.env]
GITHUB_TOKEN = "${secret:github.token}"
```

```toml
# marketplaces/anthropic.toml
[marketplace]
url = "https://github.com/anthropics/claude-plugins-official"
ref = "main"
default_update_mode = "track"
```

```toml
# plugins/atlassian.toml
[plugin]
id           = "atlassian@anthropic"
version      = "1.2.3"
manifest_sha = "abc123..."           # detects re-uploads of same version
update       = "track"                # overrides marketplace default
agents       = ["claude","opencode"]  # default = all enabled

[plugin.overrides.cursor]
commands = "skip"                     # don't project /commands as Cursor rules
```

```markdown
<!-- memory/AGENTS.md -->
# Personal coding conventions

@import ./fragments/style.md
@import ./fragments/security-rules.md
```

### Project marker

```toml
# <project-repo>/.agentsync.toml
agents = ["claude", "codex"]      # subset that applies in this project (default = all enabled)

[[mcp]]
id      = "company-api"
type    = "stdio"
command = "npx"
args    = ["-y", "@company/mcp"]
[mcp.env]
COMPANY_TOKEN = "${secret:company.api_token}"

[plugins]
disabled = ["screenshot"]

[memory]
import = ["./AGENTS.md"]          # project-relative, merged into rendered memory
```

Discovery: walk up from cwd looking for `.agentsync.toml`. First match wins. No central registry.

---

## Plugin model

agentsync consumes Claude marketplace plugins as the canonical plugin shape. Schema source: https://code.claude.com/docs/en/plugin-marketplaces.

### Plugin sources (5 types, all in v1.0)

| Source | Type | Fields |
|---|---|---|
| Relative path | string `./...` | within marketplace repo |
| `github` | object | `repo`, `ref?`, `sha?` |
| `url` (any git URL) | object | `url`, `ref?`, `sha?` |
| `git-subdir` (sparse clone) | object | `url`, `path`, `ref?`, `sha?` |
| `npm` | object | `package`, `version?`, `registry?` |

- **`npm` source**: agentsync fetches the tarball via the registry HTTP API and extracts. No `npm`/`Bun` required on user's machine.
- **`git-subdir`**: uses go-git partial/sparse clone where supported; shells out to `git` as fallback if go-git's sparse support is incomplete on a given version.

### Strict mode

`strict: true` (default) — `<plugin>/.claude-plugin/plugin.json` is authoritative; marketplace entry supplements (both merged).
`strict: false` — marketplace entry is the entire definition; conflicts with `plugin.json` are an error.

agentsync respects this flag exactly as Claude Code does.

### `${CLAUDE_PLUGIN_ROOT}` substitution

This variable appears in hook commands and MCP server configs (e.g. `command = "${CLAUDE_PLUGIN_ROOT}/scripts/validate.sh"`).

- For Claude itself: pass through verbatim. Claude resolves it at runtime.
- For non-Claude agents: agentsync resolves at apply-time to `~/.claude/plugins/cache/<plugin>/`. Otherwise the path would be meaningless in those agents' configs.

### Version resolution

- `version` set (in marketplace entry or `plugin.json`) → pinned.
- `version` omitted → every git commit is a new "version." `agentsync update` surfaces this with a warning so unintended drift is visible: `plugin foo: version unpinned; tracking commit a1b2c3 → d4e5f6`.

### `~/.claude/plugins/cache/` is jointly-owned

agentsync writes plugin contents it installs. Claude itself may also install plugins directly (`/plugin install foo`). Drift in this directory for plugins agentsync didn't install is reported as **foreign-managed**, not as drift. agentsync only owns rows in `.state/targets.json` it created.

### Reserved marketplace names

`claude-plugins-official`, `anthropic-marketplace`, etc. trigger a warning on `marketplace add` but don't block.

### Skill frontmatter is open-ended

Fields beyond `name` and `description` (e.g. `disable-model-invocation`) are real. agentsync's canonical `Skill` type carries `Frontmatter map[string]any` and writes through verbatim — never drop unknown keys.

---

## Per-component translation table

A plugin is a bag of components. Each component is translated independently per target agent.

- ✓ native — full fidelity, agent has the concept directly
- ◐ projected — lossy translation, explicitly reported (e.g. Claude `/command` → Cursor rule loses `/`-invocation)
- ✗ skipped — no defensible translation, logged in apply output

| Component | Claude (v1) | OpenCode (v1) | Codex (v1.1) | Cursor (v1.2) |
|---|---|---|---|---|
| MCP server | ✓ native | ✓ native (in `opencode.json`) | ✓ `[mcp_servers.X]` in `config.toml` | ✓ `mcp.json` |
| Memory | ✓ `CLAUDE.md` | ✓ `AGENTS.md` | ✓ `~/.codex/AGENTS.md` | ✓ `AGENTS.md` (Cursor reads natively) |
| Skill | ✓ `~/.claude/skills/X/SKILL.md` | ✓ same path (OpenCode reads `.claude/skills/`) | ✓ `~/.agents/skills/X/SKILL.md` | ✗ skip — no skills concept |
| Subagent | ✓ `~/.claude/agents/X.md` | ◐ projected — `.opencode/agents/X.md` w/ frontmatter munging (`tools` → `permission`, `color` drops, `mode: subagent` added) | ◐ projected — `.codex/agents/X.toml` (markdown→TOML; body→`developer_instructions`) | ✗ skip |
| Slash command | ✓ `~/.claude/commands/X.md` | ◐ projected — `.opencode/commands/X.md`; body preserved as template, frontmatter fields don't all map (`argument-hint` drops, OpenCode-specific fields not added) | ✗ skip — no custom slash commands | ◐ projected — `.cursor/rules/X.mdc` Manual rule, body=template |
| Hook | ✓ JSON in settings | ✗ skip(warn) — OpenCode hooks are JS/TS plugins (shim generation deferred) | ◐ projected — `hooks.json`, 5/9 events overlap, requires `[features] codex_hooks = true` | ◐ projected — `hooks.json`, ~6/9 events overlap (Tab hooks unique to Cursor) |
| LSP server | ✗ skip — corrected post-design: Claude Code reads LSP only from plugin manifests, not `settings.json` (see #66/#73) | ✗ skip(warn) — LSP projection deferred to v1.x | ✗ skip(warn) — no LSP concept | ✗ skip(warn) — Cursor inherits VSCode LSP but projection deferred |

**Skill-write strategy**: when a skill must reach multiple agents that read different paths, agentsync writes the same `SKILL.md` content to each path directly. **No symlinks ever.** Two file ops per skill, both tracked in state. Robustness over disk-cost.

**Translation report** at end of every `apply` and `verify`:

```
plugin: atlassian@anthropic
  claude    ✓ full   (1 mcp, 5 commands)
  opencode  ✓ full   (1 mcp, 5 commands)
  codex     ◐ partial (1 mcp; 5 commands skipped)
  cursor    ◐ partial (1 mcp; 5 commands → .cursor/rules/*.mdc)
```

Same data structured via `agentsync explain <plugin> --json`.

### Escape hatches

- `agents = [...]` allowlist on plugin entry → fan out only to those.
- `[plugin.overrides.<agent>] component = "skip"` → per-component override.
- `--strict` flag on `apply` → turns ◐/✗ into hard errors so silent drops are impossible.

### Cursor rule constraint (v1.2)

Cursor user-level rules live in Cursor's app-local storage, not the filesystem. **agentsync's Cursor adapter manages project-scope rules only.** This is a documented known limit; user-scope MCP via `~/.cursor/mcp.json` is filesystem-accessible and works normally.

---

## Drift detection — 3-way classifier

For every managed item (file or key), three hashes:

- `H_src` — computed now from canonical source.
- `H_applied` — recorded in `.state/targets.json`.
- `H_dest` — current on-disk content (or nil).

| H_applied vs H_src | H_applied vs H_dest | Class | `apply` behavior |
|---|---|---|---|
| = | = | clean | noop |
| ≠ | = | pending | write `H_src` |
| = | ≠ | drift | block; suggest reconcile |
| ≠ | ≠ (`H_dest` = `H_src`) | converged | refresh state silently |
| ≠ | ≠ (all three differ) | conflict | block; require reconcile |
| `H_applied` = nil, `H_dest` = nil | — | new | create |
| `H_applied` = nil, `H_dest` ≠ nil | — | foreign-collision | back up dest, write |
| `H_src` = nil, `H_applied` ≠ nil | `H_dest` = `H_applied` | orphan | delete |
| `H_src` = nil, `H_applied` ≠ nil | `H_dest` ≠ `H_applied` | orphan-drifted | warn; suggest re-add or explicit delete |

**Granularity**: structured files (JSON/JSONC/TOML) run at JSON-pointer granularity, each managed key path triple-tracked. **Foreign keys** (paths agentsync never wrote) are listed in `status` for awareness; they never enter the case table. When parsing fails, the algorithm degrades to file-level on the whole file.

---

## Reconcile UX

```
$ agentsync reconcile
~/.claude/settings.json#$.permissions.bash[2]   (drift)
  source:      "Bash(git push:*)"
  destination: "Bash(git push:*) Bash(npm publish:*)"

  [w]rite-back   [o]verride   [s]kip   [i]gnore   [d]iff   [q]uit

→ w
~/.claude/settings.json#$.permissions.bash updated in source.
```

Bulk hotkeys: `W` write-back-all, `O` override-all, `S` skip-all-remaining. Non-interactive flags: `--auto-writeback`, `--auto-override`, `--auto-safe` (auto-resolves only `converged` and `pending`). `i`gnore adds the path to `~/.agentsync/ignore.toml`.

---

## Update flow

Network-aware split — `update` is the only verb that hits the network:

```
agentsync update              # poll all marketplaces, refresh cache, recompute pins
                             # prints pending bumps; does NOT touch agent configs
agentsync apply               # local-only: cache → render → write, with translation report
agentsync update --apply      # convenience: update then apply, prompts on lossy translation
agentsync update --apply --auto    # same with --auto-safe semantics
```

`apply` always uses cached marketplace content — keeps tests reproducible and `apply` runs offline. Per-plugin update mode (`pinned` | `track` | `manual`) overrides marketplace default. `manifest_sha` pin detects re-uploaded versions.

Recurring runs (e.g. nightly refresh) are delegated: user wires their own cron / launchd / systemd / Task Scheduler entry calling `agentsync update --apply --auto`. agentsync ships no scheduler.

---

## Secrets

`${secret:foo.bar}` references are resolved at apply-time from `secrets/secrets.age`. age library (`filippo.io/age`) is vendored — no `age` CLI required. Public key (recipient) is committable; private key (identity) is per-machine, distributed via the user's existing machine-setup flow (1Password Secure Note, Keychain, manual).

`agentsync secrets edit` decrypts to a tmpfile, opens `$EDITOR`, re-encrypts on save. Cleartext never on durable disk outside `/tmp` (RAM-backed on macOS).

---

## CLI surface

```
# Bootstrap & inspect
agentsync init [<git-url>]
agentsync doctor
agentsync verify

# Agent registry
agentsync agent {add,remove,list,enable,disable}

# Canonical primitives (source-mutating)
agentsync mcp {add,set,remove,list,enable,disable}
agentsync skill {add,remove,list}
agentsync secrets {edit,get,set}

# Marketplaces & plugins (source-mutating)
agentsync marketplace {add,remove,list}
agentsync plugin {install,upgrade,enable,disable,remove,list}

# Network (only network-touching command)
agentsync update [--apply [--auto|--auto-safe]]

# Apply & drift (destination-mutating)
agentsync apply [--scope user|project|all] [--project <slug>] [--agent X]
               [--dry-run] [--strict] [--force]
agentsync status [--agent X]
agentsync diff [--agent X] [<path>]
agentsync reconcile [--auto-writeback|--auto-override|--auto-safe]
agentsync import <selector>          # capture native edits into source

# Per-plugin transparency
agentsync explain <plugin> [--json]
```

**Selector grammar** for `import` (and future per-item commands): `<agent>[:<component>[:<name>]]` — e.g. `claude:mcp:github`, `opencode:agent:reviewer`. Dropping the name imports every entry of that component; dropping the component too imports the agent's full native config in one pass.

---

## State on disk (`~/.agentsync/.state/`, gitignored)

```json
{
  "schema_version": 1,
  "files": {
    "<agent>:<scope>:<project>:<dest_path>": {
      "sha256": "...", "mode": 420, "applied_at": "...", "source_id": "mcp/github.toml"
    }
  },
  "keys": {
    "<agent>:<scope>:<project>:<file>:<json_pointer>": {
      "sha256": "...", "applied_at": "...", "source_id": "..."
    }
  },
  "marketplaces": {
    "anthropic": { "url": "...", "ref": "main", "head_sha": "...", "fetched_at": "..." }
  },
  "plugins": {
    "anthropic/atlassian": { "version": "1.2.3", "manifest_sha": "abc...", "enabled": true }
  }
}
```

Other contents under `.state/`: `apply.lock`, `staging/`, `backups/<ts>/`, `cache/marketplaces/<slug>/`, `cache/plugins/<id>/`.

State is schema-versioned; future bumps add migrators in `internal/state/`.

---

## Safety primitives (all v1.0)

1. **Two-phase atomic write** — `staging/<dest>` → fsync → rename to final. A crash mid-apply leaves either old or new content, never partial.
2. **File lock** — `gofrs/flock` on `.state/apply.lock` for `apply`/`reconcile`. Concurrent invocations queue.
3. **`AGENTSYNC_TARGET_ROOT` env var** — every adapter resolves dest paths through one helper. Tests redirect to `t.TempDir()` from line 1. Lint rule (`golangci-lint forbidigo`) bans `os.UserHomeDir()` in `_test.go`.
4. **First-apply backups** — `foreign-collision` case copies pre-existing dest content into `.state/backups/<ts>/<dest_path>` before writing.
5. **Manifest sha pinning** — every plugin records the marketplace-published manifest sha so re-uploaded versions are detected as drift, not silently consumed.

---

## v1 cutline

### v1.0 — Personal Bootstrap (Claude + OpenCode)

| Milestone | Scope |
|---|---|
| **M0** Skeleton | cobra root, `AGENTSYNC_HOME` / `AGENTSYNC_TARGET_ROOT` resolution, adapter interface + registry, source loader (TOML→Go structs), state manager (`targets.json` with fsync+rename), atomic write + flock, slog. |
| **M1** Claude adapter (full) | Detect/Read/Plan/Apply for the six supported components (memory, MCP, skill, subagent, command, hook; LSP was scoped here but downgraded to a documented skip post-design — Claude loads LSP only from plugin manifests, see #66/#73). `~/.claude/settings.json` and `~/.claude.json` key-level merge with `applied_keys`; foreign keys preserved. Skill frontmatter passthrough. |
| **M2** OpenCode adapter | MCP, AGENTS.md, skills (write to `.claude/skills/`), agents (`.opencode/agents/`), commands (`.opencode/commands/`). Hooks: ✗ skip(warn). LSP: ✗ skip(warn). JSONC round-trip via `tailscale/hujson`. |
| **M3** Drift / status / diff / reconcile | 3-way classifier, file + key levels, prompt loop, bulk hotkeys, `--auto-*` flags, foreign-key reporting. |
| **M4** Marketplaces + plugins | All 5 plugin sources (relative, github, url, git-subdir, npm). go-git fetch + sparse for git-subdir. npm tarball fetch via registry HTTP. `strict` mode handling. `${CLAUDE_PLUGIN_ROOT}` substitution. Per-component projection, translation report, sha pinning, update modes. |
| **M5** Project-local | `.agentsync.toml` walk-up, overlay merge, `--project` flag, project-scope state tracking. |
| **M6** Secrets | age integration, `${secret:...}` resolution, `secrets {edit,get,set}`. |
| **M7** Polish | `doctor`, `verify`, `explain`, `import`, `agent disable --purge`, goreleaser + brew + scoop + chocolatey + (apt/yum/AUR). README documenting age-key backup, first-apply caveats, OpenCode hook gap, Cursor user-rule limit. |

### v1.1 — Codex CLI

- TOML round-trip in `config.toml` (per-key, like Claude `settings.json`).
- All components: MCP, memory (`AGENTS.md`), skills (`.agents/skills/`), agents (markdown→TOML conversion), hooks (`hooks.json` JSON→JSON with event-mapping + `[features] codex_hooks = true` flag).
- Slash commands ✗ skip. LSP ✗ skip.

### v1.2 — Cursor

- `mcp.json` JSON merge, `AGENTS.md` write, `hooks.json` (event-mapping, ~6/9 events).
- Slash commands → Manual rules in `.cursor/rules/`.
- Skills/subagents ✗ skip. LSP ✗ skip.
- Project-scope rules only (user-rules are non-filesystem).

### Explicitly deferred past v1.2

- OpenCode hook plugin-shim generator (would emit JS/TS that subscribes to events).
- Cross-machine sync (delegated to chezmoi).
- Cursor user-level rules (no filesystem path exists).
- Permissions canonical projection (each agent has its own model; design v2 schema once friction is known).
- LSP projection beyond Claude.
- Continue, Gemini CLI, Aider — not on roadmap.

---

## Testing strategy

- **Unit tests** per package, table-driven where appropriate.
- **Golden tests** per adapter under `testdata/<adapter>/<case>/{source,expected,state_in.json,state_out.json}` over `afero.MemMapFs`. `make update-golden` re-records.
- **Round-trip parity** per adapter, per component: `Ingest(Apply(canonical)) == canonical`.
- **Drift case fixtures**: 9-case classifier exercised at file and key granularity for at least one structured format.
- **Reconcile loop tests** with scripted prompts via a `teatest`-style harness. Verify each hotkey path (`w`, `o`, `s`, `i`, `d`, `q`, `W`, `O`, `S`).
- **End-to-end shell tests** per adapter: full lifecycle (`init → mcp add → apply → mutate → status → reconcile → apply → clean`). One per adapter, plus one cross-adapter "MCP added once lands in all enabled."
- **Concurrent-apply lock test** — spawn two `apply` invocations against the same `AGENTSYNC_TARGET_ROOT`, assert serialization.
- **Marketplace fetch** via `file://` fake bare repo (no network in CI).
- **Comment-preservation fuzz** for TOML and JSONC round-trips: thousands of "parse → mutate one key → write → re-parse" iterations with comment + key-order assertions.
- **CI**: `go test -race ./...`; `golangci-lint` (govet, staticcheck, errcheck, gocritic, forbidigo banning `os.UserHomeDir()` in `_test.go`); `goreleaser release --snapshot --skip publish` on linux/darwin/windows × amd64/arm64.

---

## Open risks

1. **Claude marketplace schema evolves.** Schema is published. Mitigation: model as versioned Go structs; weekly CI doc-diff against committed reference snapshot flags new fields rather than silently dropping them.
2. **Each adapter's surface evolves** (this brainstorm caught two outdated plans). Mitigation: translation table is one grep-able file; each adapter has a version-stamped staleness comment that tests assert against.
3. **OpenCode hooks → Claude hooks deferred.** OpenCode hooks are JS/TS plugin event subscriptions; translation requires shim generation. Hand-author shim if needed in v1; documented limit.
4. **Cursor user-level rules** can't be agentsync-managed. Project-scope only; documented limit.
5. **age private key loss = total secrets loss.** README must call out key-backup discipline; agentsync doesn't manage backup.
6. **First apply on a populated machine** hits many `foreign-collision` cases. Mitigation: backups + recommend `apply --dry-run` first.
7. **Concurrent agent edits mid-reconcile** — drift classifier catches on next `status`; no special-case logic; documented.
8. **Two competing "official" implementations** of the same logical service (e.g. Slack hosted vs npm). Canonical plugin IDs include upstream owner: `slack@slackapi` vs `slack@modelcontextprotocol`. Tooling enforces uniqueness on `(id, owner)`.

---

## Out of scope (v1.x)

- Cross-machine config sync (delegated to chezmoi).
- Cursor user-rule filesystem management (no path exists).
- OpenCode hook shim auto-generation (hand-author for v1).
- LSP projection beyond Claude.
- Permissions canonical projection (each agent has its own model).
- Continue, Gemini CLI, Aider adapters (not on user's stack).
- Any daemon process.
