package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// loaderFsForState returns an afero.Fs suitable for re-loading the
// canonical repo when seeding state — the OS FS is correct here because
// state lives on the same disk as ~/.agentsync/.
func loaderFsForState() afero.Fs { return afero.NewOsFs() }

// jsonUnmarshalLoose is a thin wrapper that returns nil on empty input
// (so callers can treat empty as "absent") and surfaces real parse errors.
func jsonUnmarshalLoose(data []byte, v *map[string]any) error {
	if len(data) == 0 {
		*v = map[string]any{}
		return nil
	}
	return json.Unmarshal(data, v)
}

// collectStateSeedPointers returns the JSON pointers we record state for
// when seeding from a freshly imported canonical. We borrow the same
// ownership rule used by render.RecordOpsState (second-level granularity).
func collectStateSeedPointers(m map[string]any) []string {
	return render.CollectPointers(m, "")
}

// hashAtPointer returns the SHA-256 of the JSON-marshaled value at ptr,
// or the empty string if no value exists there.
func hashAtPointer(m map[string]any, ptr string) string {
	v := getJSONPointer(m, ptr)
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// getJSONPointer resolves a "/a/b/c" RFC 6901 pointer against m. Returns
// nil if any segment is missing. We re-implement here rather than
// exporting render.getPointer because keeping that helper unexported
// preserves the current package boundary.
func getJSONPointer(m map[string]any, ptr string) any {
	if ptr == "" || ptr[0] != '/' {
		return m
	}
	parts := strings.Split(ptr[1:], "/")
	for i, p := range parts {
		// Decode RFC 6901 escapes.
		p = strings.ReplaceAll(p, "~1", "/")
		p = strings.ReplaceAll(p, "~0", "~")
		parts[i] = p
	}
	var cur any = m
	for _, p := range parts {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

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
		RunE: func(cmd *cobra.Command, args []string) error {
			// import mutates ~/.agentsync/ AND .state/targets.json. It
			// must hold the global lock so a concurrent apply on the
			// other terminal cannot interleave its own state.Save and
			// destroy our seed entries (or vice versa).
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error {
				return importRun(cmd, args)
			})
		},
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
		err = importMCP(cmd, home, c, name)
	case "skill":
		err = importSkill(cmd, home, c, name)
	case "agent", "subagent":
		err = importSubagent(cmd, home, c, name)
	case "command":
		err = importCommand(cmd, home, c, name)
	case "hook":
		err = importHook(cmd, home, c, name)
	case "lsp":
		err = importLSP(cmd, home, c, name)
	case "memory":
		err = importMemory(cmd, home, c)
	default:
		return fmt.Errorf("unknown component %q; valid: mcp, skill, agent, command, hook, lsp, memory", component)
	}
	if err != nil {
		return err
	}

	// Seed state with the destination's current content hash so the next
	// \`apply\` sees Clean/Converged instead of ForeignCollision and
	// silently overwriting the file the user just imported from. We do
	// this from the *current on-disk dest* hashes — not the rendered
	// content — because the canonical→render pipeline may translate the
	// content slightly (frontmatter normalization, etc.) and we want the
	// next apply to compare against the file that exists today.
	if seedErr := seedStateFromCurrentDest(home, agentName, reg); seedErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: import succeeded but state seed failed: %v\n", seedErr)
	}

	// Warn if the destination file has additional pointers / files
	// that the canonical does not own. Those will appear as
	// ForeignCollision on the next apply and the user often expects
	// import to have captured everything — not just the one named item.
	if warnings := unimportedDestPointers(home, agentName, reg); len(warnings) > 0 {
		ew := cmd.ErrOrStderr()
		fmt.Fprintln(ew, "warning: the following destination items are NOT in the canonical source and will trigger ForeignCollision on next apply:")
		for _, w := range warnings {
			fmt.Fprintf(ew, "  %s\n", w)
		}
		fmt.Fprintln(ew, "  Run `agentsync import <agent>:<component>:<name>` for each, or accept the backup on next apply.")
	}
	return nil
}

// unimportedDestPointers returns a list of human-readable labels for
// destination pointers / files that exist on disk for the given agent
// but are NOT in the canonical model after the import. Used to alert
// the user that import covers ONE item and the rest of the destination
// is still unowned.
//
// We render canonical → ops, build the set of (path, pointer) pairs
// canonical claims, and diff against the actual on-disk contents.
func unimportedDestPointers(home, agentName string, reg *adapter.Registry) []string {
	pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
	c, err := source.LoadWithCache(loaderFsForState(), home, pluginCacheRoot)
	if err != nil {
		return nil
	}
	a := reg.Lookup(agentName)
	if a == nil {
		return nil
	}
	ops, _, err := a.Render(c, adapter.ScopeUser, "")
	if err != nil {
		return nil
	}
	var out []string
	for _, op := range ops {
		if op.MergeStrategy != "merge-json-keys" && op.MergeStrategy != "merge-jsonc-keys" {
			continue
		}
		data, readErr := os.ReadFile(op.Path)
		if readErr != nil {
			continue
		}
		var existing map[string]any
		if jsonErr := jsonUnmarshalLoose(data, &existing); jsonErr != nil {
			continue
		}
		var ours map[string]any
		if jsonErr := jsonUnmarshalLoose(op.Content, &ours); jsonErr != nil {
			continue
		}
		ownedPtrs := map[string]bool{}
		for _, p := range collectStateSeedPointers(ours) {
			ownedPtrs[p] = true
		}
		// Walk existing's second-level pointers and flag the ones
		// canonical doesn't own. We borrow CollectPointers indirectly
		// via collectStateSeedPointers for symmetry.
		for _, p := range collectStateSeedPointers(existing) {
			if !ownedPtrs[p] {
				out = append(out, op.Path+"#"+p)
			}
		}
	}
	return out
}

// seedStateFromCurrentDest re-renders the canonical for agent and writes
// state entries reflecting the current on-disk content of each destination.
// This makes the next \`apply\` non-destructive — the destination is
// already known to agentsync, so any future divergence is real drift, not a
// first-run foreign-collision.
func seedStateFromCurrentDest(home, agentName string, reg *adapter.Registry) error {
	statePath := filepath.Join(home, ".state", "targets.json")
	// State keys are HOME-relative against the user's $HOME (paths.HomeDir),
	// matching render.RecordOpsState — NOT the agentsync home, or apply
	// would never recognise the seeded entries as owned.
	userHome := paths.HomeDir(paths.OSEnv{})
	st, err := state.Load(statePath)
	if err != nil {
		return err
	}

	// Build a fresh canonical from disk and render only this agent.
	pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
	c, err := source.LoadWithCache(loaderFsForState(), home, pluginCacheRoot)
	if err != nil {
		return err
	}
	a := reg.Lookup(agentName)
	if a == nil {
		return fmt.Errorf("adapter %q not registered", agentName)
	}
	ops, _, err := a.Render(c, adapter.ScopeUser, "")
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	for _, op := range ops {
		if op.Action != "" && op.Action != "write" {
			continue
		}
		switch op.MergeStrategy {
		case "merge-json-keys", "merge-jsonc-keys":
			// Per-key seed: hash the *current* value at each pointer the
			// rendered op claims to own.
			data, readErr := os.ReadFile(op.Path)
			if readErr != nil {
				continue // dest doesn't exist yet; nothing to seed
			}
			var existing map[string]any
			if jsonErr := jsonUnmarshalLoose(data, &existing); jsonErr != nil {
				continue
			}
			var ours map[string]any
			if jsonErr := jsonUnmarshalLoose(op.Content, &ours); jsonErr != nil {
				continue
			}
			for _, ptr := range collectStateSeedPointers(ours) {
				key := fmt.Sprintf("%s:%s:%s:%s:%s",
					agentName, adapter.ScopeUser.String(), "", paths.HomeRelative(userHome, op.Path), ptr)
				st.Keys[key] = state.KeyEntry{
					SHA256:    hashAtPointer(existing, ptr),
					AppliedAt: now,
					SourceID:  op.SourceID,
				}
			}
		default:
			data, readErr := os.ReadFile(op.Path)
			if readErr != nil {
				continue
			}
			sum := sha256.Sum256(data)
			key := fmt.Sprintf("%s:%s:%s:%s",
				agentName, adapter.ScopeUser.String(), "", paths.HomeRelative(userHome, op.Path))
			st.Files[key] = state.FileEntry{
				SHA256:    hex.EncodeToString(sum[:]),
				Mode:      op.Mode,
				AppliedAt: now,
				SourceID:  op.SourceID,
			}
		}
	}

	return state.Save(statePath, st)
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
