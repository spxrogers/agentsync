package windsurf

import "path/filepath"

// memoryRuleFile is the agentsync-owned rule file the canonical memory body is
// projected to at project scope. Workspace rules declare their activation mode
// in YAML frontmatter (`trigger: always_on` — per docs.windsurf.com → docs.devin.ai,
// Cascade memories), which render emits and ingest strips, keeping the canonical
// body byte-clean.
const memoryRuleFile = "agentsync.md"

// Paths resolves the destination paths for a given (scope, project, target-root).
//
// The fields are scope-asymmetric, mirroring Windsurf's own layout: MCP and the
// single global rules file are global (`~/.codeium/windsurf/`; empty at project
// scope), workspace rules are the project's `.windsurf/rules/` (empty at user
// scope), and workflows exist at BOTH scopes (project `.windsurf/workflows/`,
// global `~/.codeium/windsurf/global_workflows/`).
type Paths struct {
	ConfigDir   string // ~/.codeium/windsurf (user) — also the Detect probe
	MCP         string // ~/.codeium/windsurf/mcp_config.json (user scope only; "" at project)
	GlobalRules string // ~/.codeium/windsurf/memories/global_rules.md (user scope only; "" at project)
	// RulesDir is the project workspace-rules directory. Upstream rebranded
	// Windsurf → Devin Desktop (docs.windsurf.com 307-redirects to docs.devin.ai)
	// and now documents `.devin/rules/*.md` as the PREFERRED path that "takes
	// precedence", with `.windsurf/rules/*.md` kept as the still-honored legacy
	// fallback (verified against docs.devin.ai). agentsync targets the legacy
	// `.windsurf/rules/`, which every released version still reads — nothing is
	// broken. (Workflows were NOT rebranded: upstream docs still use
	// `.windsurf/workflows/`, so WorkflowsDir stays as-is.) Preferring
	// `.devin/rules/` for write+capture is a possible future enhancement.
	RulesDir     string // <proj>/.windsurf/rules (project scope only; "" at user)
	WorkflowsDir string // <proj>/.windsurf/workflows (project) / ~/.codeium/windsurf/global_workflows (user)
}

// ResolvePaths returns the Paths for the given target root and optional project.
func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
	if projectScope && project != "" {
		ws := filepath.Join(project, ".windsurf")
		return Paths{
			ConfigDir:    ws,
			MCP:          "", // Windsurf MCP config is global-only
			GlobalRules:  "", // global_rules.md is global-only
			RulesDir:     filepath.Join(ws, "rules"),
			WorkflowsDir: filepath.Join(ws, "workflows"),
		}
	}
	cfg := filepath.Join(targetRoot, ".codeium", "windsurf")
	return Paths{
		ConfigDir:    cfg,
		MCP:          filepath.Join(cfg, "mcp_config.json"),
		GlobalRules:  filepath.Join(cfg, "memories", "global_rules.md"),
		RulesDir:     "", // workspace rules dirs are project-scope; global memory targets GlobalRules
		WorkflowsDir: filepath.Join(cfg, "global_workflows"),
	}
}
