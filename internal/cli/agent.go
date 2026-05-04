package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/paths"
)

// boolStr returns "true" or "false" as a string.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

const validAgents = "claude, opencode, codex, cursor"

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "manage which agents agentsync targets"}
	cmd.AddCommand(
		&cobra.Command{Use: "add <name>", Args: cobra.ExactArgs(1), RunE: agentAddRun},
		&cobra.Command{Use: "remove <name>", Args: cobra.ExactArgs(1), RunE: agentRemoveRun},
		&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: agentListRun},
	)
	return cmd
}

type opensyncCfg struct {
	Agents map[string]map[string]any `toml:"agents"`
	// other top-level keys preserved verbatim via decoder
}

// agentName must be one of the recognized adapter names. M0 only accepts the
// four — adding a new agent in v1.x is a code change, not a config change.
func validateAgent(name string) error {
	switch name {
	case "claude", "opencode", "codex", "cursor":
		return nil
	}
	return fmt.Errorf("unknown agent %q; valid: %s", name, validAgents)
}

// readAgentsyncTOML returns the file path + raw bytes + parsed `agents` section.
func readAgentsyncTOML() (string, []byte, map[string]map[string]any, error) {
	home := paths.AgentsyncHome(paths.OSEnv{})
	p := filepath.Join(home, "agentsync.toml")
	raw, err := os.ReadFile(p)
	if err != nil {
		return p, nil, nil, fmt.Errorf("read %s: %w", p, err)
	}
	var cfg opensyncCfg
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return p, raw, nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]map[string]any{}
	}
	return p, raw, cfg.Agents, nil
}

// buildAgentsSection returns a TOML [agents] block with inline table values,
// preserving comment header context. Agents are sorted alphabetically.
func buildAgentsSection(agents map[string]map[string]any) string {
	names := make([]string, 0, len(agents))
	for n := range agents {
		names = append(names, n)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("[agents]\n")
	for _, n := range names {
		v := agents[n]
		enabled, _ := v["enabled"].(bool)
		scope, _ := v["scope"].(string)
		fmt.Fprintf(&sb, "%s = { enabled = %s, scope = %q }\n", n, boolStr(enabled), scope)
	}
	return sb.String()
}

func writeAgents(p string, raw []byte, agents map[string]map[string]any) error {
	// Round-trip: regenerate the [agents] block using inline table format,
	// then splice it into the raw bytes preserving comments outside the section.
	newSection := buildAgentsSection(agents)
	rawStr := string(raw)
	start := strings.Index(rawStr, "[agents]")
	if start < 0 {
		// no [agents] section yet; append.
		return iox.AtomicWrite(p, []byte(rawStr+"\n"+newSection), 0o644)
	}
	// Find the next top-level section header that is NOT part of [agents.*].
	// We scan line-by-line from after the [agents] line to find the first
	// line that starts with "[" but is NOT "[agents" (which would be a sub-table).
	afterAgents := rawStr[start+len("[agents]"):]
	tailIdx := -1
	lines := strings.Split(afterAgents, "\n")
	byteOffset := 0
	for i, line := range lines {
		if i == 0 {
			byteOffset += len(line) + 1
			continue
		}
		if strings.HasPrefix(line, "[") && !strings.HasPrefix(line, "[agents") {
			tailIdx = byteOffset
			break
		}
		byteOffset += len(line) + 1
	}
	var tail string
	if tailIdx >= 0 {
		tail = "\n" + afterAgents[tailIdx:]
	}
	out := rawStr[:start] + newSection + tail
	return iox.AtomicWrite(p, []byte(out), 0o644)
}

func agentAddRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := validateAgent(name); err != nil {
		return err
	}
	p, raw, agents, err := readAgentsyncTOML()
	if err != nil {
		return err
	}
	if _, ok := agents[name]; ok {
		fmt.Fprintf(cmd.OutOrStdout(), "agent %s already registered\n", name)
		return nil
	}
	agents[name] = map[string]any{"enabled": true, "scope": "user"}
	if err := writeAgents(p, raw, agents); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "added agent: %s\n", name)
	return nil
}

func agentRemoveRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	p, raw, agents, err := readAgentsyncTOML()
	if err != nil {
		return err
	}
	if _, ok := agents[name]; !ok {
		fmt.Fprintf(cmd.OutOrStdout(), "agent %s not registered\n", name)
		return nil
	}
	delete(agents, name)
	if err := writeAgents(p, raw, agents); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed agent: %s\n", name)
	return nil
}

func agentListRun(cmd *cobra.Command, _ []string) error {
	_, _, agents, err := readAgentsyncTOML()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(agents))
	for n := range agents {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "(no agents registered; try: agentsync agent add claude)")
		return nil
	}
	for _, n := range names {
		v := agents[n]
		enabled, _ := v["enabled"].(bool)
		scope, _ := v["scope"].(string)
		fmt.Fprintf(cmd.OutOrStdout(), "%-10s enabled=%t scope=%s\n", n, enabled, scope)
	}
	return nil
}
