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
	Project      *Canonical // nil for user-scope canonical; populated by M5 overlay
}

// Config mirrors agentsync.toml at the root of ~/.agentsync/.
type Config struct {
	Agents  map[string]Agent `toml:"agents"`
	Updates UpdateDefaults   `toml:"updates"`
	Secrets SecretsConfig    `toml:"secrets"`
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
	Backend      string `toml:"backend"`       // "env" | "age"
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
	Type    string            `toml:"type"`              // stdio | http | sse
	Command string            `toml:"command,omitempty"`
	Args    []string          `toml:"args,omitempty"`
	URL     string            `toml:"url,omitempty"`
	Headers map[string]string `toml:"headers,omitempty"`
	Env     map[string]string `toml:"env,omitempty"`
	Agents  []string          `toml:"agents,omitempty"` // ["*"] or ["claude","opencode"]
	Enabled *bool             `toml:"enabled,omitempty"` // nil means default-on
}

// Skill mirrors skills/<name>/SKILL.md (frontmatter + body).
type Skill struct {
	Name        string         `toml:"-"` // dirname
	Frontmatter map[string]any `toml:"-"` // YAML frontmatter parsed
	Body        string         `toml:"-"` // markdown body
}

// Plugin mirrors plugins/<id>.toml.
type Plugin struct {
	ID        string                       `toml:"-"`
	Plugin    PluginSpec                   `toml:"plugin"`
	Overrides map[string]PluginOverrideSet `toml:"plugin.overrides"` // per-agent
}

type PluginSpec struct {
	ID          string   `toml:"id"`
	Version     string   `toml:"version,omitempty"`
	ManifestSHA string   `toml:"manifest_sha,omitempty"`
	Update      string   `toml:"update,omitempty"` // pinned | track | manual
	Agents      []string `toml:"agents,omitempty"`
}

// PluginOverrideSet captures per-agent component overrides for one plugin.
// e.g. [plugin.overrides.cursor] commands = "skip"
type PluginOverrideSet map[string]string // component -> action ("skip" today; future: "force", etc.)

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
	ID      string
	Spec    LSPServerSpec
}

// LSPServerSpec holds the server configuration for an LSP server.
type LSPServerSpec struct {
	Command string            `toml:"command,omitempty"`
	Args    []string          `toml:"args,omitempty"`
	Env     map[string]string `toml:"env,omitempty"`
	URL     string            `toml:"url,omitempty"`
	Headers map[string]string `toml:"headers,omitempty"`
}

// Memory mirrors memory/AGENTS.md and memory/fragments/.
type Memory struct {
	Body      string            // resolved AGENTS.md after @import expansion
	Fragments map[string]string // path -> body, keyed by repo-relative path under memory/
}
