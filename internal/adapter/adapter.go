// Package adapter declares the interface every per-agent adapter implements.
// The registry holds zero or more concrete implementations; the apply pipeline
// asks each registered adapter to Render a CanonicalModel into FileOps.
package adapter

import (
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
	Render(c source.Canonical, scope Scope, project string) ([]FileOp, []Skip, error)
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
