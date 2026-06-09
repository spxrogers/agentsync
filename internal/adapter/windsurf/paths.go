package windsurf

import "path/filepath"

// memoryRuleFile is the agentsync-owned rule file the canonical memory body is
// projected to. Windsurf rules are plain markdown (activation is configured in
// the UI, not via frontmatter), so the body is written verbatim.
const memoryRuleFile = "agentsync.md"

// Paths resolves the destination paths for a given (scope, project, target-root).
//
// The fields are scope-asymmetric, mirroring Windsurf's own layout: MCP is the
// global ~/.codeium/windsurf/mcp_config.json (empty at project scope), while
// rules/workflows are the project's .windsurf/ dirs (empty at user scope).
type Paths struct {
	ConfigDir    string // ~/.codeium/windsurf (user) — also the Detect probe
	MCP          string // ~/.codeium/windsurf/mcp_config.json (user scope only; "" at project)
	RulesDir     string // <proj>/.windsurf/rules (project scope only; "" at user)
	WorkflowsDir string // <proj>/.windsurf/workflows (project scope only; "" at user)
}

// ResolvePaths returns the Paths for the given target root and optional project.
func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
	if projectScope && project != "" {
		ws := filepath.Join(project, ".windsurf")
		return Paths{
			ConfigDir:    ws,
			MCP:          "", // Windsurf MCP config is global-only
			RulesDir:     filepath.Join(ws, "rules"),
			WorkflowsDir: filepath.Join(ws, "workflows"),
		}
	}
	cfg := filepath.Join(targetRoot, ".codeium", "windsurf")
	return Paths{
		ConfigDir:    cfg,
		MCP:          filepath.Join(cfg, "mcp_config.json"),
		RulesDir:     "", // Windsurf global rules are app-managed, not filesystem
		WorkflowsDir: "",
	}
}
