// Package adapter declares the interface every per-agent adapter implements.
// The registry holds zero or more concrete implementations; the apply pipeline
// asks each registered adapter to Render a CanonicalModel into FileOps.
package adapter

import (
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// Capability is a bitmask of components an adapter can produce. M1's Claude
// adapter is full-spectrum; M2's OpenCode adapter omits Hook + LSP.
type Capability uint32

const (
	CapMCP Capability = 1 << iota
	CapMemory
	CapSkill
	CapSubagent
	CapCommand
	CapHook
	CapLSP
)

// Scope distinguishes user-level vs project-level apply targets.
type Scope int

const (
	ScopeUser Scope = iota
	ScopeProject
)

func (s Scope) String() string {
	switch s {
	case ScopeProject:
		return "project"
	default:
		return "user"
	}
}

// FileOp describes one destination-side change. Action is "write" or "delete".
// Path is absolute (after AGENTSYNC_TARGET_ROOT redirection).
type FileOp struct {
	Action        string // "write" | "delete"
	Path          string
	Content       []byte
	Mode          uint32
	SourceID      string   // canonical source path that produced this op
	MergeStrategy string   // "replace" (default) | "merge-json-keys"
	OwnedKeys     []string // JSON pointers owned by agentsync; populated by Apply from state, not Render
}

// Skip describes a component the adapter chose not to render. Surfaces in the
// translation report and in `apply --strict`'s exit logic.
type Skip struct {
	Component string // "skill" | "subagent" | etc.
	Name      string
	Reason    string
}

// Adapter is the per-agent contract.
type Adapter interface {
	Name() string
	Capabilities() Capability
	Detect() (bool, error)
	// Render projects the resolved canonical (secrets already substituted to
	// cleartext, or wrapped templated for a preview) into destination FileOps.
	// It accepts only secrets.Resolved — never a raw source.Canonical — so the
	// render egress is type-distinct from the dest->source write path.
	Render(r secrets.Resolved, scope Scope, project string) ([]FileOp, []Skip, error)
	Ingest(scope Scope, project string) (source.Canonical, error)
	// KeyMergeStrategy returns this adapter's single JSON-key-merge strategy
	// ("merge-json-keys" for claude, "merge-jsonc-keys" for opencode), or ""
	// if the adapter does not merge keys. The render layer needs it to
	// synthesize cleanup ops that remove now-orphaned owned keys from a
	// destination when the source section became empty (no render op exists
	// to carry the removal). It MUST be exact — applying the wrong strategy to
	// a JSONC file would parse it as strict JSON and clobber the file.
	KeyMergeStrategy() string
	// Apply executes ops against destinations. Adapters MUST route every
	// destination write through w.Write / w.Delete rather than calling
	// iox.AtomicWrite or os.Remove directly — w owns the foreign-collision
	// backup invariant. A forbidigo lint rule in .golangci.yml backs this
	// up at build time by failing direct iox.AtomicWrite calls outside the
	// allowed packages.
	Apply(ops []FileOp, w DestWriter) error
}

// NativeSource describes where a natively-registered marketplace is fetched
// from, in the agent's own vocabulary. `import` maps it onto an agentsync
// marketplace source. Fields mirror Claude's extraKnownMarketplaces `source`
// object; only the fields relevant to a given Type are populated.
type NativeSource struct {
	Type    string // "github" | "git" | "url" | "npm" | "file" | "directory" | …
	Repo    string // "owner/repo" for github
	URL     string // for git/url
	Path    string // for file/directory
	Ref     string // branch/tag/commit
	Package string // for npm
}

// NativeMarketplace is a marketplace registered in an agent's native config,
// as discovered by `import`. ID is the agent's own marketplace identifier (the
// "@<marketplace>" half of a plugin reference), which need not match the name
// declared inside the fetched marketplace.json.
type NativeMarketplace struct {
	ID     string
	Source NativeSource
}

// NativePlugin is a plugin recorded in an agent's native config, as discovered
// by `import`. Enabled is false for an explicitly-disabled entry.
type NativePlugin struct {
	Name          string // the "<plugin>" half of a plugin reference
	MarketplaceID string // the marketplace it was installed from
	Enabled       bool
}

// PluginIngester is an OPTIONAL extension to Adapter: an agent that tracks
// installed plugins + marketplaces in its native config implements it so
// `import` can capture them into the canonical source. import type-asserts for
// it; an adapter that does not implement it simply imports no plugins. It is
// kept off the core Adapter interface because only Claude has a native plugin
// concept in v1 and the canonical schema does not otherwise depend on it.
type PluginIngester interface {
	IngestPlugins(scope Scope, project string) ([]NativeMarketplace, []NativePlugin, error)
}

// DestWriter is the single funnel for any write that targets a native agent
// destination file (~/.claude.json, ~/.claude/agents/*.md, ~/.config/opencode/*,
// etc). It enforces the foreign-collision backup invariant: a pre-existing
// destination that agentsync does not yet own is copied to
// <home>/.state/backups/<ts>/<original-path> before being overwritten.
//
// Adapters MUST use DestWriter instead of calling iox.AtomicWrite or
// os.Remove directly. The forbidigo lint rule in .golangci.yml will fail
// any direct iox.AtomicWrite call outside the allowed packages, so a new
// adapter or a new write path inside an existing adapter cannot silently
// regress the backup guarantee.
type DestWriter interface {
	// Write writes finalBytes to op.Path, after backing up any pre-existing
	// foreign content. For replace ops, finalBytes is op.Content. For merge
	// ops, the adapter performs its merge first (claude → jsonkeys.MergeKeys;
	// opencode → hujson.MergeJSONC) and passes the post-merge bytes here;
	// the writer uses op.Content (ours pre-merge) plus op.OwnedKeys to
	// detect per-key collisions for the backup decision.
	Write(op FileOp, finalBytes []byte) error

	// Delete removes op.Path. No backup — agentsync only deletes paths it
	// already owns per state. Idempotent on missing files.
	Delete(op FileOp) error
}
