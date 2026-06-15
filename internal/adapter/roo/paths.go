package roo

import "path/filepath"

// memoryRuleFile is the agentsync-owned rule file the canonical memory body is
// projected to. Roo reads .roo/rules/ recursively as plain markdown, so the body
// is written verbatim (no frontmatter).
const memoryRuleFile = "agentsync.md"

// Paths resolves the destination paths for a given (scope, project, target-root).
// Roo uses the same `.roo/` layout at user scope (~/.roo) and project scope
// (<repo>/.roo). MCP is project-only: Roo's global MCP lives in VS Code's
// globalStorage (intentionally not targeted), so MCP is empty at user scope.
type Paths struct {
	ConfigDir   string // ~/.roo (or <proj>/.roo) — also the Detect probe
	MCP         string // <proj>/.roo/mcp.json (project scope only; "" at user)
	RulesDir    string // .roo/rules (both scopes)
	CommandsDir string // .roo/commands (both scopes)
}

// ResolvePaths returns the Paths for the given target root and optional project.
func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
	if projectScope && project != "" {
		base := filepath.Join(project, ".roo")
		return Paths{
			ConfigDir:   base,
			MCP:         filepath.Join(base, "mcp.json"),
			RulesDir:    filepath.Join(base, "rules"),
			CommandsDir: filepath.Join(base, "commands"),
		}
	}
	base := filepath.Join(targetRoot, ".roo")
	return Paths{
		ConfigDir:   base,
		MCP:         "", // global MCP is VS Code globalStorage — not targeted
		RulesDir:    filepath.Join(base, "rules"),
		CommandsDir: filepath.Join(base, "commands"),
	}
}
