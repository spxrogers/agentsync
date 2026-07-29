// Package source loads and represents the canonical agentsync repo layout
// (~/.agentsync/). Structs in this file are TOML-tagged and serve as the
// canonical model — there is no separate IR layer; adapters consume these
// types directly.
package source

import (
	"fmt"

	"github.com/spxrogers/agentsync/internal/untrusted"
)

// Canonical is the in-memory image of a fully-loaded ~/.agentsync/ tree.
type Canonical struct {
	Config       Config
	MCPServers   []MCPServer
	Skills       []Skill
	Subagents    []Subagent
	Commands     []Command
	Hooks        []Hook
	LSPServers   []LSPServer
	Plugins      []Plugin
	Marketplaces []Marketplace
	Memory       Memory
	// Project holds the project-only canonical set by project.Merge. It is nil
	// for a user-scope canonical (one loaded directly from ~/.agentsync/ without
	// a project overlay). Scope-aware render paths (apply/status/diff/reconcile
	// --scope project) render from Project instead of the merged canonical so
	// user-scope items are not duplicated into the project directory.
	Project *Canonical
}

// Config mirrors agentsync.toml at the root of ~/.agentsync/.
type Config struct {
	Agents               map[string]Agent           `toml:"agents"`
	Updates              UpdateDefaults             `toml:"updates"`
	Secrets              SecretsConfig              `toml:"secrets"`
	Memory               MemoryConfig               `toml:"memory"`
	DestinationGitBackup DestinationGitBackupConfig `toml:"destination_directory_git_backup"`
}

// DestinationGitBackupConfig mirrors the [destination_directory_git_backup] table
// in agentsync.toml. It controls whether `apply` git-versions the rendered
// destination dirs (~/.claude, ~/.codex, …) so a bad apply is revertible — a
// LOCAL-ONLY rollback history that is never pushed (issue #118; see the secret-
// handling note in CLAUDE.md). The table name is deliberately explicit: these
// repos back up the destination directories, not general git config. An empty Mode
// means "prompt".
type DestinationGitBackupConfig struct {
	Mode        string `toml:"mode,omitempty"`         // "prompt" | "on" | "off"
	AuthorName  string `toml:"author_name,omitempty"`  // commit author override
	AuthorEmail string `toml:"author_email,omitempty"` // commit email override
}

// Destination-git-backup modes.
const (
	GitBackupModePrompt = "prompt" // default: ask on first write to an untracked dir
	GitBackupModeOn     = "on"     // init + checkpoint silently
	GitBackupModeOff    = "off"    // never init/commit/prompt
)

// EffectiveMode returns Mode, defaulting to GitBackupModePrompt when unset.
func (g DestinationGitBackupConfig) EffectiveMode() string {
	if g.Mode == "" {
		return GitBackupModePrompt
	}
	return g.Mode
}

// validateMode rejects any Mode value outside the allowed set
// {"", "prompt", "on", "off"}. The empty string is accepted (EffectiveMode
// defaults it to "prompt"); every other value must match a mode constant
// case-SENSITIVELY — "On"/"true"/"yes" are invalid by design, so a typo can no
// longer silently disable backup for untracked dirs while owned repos still
// commit. Called from loadConfig after a successful decode so every source.Load
// caller (apply, doctor, …) inherits the rejection.
func (g DestinationGitBackupConfig) validateMode() error {
	switch g.Mode {
	case "", GitBackupModePrompt, GitBackupModeOn, GitBackupModeOff:
		return nil
	default:
		return fmt.Errorf("[destination_directory_git_backup] mode: invalid value %q (want %q, %q, or %q)",
			g.Mode, GitBackupModePrompt, GitBackupModeOn, GitBackupModeOff)
	}
}

// MemoryConfig mirrors the [memory] table in agentsync.toml.
type MemoryConfig struct {
	// Banner controls the agentsync "managed file" notice prepended to every
	// rendered memory file (CLAUDE.md, AGENTS.md, …) — see RenderManagedMemory.
	// It is ON by default; set `banner = false` to opt out. The field is a *bool
	// so "unset" (nil → default-on) is distinguishable from "explicitly false",
	// which the project-overlay merge needs to tell an explicit project override
	// from an inherited default.
	Banner *bool `toml:"banner,omitempty"`
}

type Agent struct {
	Enabled bool `toml:"enabled"`
	// Scope is display-only and never drives behavior: the scope of record is
	// which file the entry lives in (~/.agentsync/agentsync.toml vs a project
	// tree's). `agent add` writes "user" at user scope for continuity and omits
	// the key entirely in project files; no render/plan path reads it.
	Scope string `toml:"scope,omitempty"`
}

type UpdateDefaults struct {
	DefaultMode     string `toml:"default_mode"`     // pinned | track | manual
	DefaultInterval string `toml:"default_interval"` // e.g. "24h"
}

type SecretsConfig struct {
	Backend      string `toml:"backend"` // "env" | "age"
	File         string `toml:"file"`
	Recipient    string `toml:"recipient"`
	IdentityFile string `toml:"identity_file"`
}

// MCPServer mirrors mcp/<id>.toml.
type MCPServer struct {
	ID     string        `toml:"-"` // filename minus .toml
	Server MCPServerSpec `toml:"server"`
	// Plugin is the id of the plugin providing this server, empty when the user
	// declared it in mcp/<id>.toml. Unlike a skill/subagent/command, an MCP
	// server's ID is NOT namespaced — two sources claiming one id can be a silent
	// endpoint hijack, which checkProjectedConflicts refuses rather than renames
	// apart. Provenance is carried only so the dest→source paths (import,
	// reconcile write-back) can refuse to capture a server the plugin owns. It is
	// derived state, never serialized, and never secret-bearing — see
	// NamespacedComponentName (provenance.go) and walkerCovered
	// (internal/secrets/walk_test.go).
	Plugin string `toml:"-"`
}

type MCPServerSpec struct {
	Type    string            `toml:"type"` // stdio | http | sse
	Command string            `toml:"command,omitempty"`
	Args    []string          `toml:"args,omitempty"`
	URL     string            `toml:"url,omitempty"`
	Headers map[string]string `toml:"headers,omitempty"`
	Env     map[string]string `toml:"env,omitempty"`
	Agents  []string          `toml:"agents,omitempty"`  // ["*"] or ["claude","opencode"]
	Enabled *bool             `toml:"enabled,omitempty"` // nil means default-on
	// Extra carries native MCP-server fields agentsync does not model (e.g.
	// timeout, disabled, cwd), captured on ingest and rendered back verbatim so
	// import/reconcile/apply are not lossy for them. It is PASSTHROUGH ONLY:
	// values are never secret-resolved (a ${secret:…} here is written literally,
	// not substituted) and never visited by walkSecretFields — so it stays out of
	// the secret machinery. The capture leak backstop (secrets.ResidualSecretCleartext)
	// scans it instead, refusing a write that would persist a live secret value.
	Extra map[string]any `toml:"extra,omitempty"`
}

// Skill mirrors a skill directory skills/<name>/. Per the Agent Skills spec a
// skill is a DIRECTORY whose only required member is SKILL.md (frontmatter +
// body); it may also bundle scripts/, references/, assets/, and arbitrary
// nested files. Files captures everything in the directory other than SKILL.md
// so apply/import/reconcile are not lossy for those resources.
type Skill struct {
	Name        string         `toml:"-"` // dirname (namespaced when Plugin is set)
	Frontmatter map[string]any `toml:"-"` // YAML frontmatter parsed
	Body        string         `toml:"-"` // markdown body
	Files       []SkillFile    `toml:"-"` // bundled files other than SKILL.md

	// Plugin / BaseName carry plugin provenance; see NamespacedComponentName
	// (provenance.go) for what they mean and why they are never serialized.
	Plugin   string `toml:"-"`
	BaseName string `toml:"-"`
}

// SkillFile is one bundled resource inside a skill directory (e.g.
// scripts/extract.py, references/REFERENCE.md, assets/logo.png). Content is
// captured verbatim — never secret-substituted, never frontmatter-parsed — so
// binary assets round-trip byte-for-byte. Path is relative to the skill
// directory and slash-separated; Mode preserves the file's permission bits so
// executable scripts keep their +x bit.
type SkillFile struct {
	Path    string `toml:"-"`
	Content []byte `toml:"-"`
	Mode    uint32 `toml:"-"`
}

// Plugin mirrors plugins/<id>.toml.
//
// NOTE: a per-agent `[plugin.overrides.<agent>]` table is NOT wired in v1 — the
// projector does not consult it. It was previously a struct field that never
// parsed and was read nowhere, so it has been removed rather than left as a
// silent no-op. (Per-agent fan-out is still controllable via a component's
// `agents` allowlist.)
type Plugin struct {
	// ID is the plugin's filesystem id (the plugins/<id>.toml stem), originating
	// from a marketplace install — untrusted.Text so a print site sanitizes it by
	// construction; use Unverified() for path/lookup use.
	ID     untrusted.Text `toml:"-"`
	Plugin PluginSpec     `toml:"plugin"`
}

type PluginSpec struct {
	// ID is "<name>@<marketplace>" and Version comes from the fetched manifest;
	// both are untrusted.Text (see Plugin.ID). ManifestSHA/Update are
	// agentsync-controlled and stay plain strings.
	ID          untrusted.Text `toml:"id"`
	Version     untrusted.Text `toml:"version,omitempty"`
	ManifestSHA string         `toml:"manifest_sha,omitempty"`
	Update      string         `toml:"update,omitempty"` // pinned | track | manual
	Agents      []string       `toml:"agents,omitempty"`
	// Disabled, when true, suppresses the plugin's projection during
	// marketplace.LoadProjected. `agentsync plugin disable <id>` sets this.
	// Without honouring it there, the CLI's TOML write would be a no-op:
	// projection would still surface the plugin's MCP servers / skills / etc.
	// into the canonical model and apply would ship them.
	Disabled bool `toml:"disabled,omitempty"`
}

// Marketplace mirrors marketplaces/<name>.toml.
type Marketplace struct {
	// Name is the declared marketplace name (from marketplace.json, falling back
	// to a URL-derived slug) — untrusted.Text so a print site sanitizes it by
	// construction; use Unverified() for path/lookup use.
	Name        untrusted.Text  `toml:"-"`
	Marketplace MarketplaceSpec `toml:"marketplace"`
}

// MarketplaceSpec is the canonical model of a marketplace. It DELIBERATELY does
// NOT model the on-disk `head_sha` or persisted `name` keys the CLI writes into
// marketplaces/<name>.toml (issue #171): both are CLI fetch-CACHE metadata,
// regenerated by `marketplace add`/`update` on every fetch — `head_sha` is the
// source's git HEAD at fetch time (the user pins via `ref`, not this), and the
// marketplace name is re-derived on load into Marketplace.Name (`toml:"-"`) rather
// than round-tripped from a second persisted copy. The CLI reads/writes those
// fields through its own marketplaceTOMLSpec (internal/cli/marketplace.go); they
// are never round-tripped through this canonical model, so nothing is silently
// lost — the subset is intentional and written down here.
type MarketplaceSpec struct {
	URL               string `toml:"url"`
	Ref               string `toml:"ref,omitempty"`
	DefaultUpdateMode string `toml:"default_update_mode,omitempty"`
}

// Subagent mirrors subagents/<name>.md (frontmatter + body).
// Subagents in Claude live at ~/.claude/agents/<name>.md.
type Subagent struct {
	Name        string         // filename without .md extension (namespaced when Plugin is set)
	Frontmatter map[string]any // YAML frontmatter (description, tools, model, color, etc.)
	Body        string         // markdown body

	// Plugin / BaseName carry plugin provenance; see NamespacedComponentName
	// (provenance.go) for what they mean and why they are never serialized.
	Plugin   string
	BaseName string
}

// Command mirrors commands/<name>.md (frontmatter + body).
// Slash commands in Claude live at ~/.claude/commands/<name>.md.
type Command struct {
	Name        string         // filename without .md extension (namespaced when Plugin is set)
	Frontmatter map[string]any // YAML frontmatter
	Body        string         // markdown body

	// Plugin / BaseName carry plugin provenance; see NamespacedComponentName
	// (provenance.go) for what they mean and why they are never serialized.
	Plugin   string
	BaseName string
}

// Hook represents a single hook entry for an event.
//
// Event is untrusted.Text: it is derived from an on-disk file NAME
// (hooks/<event>.toml) — a user/marketplace-controllable origin outside
// agentsync's trust boundary — so a print site sanitizes it by construction (its
// String() runs untrusted.Sanitize). Reach the raw bytes via Unverified() only
// for non-display logic — the hooks/<event>.toml stem, a map key, an event
// comparison, TOML serialization. See internal/untrusted and SECURITY.md.
type Hook struct {
	Event   untrusted.Text // e.g. "PreToolUse"
	Matcher string         // glob/regex string
	Type    string         // "command"
	Command string         // shell command
}

// LSPServer mirrors lsp/<id>.toml.
type LSPServer struct {
	ID   string
	Spec LSPServerSpec
	// Plugin mirrors MCPServer.Plugin — provenance only, never namespaced, never
	// serialized, never secret-bearing. See MCPServer.Plugin.
	Plugin string `toml:"-"`
}

// LSPServerSpec holds the server configuration for an LSP server.
type LSPServerSpec struct {
	Command string            `toml:"command,omitempty"`
	Args    []string          `toml:"args,omitempty"`
	Env     map[string]string `toml:"env,omitempty"`
	URL     string            `toml:"url,omitempty"`
	Headers map[string]string `toml:"headers,omitempty"`
	// Agents / Enabled mirror MCPServerSpec: Agents is a source-only targeting
	// field no native dest carries, so capture always restores it (via
	// source.ReadLSP). Enabled follows the same conditional rule as MCP — capture
	// restores the source value only when the ingest carried none — so a dest that
	// ever reports LSP enable state can round-trip it; no LSP adapter reads native
	// `enabled` today (Codex's MCP dest does, #152), so in practice it is preserved.
	Agents  []string `toml:"agents,omitempty"`  // ["*"] or ["claude",...]; empty = all
	Enabled *bool    `toml:"enabled,omitempty"` // nil means default-on
	// Extra carries native LSP-server fields agentsync does not model, captured
	// on ingest and rendered back verbatim. Passthrough only — see the note on
	// MCPServerSpec.Extra (never secret-resolved or walked; the capture leak
	// backstop scans it).
	Extra map[string]any `toml:"extra,omitempty"`
}

// Memory mirrors memory/AGENTS.md and memory/fragments/.
type Memory struct {
	Body      string            // raw AGENTS.md (with @import directives); expansion happens at render via ExpandMemoryImports
	Fragments map[string]string // fragment body, keyed by slash path under memory/fragments/ (loaded recursively)
}
