// Package source loads and represents the canonical agentsync repo layout
// (~/.agentsync/). Structs in this file are TOML-tagged and serve as the
// canonical model — there is no separate IR layer; adapters consume these
// types directly.
package source

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
	Agents  map[string]Agent `toml:"agents"`
	Updates UpdateDefaults   `toml:"updates"`
	Secrets SecretsConfig    `toml:"secrets"`
	Memory  MemoryConfig     `toml:"memory"`
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
	Enabled bool   `toml:"enabled"`
	Scope   string `toml:"scope,omitempty"` // "user" | "project"
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
	Name        string         `toml:"-"` // dirname
	Frontmatter map[string]any `toml:"-"` // YAML frontmatter parsed
	Body        string         `toml:"-"` // markdown body
	Files       []SkillFile    `toml:"-"` // bundled files other than SKILL.md
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
	ID     string     `toml:"-"`
	Plugin PluginSpec `toml:"plugin"`
}

type PluginSpec struct {
	ID          string   `toml:"id"`
	Version     string   `toml:"version,omitempty"`
	ManifestSHA string   `toml:"manifest_sha,omitempty"`
	Update      string   `toml:"update,omitempty"` // pinned | track | manual
	Agents      []string `toml:"agents,omitempty"`
	// Disabled, when true, suppresses the plugin's projection during
	// marketplace.LoadProjected. `agentsync plugin disable <id>` sets this.
	// Without honouring it there, the CLI's TOML write would be a no-op:
	// projection would still surface the plugin's MCP servers / skills / etc.
	// into the canonical model and apply would ship them.
	Disabled bool `toml:"disabled,omitempty"`
}

// Marketplace mirrors marketplaces/<name>.toml.
type Marketplace struct {
	Name        string          `toml:"-"`
	Marketplace MarketplaceSpec `toml:"marketplace"`
}

type MarketplaceSpec struct {
	URL               string `toml:"url"`
	Ref               string `toml:"ref,omitempty"`
	DefaultUpdateMode string `toml:"default_update_mode,omitempty"`
}

// Subagent mirrors agents/<name>.md (frontmatter + body).
// Subagents in Claude live at ~/.claude/agents/<name>.md.
type Subagent struct {
	Name        string         // filename without .md extension
	Frontmatter map[string]any // YAML frontmatter (description, tools, model, color, etc.)
	Body        string         // markdown body
}

// Command mirrors commands/<name>.md (frontmatter + body).
// Slash commands in Claude live at ~/.claude/commands/<name>.md.
type Command struct {
	Name        string         // filename without .md extension
	Frontmatter map[string]any // YAML frontmatter
	Body        string         // markdown body
}

// Hook represents a single hook entry for an event.
type Hook struct {
	Event   string // e.g. "PreToolUse"
	Matcher string // glob/regex string
	Type    string // "command"
	Command string // shell command
}

// LSPServer mirrors lsp/<id>.toml.
type LSPServer struct {
	ID   string
	Spec LSPServerSpec
}

// LSPServerSpec holds the server configuration for an LSP server.
type LSPServerSpec struct {
	Command string            `toml:"command,omitempty"`
	Args    []string          `toml:"args,omitempty"`
	Env     map[string]string `toml:"env,omitempty"`
	URL     string            `toml:"url,omitempty"`
	Headers map[string]string `toml:"headers,omitempty"`
	// Agents / Enabled mirror MCPServerSpec: they are source-only targeting
	// fields that the rendered destination never carries, so capture must
	// preserve them (via source.ReadLSP) rather than reset them from the dest.
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
