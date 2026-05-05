package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/source"
)

// newImportCmd returns the "import" subcommand.
// Selector grammar: <agent>:<component>:<name>
// e.g. claude:mcp:github  opencode:agent:reviewer
func newImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <agent>:<component>:<name>",
		Args:  cobra.ExactArgs(1),
		Short: "capture a native config item back into the canonical source",
		Long: `import reads the named item from the agent's native config and writes it
back into ~/.agentsync/ as the canonical source of truth.

Selector format: <agent>:<component>:<name>
  agent     - registered agent name (claude, opencode, codex, cursor)
  component - mcp | skill | agent | command | hook | lsp | memory
  name      - item name (server id, skill name, subagent name, etc.)

Examples:
  agentsync import claude:mcp:github
  agentsync import opencode:agent:reviewer
  agentsync import claude:command:review`,
		RunE: importRun,
	}
	return cmd
}

func importRun(cmd *cobra.Command, args []string) error {
	sel := args[0]
	agentName, component, name, err := parseSelector(sel)
	if err != nil {
		return err
	}

	home := paths.AgentsyncHome(paths.OSEnv{})
	reg := registryFactory()
	a := reg.Lookup(agentName)
	if a == nil {
		return fmt.Errorf("adapter %q not registered; valid agents: %s", agentName, validAgents)
	}

	c, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		return fmt.Errorf("ingest %s: %w", agentName, err)
	}

	switch component {
	case "mcp":
		return importMCP(cmd, home, c, name)
	case "skill":
		return importSkill(cmd, home, c, name)
	case "agent", "subagent":
		return importSubagent(cmd, home, c, name)
	case "command":
		return importCommand(cmd, home, c, name)
	case "hook":
		return importHook(cmd, home, c, name)
	case "lsp":
		return importLSP(cmd, home, c, name)
	case "memory":
		return importMemory(cmd, home, c)
	default:
		return fmt.Errorf("unknown component %q; valid: mcp, skill, agent, command, hook, lsp, memory", component)
	}
}

// parseSelector splits "agent:component:name" into its three parts.
func parseSelector(sel string) (agentName, component, name string, err error) {
	parts := strings.SplitN(sel, ":", 3)
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("invalid selector %q: expected <agent>:<component>:<name>", sel)
	}
	agentName = parts[0]
	component = parts[1]
	if len(parts) == 3 {
		name = parts[2]
	}
	if agentName == "" || component == "" {
		return "", "", "", fmt.Errorf("invalid selector %q: agent and component must be non-empty", sel)
	}
	return agentName, component, name, nil
}

func importMCP(cmd *cobra.Command, home string, c source.Canonical, name string) error {
	for _, m := range c.MCPServers {
		if m.ID == name {
			if err := source.WriteMCP(home, m.ID, m); err != nil {
				return fmt.Errorf("write mcp %s: %w", name, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "imported mcp/%s.toml\n", name)
			return nil
		}
	}
	return fmt.Errorf("mcp server %q not found in native config", name)
}

func importSkill(cmd *cobra.Command, home string, c source.Canonical, name string) error {
	for _, sk := range c.Skills {
		if sk.Name == name {
			if err := source.WriteSkill(home, sk); err != nil {
				return fmt.Errorf("write skill %s: %w", name, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "imported skills/%s/SKILL.md\n", name)
			return nil
		}
	}
	return fmt.Errorf("skill %q not found in native config", name)
}

func importSubagent(cmd *cobra.Command, home string, c source.Canonical, name string) error {
	for _, sa := range c.Subagents {
		if sa.Name == name {
			if err := source.WriteSubagent(home, sa); err != nil {
				return fmt.Errorf("write subagent %s: %w", name, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "imported agents/%s.md\n", name)
			return nil
		}
	}
	return fmt.Errorf("subagent %q not found in native config", name)
}

func importCommand(cmd *cobra.Command, home string, c source.Canonical, name string) error {
	for _, cm := range c.Commands {
		if cm.Name == name {
			if err := source.WriteCommand(home, cm); err != nil {
				return fmt.Errorf("write command %s: %w", name, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "imported commands/%s.md\n", name)
			return nil
		}
	}
	return fmt.Errorf("command %q not found in native config", name)
}

func importHook(cmd *cobra.Command, home string, c source.Canonical, name string) error {
	// Find hooks for the named event.
	var matched []source.Hook
	for _, h := range c.Hooks {
		if h.Event == name {
			matched = append(matched, h)
		}
	}
	if len(matched) == 0 {
		return fmt.Errorf("hook event %q not found in native config", name)
	}
	if err := source.WriteHooks(home, name, matched); err != nil {
		return fmt.Errorf("write hooks/%s: %w", name, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "imported hooks/%s.toml (%d entries)\n", name, len(matched))
	return nil
}

func importLSP(cmd *cobra.Command, home string, c source.Canonical, name string) error {
	for _, ls := range c.LSPServers {
		if ls.ID == name {
			if err := source.WriteLSP(home, ls); err != nil {
				return fmt.Errorf("write lsp %s: %w", name, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "imported lsp/%s.toml\n", name)
			return nil
		}
	}
	return fmt.Errorf("lsp server %q not found in native config", name)
}

func importMemory(cmd *cobra.Command, home string, c source.Canonical) error {
	if err := source.WriteMemory(home, c.Memory); err != nil {
		return fmt.Errorf("write memory: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "imported memory/AGENTS.md\n")
	return nil
}
