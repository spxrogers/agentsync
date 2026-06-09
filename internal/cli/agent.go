package cli

import (
	"errors"
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
	"gemini":   "gemini",
	"continue": "cn",
	"windsurf": "windsurf",
}

// boolStr returns "true" or "false" as a string.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

const validAgents = "claude, opencode, codex, cursor, gemini, continue, windsurf"

// v1Supported lists agents whose adapter actually emits ops today. Every valid
// agent (see validateAgent) now has a real adapter, so this gate is dormant —
// it stays as a guard for any FUTURE agent added to the valid set but registered
// as a noop placeholder: adding such an agent silently would produce `applied: 0
// ops` with no diagnostic, so `agent add` / `import` reject it with a status
// hint. (Allow override with AGENTSYNC_ALLOW_UNIMPLEMENTED=1 for plan/spec work.)
var v1Supported = map[string]bool{
	"claude":   true,
	"opencode": true,
	"codex":    true,
	"cursor":   true,
	"gemini":   true,
	"continue": true,
	"windsurf": true,
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "manage which agents agentsync targets"}
	cmd.AddCommand(
		&cobra.Command{Use: "add <name>", Short: "register an agent (claude | opencode | codex)", Args: cobra.ExactArgs(1), RunE: lockedRun(agentAddRun)},
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
	case "claude", "opencode", "codex", "cursor", "gemini", "continue", "windsurf":
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
		if errors.Is(err, os.ErrNotExist) {
			return p, nil, nil, fmt.Errorf("no agentsync config at %s; run `agentsync init` first", p)
		}
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
	// Round-trip: regenerate the [agents] block in inline-table format, then
	// splice it back in preserving everything OUTSIDE the agents config.
	//
	// We work line-by-line and drop every agents-owned line — both the inline
	// `[agents]` header form AND the idiomatic `[agents.<name>]` sub-table form
	// — then reinsert the regenerated block where the first agents section was.
	// The old string-search for a literal "[agents]" missed the sub-table form
	// entirely: it appended a second [agents] block while leaving the
	// sub-tables, defining agents.<name> twice and bricking the config (go-toml
	// rejects the duplicate on the next load).
	newSection := strings.TrimRight(buildAgentsSection(agents), "\n")
	newLines := strings.Split(newSection, "\n")

	lines := strings.Split(string(raw), "\n")
	out := make([]string, 0, len(lines)+len(newLines))
	insertAt := -1
	inAgents := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			// New section header: agents-owned iff it's [agents] or [agents.*].
			inAgents = trimmed == "[agents]" || strings.HasPrefix(trimmed, "[agents.")
			if inAgents {
				if insertAt < 0 {
					insertAt = len(out) // first agents section → reinsert here
				}
				continue // drop the header
			}
		}
		if inAgents {
			continue // drop lines inside an agents section
		}
		out = append(out, line)
	}

	if insertAt < 0 {
		// No agents section existed; append after a blank-line separator.
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, newLines...)
	} else {
		tail := append([]string(nil), out[insertAt:]...)
		out = append(out[:insertAt], newLines...)
		// Keep a blank line between the regenerated [agents] block and whatever
		// section follows, so repeated agent edits don't collapse the file's
		// section spacing. The loop above drops blank lines inside the agents
		// section, so a single separator is re-added each run (no accumulation).
		if len(tail) > 0 && strings.TrimSpace(tail[0]) != "" {
			out = append(out, "")
		}
		out = append(out, tail...)
	}
	return iox.AtomicWrite(p, []byte(strings.Join(out, "\n")), 0o644)
}

func agentAddRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := validateAgent(name); err != nil {
		return err
	}
	if !v1Supported[name] && os.Getenv("AGENTSYNC_ALLOW_UNIMPLEMENTED") != "1" {
		return fmt.Errorf("agent %q has no implemented adapter yet; "+
			"set AGENTSYNC_ALLOW_UNIMPLEMENTED=1 to register anyway and accept a no-op apply", name)
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
	home := paths.AgentsyncHome(paths.OSEnv{})
	v, ok := agents[name]
	if !ok {
		// Not registered. A removed agent can still own orphaned destination
		// files + state keys; `--purge` is the reachable cleanup path for it,
		// working purely from state. Still reject a name that was never a valid
		// agent, so `disable bogus --purge` doesn't report a misleading success.
		if purge {
			if err := validateAgent(name); err != nil {
				return err
			}
			return purgeAgentDests(cmd, name, home)
		}
		return fmt.Errorf("agent %q not registered (pass --purge to clean up an already-removed agent's leftover files)", name)
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

	prefix := name + ":"

	// File-owned dests (whole-file replace ops, e.g. ~/.claude/skills/<n>/
	// SKILL.md): the whole file is agentsync's, so a whole-file delete is
	// correct — unless another agent still owns the same shared file. The
	// dest path is field index 3 in a Files key ("agent:scope:project:path").
	purgedFilePaths := map[string]bool{}
	otherFilePaths := map[string]bool{}
	for key := range s.Files {
		parts := strings.SplitN(key, ":", 4)
		if len(parts) < 4 || parts[3] == "" {
			continue
		}
		if strings.HasPrefix(key, prefix) {
			purgedFilePaths[parts[3]] = true
		} else {
			otherFilePaths[parts[3]] = true
		}
	}

	// Key-owned dests (merge-key pointers within a possibly-shared file, e.g.
	// /mcpServers/<id> in ~/.claude.json): agentsync owns only those pointers,
	// NOT the whole file. A whole-file delete here would destroy the user's
	// foreign keys (other servers, top-level settings) with no backup. Collect
	// this agent's owned pointers per path so we can remove ONLY them.
	purgedKeyPtrs := map[string][]string{}
	for key := range s.Keys {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		parts := strings.SplitN(key, ":", 5)
		if len(parts) < 5 || parts[3] == "" {
			continue
		}
		purgedKeyPtrs[parts[3]] = append(purgedKeyPtrs[parts[3]], parts[4])
	}

	reg := registryFactory()
	a := reg.Lookup(name)
	deletedFiles := 0
	prunedFiles := 0
	sharedKept := 0
	if a != nil {
		var ops []adapter.FileOp
		// Whole-file deletes for file-owned dests not shared with another
		// agent. claude and opencode both render skills to the same path, so
		// deleting a shared one while purging one agent would destroy a file
		// the other still needs — Writer.Delete skips backup, so the loss is
		// unrecoverable. Keep those.
		for p := range purgedFilePaths {
			if otherFilePaths[p] {
				sharedKept++
				continue
			}
			ops = append(ops, adapter.FileOp{Action: "delete", Path: paths.FromHomeRelative(userHome, p)})
			deletedFiles++
		}
		// Pointer prunes for key-owned dests: an empty merge op carrying only
		// this agent's owned pointers, so MergeKeys removes exactly those and
		// preserves the user's (and any other agent's) keys in the shared file.
		if strat := a.KeyMergeStrategy(); strat != "" {
			for p, ptrs := range purgedKeyPtrs {
				abs := paths.FromHomeRelative(userHome, p)
				if _, err := os.Stat(abs); err != nil {
					continue // already gone; nothing to prune
				}
				ops = append(ops, adapter.FileOp{
					Action:        "write",
					Path:          abs,
					Content:       []byte("{}"),
					Mode:          0o644,
					MergeStrategy: strat,
					OwnedKeys:     ptrs,
				})
				prunedFiles++
			}
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

	fmt.Fprintf(cmd.OutOrStdout(), "purged agent %s: deleted %d file(s), pruned owned keys from %d shared file(s)\n", name, deletedFiles, prunedFiles)
	if sharedKept > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  kept %d shared path(s) still owned by another agent\n", sharedKept)
	}
	return nil
}
