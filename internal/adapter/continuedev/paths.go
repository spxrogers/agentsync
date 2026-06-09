package continuedev

import "path/filepath"

// memoryRuleFile is the agentsync-owned rule file the canonical memory body is
// projected to. A rule with no frontmatter is always-applied by Continue (per
// docs.continue.dev/customize/deep-dives/rules), which is the memory semantics.
const memoryRuleFile = "agentsync.md"

// Paths resolves the destination directories for a given (scope, project,
// target-root). Continue uses the same `.continue/` layout at user scope
// (~/.continue) and project scope (<repo>/.continue).
type Paths struct {
	ConfigDir  string // ~/.continue (or <proj>/.continue)
	MCPDir     string // .continue/mcpServers (one YAML block per server)
	RulesDir   string // .continue/rules (memory → agentsync.md)
	PromptsDir string // .continue/prompts (one prompt block per command)
}

// ResolvePaths returns the Paths for the given target root and optional project.
func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
	base := filepath.Join(targetRoot, ".continue")
	if projectScope && project != "" {
		base = filepath.Join(project, ".continue")
	}
	return Paths{
		ConfigDir:  base,
		MCPDir:     filepath.Join(base, "mcpServers"),
		RulesDir:   filepath.Join(base, "rules"),
		PromptsDir: filepath.Join(base, "prompts"),
	}
}
