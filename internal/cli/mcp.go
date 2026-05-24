package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/source"
)

// newMCPCmd implements `agentsync mcp {add,remove,list}`. The design
// spec also lists `set`, `enable`, `disable`; today add overwrites
// (effectively set) and the [enabled] field on MCPServerSpec is read by
// the loader, but the dedicated subcommands are deferred to v1.x.
func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "manage MCP servers in the canonical source",
	}
	cmd.AddCommand(
		newMCPAddCmd(),
		newMCPRemoveCmd(),
		newMCPListCmd(),
	)
	return cmd
}

func newMCPAddCmd() *cobra.Command {
	var (
		serverType string
		command    string
		argsCSV    string
		url        string
		envCSV     string
		agentsCSV  string
	)
	cmd := &cobra.Command{
		Use:   "add <id>",
		Short: "author mcp/<id>.toml in the canonical source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error {
				return mcpAddRun(cmd, home, id, serverType, command, argsCSV, url, envCSV, agentsCSV)
			})
		},
	}
	cmd.Flags().StringVar(&serverType, "type", "stdio", "server transport (stdio|http|sse)")
	cmd.Flags().StringVar(&command, "command", "", "executable for stdio transport")
	cmd.Flags().StringVar(&argsCSV, "args", "", "comma-separated args (escape commas with \\,)")
	cmd.Flags().StringVar(&url, "url", "", "server URL for http/sse transport")
	cmd.Flags().StringVar(&envCSV, "env", "", "KEY=VAL,KEY=VAL environment overrides")
	cmd.Flags().StringVar(&agentsCSV, "agents", "*", "comma-separated agent allowlist (\"*\" = all enabled)")
	return cmd
}

func mcpAddRun(cmd *cobra.Command, home, id, serverType, command, argsCSV, url, envCSV, agentsCSV string) error {
	if err := validateMCPID(id); err != nil {
		return err
	}
	// An explicitly empty --agents previously fell through to a nil allowlist,
	// which targetsAgent treats as "all enabled agents" — the silent opposite
	// of what a user passing "" likely intends. Reject it: use "*" for all, or
	// a comma-separated list.
	if cmd.Flags().Changed("agents") && strings.TrimSpace(agentsCSV) == "" {
		return fmt.Errorf(`--agents cannot be empty; pass "*" for all enabled agents or a comma-separated allowlist`)
	}
	if err := validateMCPSpec(serverType, command, url); err != nil {
		return err
	}

	spec := source.MCPServerSpec{Type: serverType}
	switch serverType {
	case "stdio":
		spec.Command = command
		spec.Args = splitArgs(argsCSV)
	case "http", "sse":
		spec.URL = url
	}
	if envCSV != "" {
		env, err := parseEnvCSV(envCSV)
		if err != nil {
			return err
		}
		spec.Env = env
	}
	if agentsCSV != "" {
		spec.Agents = splitAgents(agentsCSV)
	}

	dest := filepath.Join(home, "mcp", id+".toml")
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("mcp/%s.toml already exists; remove it first or edit by hand", id)
	}
	if err := source.WriteMCP(home, id, source.MCPServer{Server: spec}); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "added mcp server: %s -> %s\n", id, dest)
	return nil
}

func newMCPRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "delete mcp/<id>.toml from the canonical source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if err := validateMCPID(id); err != nil {
				return err
			}
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error {
				p := filepath.Join(home, "mcp", id+".toml")
				if err := os.Remove(p); err != nil {
					if os.IsNotExist(err) {
						return fmt.Errorf("mcp/%s.toml not found", id)
					}
					return fmt.Errorf("remove %s: %w", p, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "removed mcp server: %s\n", id)
				return nil
			})
		},
	}
}

func newMCPListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list MCP servers in the canonical source",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			dir := filepath.Join(home, "mcp")
			entries, err := os.ReadDir(dir)
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("read %s: %w", dir, err)
			}
			var ids []string
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
					continue
				}
				ids = append(ids, strings.TrimSuffix(e.Name(), ".toml"))
			}
			sort.Strings(ids)
			if len(ids) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no MCP servers; try: agentsync mcp add github --command npx --args \"-y,@modelcontextprotocol/server-github\")")
				return nil
			}
			for _, id := range ids {
				fmt.Fprintln(cmd.OutOrStdout(), id)
			}
			return nil
		},
	}
}

// validateMCPID rejects ids that would not produce a clean
// mcp/<id>.toml file path.
func validateMCPID(id string) error {
	if id == "" {
		return fmt.Errorf("mcp id is empty")
	}
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return fmt.Errorf("mcp id %q contains a path component", id)
	}
	return nil
}

// validateMCPSpec enforces the type ⇄ command/url contract.
func validateMCPSpec(serverType, command, url string) error {
	switch serverType {
	case "stdio":
		if command == "" {
			return fmt.Errorf("--command required for stdio transport")
		}
		if url != "" {
			return fmt.Errorf("--url not allowed for stdio transport")
		}
	case "http", "sse":
		if url == "" {
			return fmt.Errorf("--url required for %s transport", serverType)
		}
		if command != "" {
			return fmt.Errorf("--command not allowed for %s transport", serverType)
		}
	default:
		return fmt.Errorf("unknown --type %q (want stdio|http|sse)", serverType)
	}
	return nil
}

// splitArgs splits a comma-separated args string, honoring \, as an
// escaped literal comma so users can pass values containing commas.
func splitArgs(s string) []string {
	if s == "" {
		return nil
	}
	var (
		out []string
		buf strings.Builder
	)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 < len(s) && s[i+1] == ',' {
				buf.WriteByte(',')
				i++
				continue
			}
			buf.WriteByte(s[i])
		case ',':
			out = append(out, buf.String())
			buf.Reset()
		default:
			buf.WriteByte(s[i])
		}
	}
	out = append(out, buf.String())
	return out
}

// parseEnvCSV parses "KEY=VAL,KEY=VAL" into a map.
func parseEnvCSV(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range splitArgs(s) {
		i := strings.IndexByte(pair, '=')
		if i <= 0 {
			return nil, fmt.Errorf("--env entry %q must be KEY=VAL", pair)
		}
		out[pair[:i]] = pair[i+1:]
	}
	return out, nil
}

// splitAgents normalizes a comma-separated agents allowlist.
func splitAgents(s string) []string {
	parts := splitArgs(s)
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
