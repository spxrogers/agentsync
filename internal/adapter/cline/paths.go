package cline

import "path/filepath"

// memoryRuleFile is the agentsync-owned rule file the canonical memory body is
// projected to. Cline concatenates `.clinerules/` markdown files (plain — no
// frontmatter), so the body is written verbatim.
const memoryRuleFile = "agentsync.md"

// Paths resolves the destination paths for a given (scope, project, target-root).
//
// Scope-asymmetric (mirrors Cline's layout): MCP is the CLI's global
// ~/.cline/mcp.json (empty at project scope — Cline has no project MCP file),
// while rules/workflows live in the project's .clinerules/ tree (empty at user
// scope — Cline's global rules are a non-XDG app path agentsync does not target).
type Paths struct {
	ConfigDir    string // ~/.cline (user) — also the Detect probe
	MCP          string // ~/.cline/mcp.json (user scope only; "" at project)
	RulesDir     string // <proj>/.clinerules (project scope only; "" at user)
	WorkflowsDir string // <proj>/.clinerules/workflows (project scope only; "" at user)
}

// ResolvePaths returns the Paths for the given target root and optional project.
func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
	if projectScope && project != "" {
		rules := filepath.Join(project, ".clinerules")
		return Paths{
			ConfigDir:    rules,
			MCP:          "", // Cline has no project-level MCP file
			RulesDir:     rules,
			WorkflowsDir: filepath.Join(rules, "workflows"),
		}
	}
	cfg := filepath.Join(targetRoot, ".cline")
	return Paths{
		ConfigDir:    cfg,
		MCP:          filepath.Join(cfg, "mcp.json"),
		RulesDir:     "", // Cline global rules are ~/Documents/Cline (not targeted)
		WorkflowsDir: "",
	}
}
