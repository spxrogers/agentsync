// Package generic implements a data-driven "breadth-tier" adapter: one Go
// implementation that supports many agents from a table of Specs, rather than a
// hand-written package per agent. It is for the long tail of agents whose
// agentsync surface is just a rules/memory file plus (optionally) an `mcpServers`
// JSON — the same shape the deep adapters (Windsurf/Cline/Continue) already take.
//
// The DEEP adapters (claude, codex, cursor, gemini, opencode, continuedev,
// windsurf, roo, cline) are NOT generic — they have richer, agent-specific
// component support (skills, subagents, commands, hooks, …) and bidirectional
// nuances that don't fit a table. The generic tier deliberately covers ONLY
// memory + MCP, and reports every other component as a skip, so its coverage is
// never overstated. Breadth-tier agents still flow through agentsync's normal
// apply/import pipeline, so they inherit drift detection, secret resolution, and
// capture — which a one-way "rules dump" (ruler/rulesync) does not provide.
//
// Each agent is one verified Spec (see specs.go); adding an agent is a data entry,
// not a package. Every path in a Spec MUST be verified against the agent's
// upstream docs (and corroborated by prior-art tools) before being added — the
// count stays honest because each row reflects the agent's real coverage.
package generic

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// FileTarget is a per-scope path for a single file component (relative to the
// scope root). An empty path means the component is unsupported at that scope.
type FileTarget struct {
	User    string // path under the user home (targetRoot)
	Project string // path under the project root
}

// MCPTarget describes an agent's MCP servers JSON file and its on-disk dialect.
// The breadth tier only handles JSON `<rootKey>` server-map files; agents whose
// MCP is TOML, app-storage, or otherwise unmodeled simply leave User/Project
// empty (MCP unsupported) and are reported as a skip.
//
// The dialect knobs capture the variance the prior-art research surfaced across
// the tail (all with Claude-compatible defaults):
//   - RootKey: the top-level object key. Default "mcpServers"; e.g. "servers"
//     (VS Code Copilot), "mcp" (Crush).
//   - TransportKey: the per-server field naming the transport. "" = inferred from
//     command-vs-url (no field). Else e.g. "type" or "transport".
//   - StdioValue / RemoteValue: the TransportKey values for stdio vs remote
//     (defaults "stdio"/"http"; Copilot CLI uses "local" for stdio). Ignored when
//     TransportKey == "".
//   - RemoteURLKey: the per-server field holding a remote server's URL. Default
//     "url"; some agents use "httpUrl" (Qwen) or "serverUrl" (Antigravity).
type MCPTarget struct {
	User         string
	Project      string
	RootKey      string
	TransportKey string
	StdioValue   string
	RemoteValue  string
	RemoteURLKey string
}

// supported reports whether the spec declares an MCP file at any scope.
func (t MCPTarget) supported() bool { return t.User != "" || t.Project != "" }

// rootKey returns the configured top-level key, defaulting to "mcpServers".
func (t MCPTarget) rootKey() string {
	if t.RootKey != "" {
		return t.RootKey
	}
	return "mcpServers"
}

// stdioValue / remoteValue return the transport-field values, with defaults.
func (t MCPTarget) stdioValue() string {
	if t.StdioValue != "" {
		return t.StdioValue
	}
	return "stdio"
}

func (t MCPTarget) remoteValue() string {
	if t.RemoteValue != "" {
		return t.RemoteValue
	}
	return "http"
}

// remoteURLKey returns the per-server URL field, defaulting to "url".
func (t MCPTarget) remoteURLKey() string {
	if t.RemoteURLKey != "" {
		return t.RemoteURLKey
	}
	return "url"
}

// Spec is the verified, data-driven description of one breadth-tier agent.
type Spec struct {
	Name      string     // agent name (Adapter.Name())
	DetectBin string     // binary on PATH (best-effort); "" to skip
	DetectDir string     // dir under targetRoot whose existence implies installed; "" to skip
	Memory    FileTarget // rules/instructions file (plain markdown)
	MCP       MCPTarget  // mcpServers JSON
}

// Options configure a generic adapter instance.
type Options struct {
	TargetRoot string
	LookPath   func(file string) (string, error)
}

// Adapter implements adapter.Adapter for one Spec.
type Adapter struct {
	spec Spec
	opts Options
}

// New constructs a generic adapter for the given Spec.
func New(spec Spec, opts Options) *Adapter { return &Adapter{spec: spec, opts: opts} }

func (a *Adapter) Name() string { return a.spec.Name }

func (a *Adapter) Capabilities() adapter.Capability {
	var c adapter.Capability
	if a.spec.Memory.User != "" || a.spec.Memory.Project != "" {
		c |= adapter.CapMemory
	}
	if a.spec.MCP.supported() {
		c |= adapter.CapMCP
	}
	return c
}

// KeyMergeStrategy is merge-json-keys when the spec has an MCP file (the only
// key-merge surface); otherwise "" (memory is a whole-file write).
func (a *Adapter) KeyMergeStrategy() string {
	if a.spec.MCP.supported() {
		return "merge-json-keys"
	}
	return ""
}

func (a *Adapter) Detect() (bool, error) {
	if a.spec.DetectDir != "" {
		if _, err := os.Stat(filepath.Join(a.opts.TargetRoot, a.spec.DetectDir)); err == nil {
			return true, nil
		}
	}
	if a.spec.DetectBin != "" {
		lookPath := a.opts.LookPath
		if lookPath == nil {
			lookPath = exec.LookPath
		}
		if _, err := lookPath(a.spec.DetectBin); err == nil {
			return true, nil
		}
	}
	return false, nil
}

// memoryPath / mcpPath resolve the absolute destination for the given scope, or
// "" when the spec does not support that component at that scope.
func (a *Adapter) memoryPath(scope adapter.Scope, project string) string {
	if scope == adapter.ScopeProject {
		if a.spec.Memory.Project == "" {
			return ""
		}
		return filepath.Join(project, a.spec.Memory.Project)
	}
	if a.spec.Memory.User == "" {
		return ""
	}
	return filepath.Join(a.opts.TargetRoot, a.spec.Memory.User)
}

func (a *Adapter) mcpPath(scope adapter.Scope, project string) string {
	if !a.spec.MCP.supported() {
		return ""
	}
	if scope == adapter.ScopeProject {
		if a.spec.MCP.Project == "" {
			return ""
		}
		return filepath.Join(project, a.spec.MCP.Project)
	}
	if a.spec.MCP.User == "" {
		return ""
	}
	return filepath.Join(a.opts.TargetRoot, a.spec.MCP.User)
}

// agentTargeted reports whether the agents allowlist includes this agent.
func agentTargeted(name string, agents []string) bool {
	if len(agents) == 0 {
		return true
	}
	for _, a := range agents {
		if a == "*" || a == name {
			return true
		}
	}
	return false
}
