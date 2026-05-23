package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// agentBinaries maps an agent name to the executable agentsync should
// look for on PATH at `agent add` time. Used to produce a warning when
// the user registers an agent whose binary is not installed.
var agentBinaries = map[string]string{
	"claude":   "claude",
	"opencode": "opencode",
	"codex":    "codex",
	"cursor":   "cursor",
}

// boolStr returns "true" or "false" as a string.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

const validAgents = "claude, opencode, codex, cursor"

// v1Supported lists agents whose adapter actually emits ops today.
// codex and cursor are registered as NoopAdapter in registry_internal.go;
// adding them silently would produce `applied: 0 ops` for those agents
// with no diagnostic — so `agent add` rejects them with a status hint.
// (Allow override with AGENTSYNC_ALLOW_UNIMPLEMENTED=1 for plan/spec work.)
var v1Supported = map[string]bool{
	"claude":   true,
	"opencode": true,
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "manage which agents agentsync targets"}
	cmd.AddCommand(
		&cobra.Command{Use: "add <name>", Short: "register an agent (claude | opencode)", Args: cobra.ExactArgs(1), RunE: lockedRun(agentAddRun)},
		&cobra.Command{Use: "remove <name>", Short: "unregister an agent", Args: cobra.ExactArgs(1), RunE: lockedRun(agentRemoveRun)},
		&cobra.Command{Use: "list", Short: "list registered agents", Args: cobra.NoArgs, RunE: agentListRun},
		newAgentEnableCmd(),
		newAgentDisableCmd(),
	)
	return cmd
}

func newAgentEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name>",
		Args:  cobra.ExactArgs(1),
		Short: "enable a registered agent",
		RunE:  lockedRun(agentEnableRun),
	}
}

func newAgentDisableCmd() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "disable <name>",
		Args:  cobra.ExactArgs(1),
		Short: "disable a registered agent (optionally purging its destination files)",
		RunE: lockedRun(func(cmd *cobra.Command, args []string) error {
			return agentDisableRun(cmd, args, purge)
		}),
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "remove agent destination files that agentsync owns")
	return cmd
}

type agentsyncCfg struct {
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
	var cfg agentsyncCfg
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
	if !v1Supported[name] && os.Getenv("AGENTSYNC_ALLOW_UNIMPLEMENTED") != "1" {
		return fmt.Errorf("agent %q is not yet implemented in v1.0 "+
			"(codex is planned for v1.1, cursor for v1.2); "+
			"set AGENTSYNC_ALLOW_UNIMPLEMENTED=1 to register anyway and accept noop apply", name)
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

	// Warn (don't fail) if the agent's binary is not on PATH. The user
	// may be authoring config on machine A to apply on machine B, so we
	// don't want a hard reject — but a silent success when Claude Code
	// isn't installed leaves them debugging "why didn't apply do
	// anything?" later.
	if bin, ok := agentBinaries[name]; ok {
		if _, err := exec.LookPath(bin); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: %s binary not found on PATH; "+
					"agentsync will still write config to its destination dirs "+
					"but %s itself must be installed to read it\n",
				bin, name)
		}
	}
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

func agentEnableRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	p, raw, agents, err := readAgentsyncTOML()
	if err != nil {
		return err
	}
	v, ok := agents[name]
	if !ok {
		return fmt.Errorf("agent %q not registered; use 'agentsync agent add %s' first", name, name)
	}
	v["enabled"] = true
	agents[name] = v
	if err := writeAgents(p, raw, agents); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "enabled agent: %s\n", name)
	return nil
}

func agentDisableRun(cmd *cobra.Command, args []string, purge bool) error {
	name := args[0]
	p, raw, agents, err := readAgentsyncTOML()
	if err != nil {
		return err
	}
	v, ok := agents[name]
	if !ok {
		return fmt.Errorf("agent %q not registered", name)
	}
	v["enabled"] = false
	agents[name] = v
	if err := writeAgents(p, raw, agents); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "disabled agent: %s\n", name)

	if !purge {
		return nil
	}

	// The whole disable command (including this purge) already runs under the
	// global lock via lockedRun, so call purge directly — re-acquiring here
	// would deadlock on the re-entrant flock.
	home := paths.AgentsyncHome(paths.OSEnv{})
	return purgeAgentDests(cmd, name, home)
}

// purgeAgentDests deletes the destination files owned solely by the named
// agent and removes its state entries. Must be called under withGlobalLock.
func purgeAgentDests(cmd *cobra.Command, name, home string) error {
	// State keys store dest paths HOME-relative ("${HOME}/.claude.json").
	// Expand them back to absolute via the user's $HOME before deleting —
	// otherwise os.Remove would target the literal "${HOME}/..." string,
	// silently delete nothing, yet still report "purged N path(s)".
	userHome := paths.HomeDir(paths.OSEnv{})
	statePath := filepath.Join(home, ".state", "targets.json")
	s, err := state.Load(statePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// Gather the portable ("${HOME}/...") dest paths owned by the agent
	// being purged, and — separately — those still owned by ANY OTHER agent.
	// The dest-path field is index 3 in both Files keys ("a:s:p:path") and
	// Keys keys ("a:s:p:path:ptr").
	prefix := name + ":"
	purgedPaths := map[string]bool{}
	otherPaths := map[string]bool{}
	collect := func(key string, fields int) {
		parts := strings.SplitN(key, ":", fields)
		if len(parts) < 4 || parts[3] == "" {
			return
		}
		if strings.HasPrefix(key, prefix) {
			purgedPaths[parts[3]] = true
		} else {
			otherPaths[parts[3]] = true
		}
	}
	for key := range s.Files {
		collect(key, 4)
	}
	for key := range s.Keys {
		collect(key, 5)
	}

	// Delete only paths NOT still owned by another agent. claude and
	// opencode both render skills to ~/.claude/skills/<name>/SKILL.md, so
	// after apply that path is owned by BOTH in state. Deleting it while
	// purging one agent would destroy a file the other still needs — and
	// Writer.Delete skips backup, so the loss is unrecoverable.
	toDelete := map[string]bool{}
	sharedKept := 0
	for p := range purgedPaths {
		if otherPaths[p] {
			sharedKept++
			continue
		}
		toDelete[paths.FromHomeRelative(userHome, p)] = true
	}

	// Build delete ops and apply via the adapter. Purge is destructive
	// by intent — we don't want collision-backups for files agentsync
	// is being told to remove — but we still route through DestWriter
	// because that's the only interface the adapter knows. The writer's
	// Delete path skips backup deliberately (see DestWriter doc).
	reg := registryFactory()
	a := reg.Lookup(name)
	if a == nil {
		// No adapter — just remove state entries.
	} else {
		var ops []adapter.FileOp
		for path := range toDelete {
			ops = append(ops, adapter.FileOp{Action: "delete", Path: path})
		}
		rw := render.NewWriter(s, home, userHome, adapter.ScopeUser, "", name)
		if err := a.Apply(ops, rw); err != nil {
			return fmt.Errorf("purge apply %s: %w", name, err)
		}
	}

	// Remove state entries for this agent.
	for key := range s.Files {
		if strings.HasPrefix(key, prefix) {
			delete(s.Files, key)
		}
	}
	for key := range s.Keys {
		if strings.HasPrefix(key, prefix) {
			delete(s.Keys, key)
		}
	}
	if err := state.Save(statePath, s); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "purged %d destination path(s) for agent %s\n", len(toDelete), name)
	if sharedKept > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  kept %d shared path(s) still owned by another agent\n", sharedKept)
	}
	return nil
}
