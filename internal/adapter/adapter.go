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
	Action   string // "write" | "delete"
	Path     string
	Content  []byte
	Mode     uint32
	SourceID string // canonical source path that produced this op
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
	Apply(ops []FileOp) error
}
