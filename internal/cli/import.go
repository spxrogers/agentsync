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
	"github.com/spxrogers/agentsync/internal/capture"
	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// loaderFsForState returns an afero.Fs suitable for re-loading the
// canonical repo when seeding state — the OS FS is correct here because
// state lives on the same disk as ~/.agentsync/.
func loaderFsForState() afero.Fs { return afero.NewOsFs() }

// jsonUnmarshalLoose is a thin wrapper that returns nil on empty input
// (so callers can treat empty as "absent") and surfaces real parse errors.
// It accepts JSONC (comments, trailing commas) via hujson so seeding state
// from a hand-commented opencode.json doesn't mis-hash the dest as null —
// matching the apply/ingest read path.
func jsonUnmarshalLoose(data []byte, v *map[string]any) error {
	if len(data) == 0 {
		*v = map[string]any{}
		return nil
	}
	std, err := standardizeJSONC(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(std, v)
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
// Selector grammar: <agent>[:<component>[:<name>]]
// e.g. claude (full config), claude:mcp (all servers), claude:mcp:github (one).
func newImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <agent>[:<component>[:<name>]]",
		Args:  cobra.ExactArgs(1),
		Short: "capture native config back into the canonical source",
		Long: `import reads items from an agent's native config and writes them back
into ~/.agentsync/ as the canonical source of truth.

Selector grammar: <agent>[:<component>[:<name>]]
  agent     - registered agent name (claude, opencode, codex, cursor)
  component - mcp | skill | agent | command | hook | lsp | memory
  name      - item name (server id, skill name, subagent name, hook event)

Dropping the name imports every entry of that component; dropping the
component too imports the agent's full native config in one pass.

Examples:
  agentsync import claude                 # all importable components
  agentsync import claude:mcp             # every MCP server
  agentsync import claude:mcp:github      # a single MCP server
  agentsync import opencode:agent:reviewer`,
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
	// Gate codex/cursor the same way `agent add` does: they're registered as
	// noop adapters, so Ingest returns an empty canonical and import would
	// otherwise fail with a misleading "<component> not found in native config".
	// Tell the user the agent is unimplemented instead.
	if !v1Supported[agentName] && os.Getenv("AGENTSYNC_ALLOW_UNIMPLEMENTED") != "1" {
		return fmt.Errorf("agent %q is not yet implemented in v1.0 "+
			"(codex is planned for v1.1, cursor for v1.2); "+
			"set AGENTSYNC_ALLOW_UNIMPLEMENTED=1 to import from its noop adapter anyway", agentName)
	}

	c, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		return fmt.Errorf("ingest %s: %w", agentName, err)
	}

	// Three selector depths, narrowing from broad to specific:
	//   <agent>                     -> every importable component
	//   <agent>:<component>         -> every entry of that component
	//   <agent>:<component>:<name>  -> a single named entry
	// The bulk forms reuse the same per-component importers with an empty name
	// filter, so MCP/LSP/hook capture still routes through capture.Capture
	// (secret re-referencing + source-only field preservation) — see the
	// importer bodies. Text components write directly via source.Write*.
	switch {
	case component == "":
		if err := importAllComponents(cmd, home, agentName, c); err != nil {
			return err
		}
	default:
		n, err := importComponent(cmd, home, c, component, name)
		if err != nil {
			return err
		}
		// A bulk component import (no name) that matched nothing is not an
		// error — report it and exit cleanly. A named import that matched
		// nothing already returned a "not found" error above.
		if n == 0 && name == "" {
			fmt.Fprintf(cmd.OutOrStdout(), "no %s found in %s native config\n", component, agentName)
		}
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
		fmt.Fprintf(ew, "  Run `agentsync import %s` to capture the agent's full config, or accept the backup on next apply.\n", agentName)
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
	c, err := marketplace.LoadProjected(loaderFsForState(), home, pluginCacheRoot)
	if err != nil {
		return nil
	}
	a := reg.Lookup(agentName)
	if a == nil {
		return nil
	}
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
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
	c, err := marketplace.LoadProjected(loaderFsForState(), home, pluginCacheRoot)
	if err != nil {
		return err
	}
	a := reg.Lookup(agentName)
	if a == nil {
		return fmt.Errorf("adapter %q not registered", agentName)
	}
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
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

// parseSelector splits "agent[:component[:name]]" into its parts. A bare
// "agent" (component == "") selects the whole config; "agent:component"
// (name == "") selects every entry of that component.
func parseSelector(sel string) (agentName, component, name string, err error) {
	parts := strings.SplitN(sel, ":", 3)
	agentName = parts[0]
	if len(parts) >= 2 {
		component = parts[1]
	}
	if len(parts) == 3 {
		name = parts[2]
	}
	if agentName == "" {
		return "", "", "", fmt.Errorf("invalid selector %q: agent must be non-empty; expected <agent>[:<component>[:<name>]]", sel)
	}
	// "claude:" is a typo, not a request to import the whole agent.
	if len(parts) >= 2 && component == "" {
		return "", "", "", fmt.Errorf("invalid selector %q: component must be non-empty", sel)
	}
	return agentName, component, name, nil
}

// importComponentOrder lists the importable components in the order a
// full-agent import walks them (and the order they appear in the summary).
// "subagent" is an accepted alias for "agent" in selectors but is not listed
// here to avoid importing subagents twice.
var importComponentOrder = []string{"mcp", "lsp", "skill", "agent", "command", "hook", "memory"}

// importComponent imports one component class from c. When name is empty it
// imports every entry of that component (the bulk form); when name is set it
// imports just that entry and errors if it is absent. It returns the number of
// source items written.
func importComponent(cmd *cobra.Command, home string, c source.Canonical, component, name string) (int, error) {
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
		return 0, fmt.Errorf("unknown component %q; valid: mcp, skill, agent, command, hook, lsp, memory", component)
	}
}

// importAllComponents imports every importable component for the agent and
// prints a one-line summary. Empty components are skipped silently; an agent
// with nothing to import reports that and exits cleanly.
func importAllComponents(cmd *cobra.Command, home, agentName string, c source.Canonical) error {
	counts := map[string]int{}
	total := 0
	for _, comp := range importComponentOrder {
		n, err := importComponent(cmd, home, c, comp, "")
		if err != nil {
			return err
		}
		if n > 0 {
			counts[comp] = n
			total += n
		}
	}
	if total == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "no importable items found in %s native config\n", agentName)
		return nil
	}
	var parts []string
	for _, comp := range importComponentOrder {
		if counts[comp] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[comp], comp))
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "imported %d item(s) from %s: %s\n", total, agentName, strings.Join(parts, ", "))
	return nil
}

// importMCP captures the MCP server named name (or all of them when name is
// empty). Capture.Capture batches the whole slice in one call, so it
// re-references secrets and preserves source-only fields for every server.
func importMCP(cmd *cobra.Command, home string, c source.Canonical, name string) (int, error) {
	var matched []source.MCPServer
	for _, m := range c.MCPServers {
		if name == "" || m.ID == name {
			matched = append(matched, m)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return 0, fmt.Errorf("mcp server %q not found in native config", name)
		}
		return 0, nil
	}
	single := source.Canonical{MCPServers: matched}
	if _, err := capture.Capture(home, &single, capture.Opts{Warn: cmd.ErrOrStderr()}); err != nil {
		return 0, err
	}
	for _, m := range matched {
		fmt.Fprintf(cmd.OutOrStdout(), "imported mcp/%s.toml\n", m.ID)
	}
	return len(matched), nil
}

func importSkill(cmd *cobra.Command, home string, c source.Canonical, name string) (int, error) {
	var matched []source.Skill
	for _, sk := range c.Skills {
		if name == "" || sk.Name == name {
			matched = append(matched, sk)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return 0, fmt.Errorf("skill %q not found in native config", name)
		}
		return 0, nil
	}
	for _, sk := range matched {
		if err := source.WriteSkill(home, sk); err != nil {
			return 0, fmt.Errorf("write skill %s: %w", sk.Name, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "imported skills/%s/SKILL.md\n", sk.Name)
	}
	return len(matched), nil
}

func importSubagent(cmd *cobra.Command, home string, c source.Canonical, name string) (int, error) {
	var matched []source.Subagent
	for _, sa := range c.Subagents {
		if name == "" || sa.Name == name {
			matched = append(matched, sa)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return 0, fmt.Errorf("subagent %q not found in native config", name)
		}
		return 0, nil
	}
	for _, sa := range matched {
		if err := source.WriteSubagent(home, sa); err != nil {
			return 0, fmt.Errorf("write subagent %s: %w", sa.Name, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "imported agents/%s.md\n", sa.Name)
	}
	return len(matched), nil
}

func importCommand(cmd *cobra.Command, home string, c source.Canonical, name string) (int, error) {
	var matched []source.Command
	for _, cm := range c.Commands {
		if name == "" || cm.Name == name {
			matched = append(matched, cm)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return 0, fmt.Errorf("command %q not found in native config", name)
		}
		return 0, nil
	}
	for _, cm := range matched {
		if err := source.WriteCommand(home, cm); err != nil {
			return 0, fmt.Errorf("write command %s: %w", cm.Name, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "imported commands/%s.md\n", cm.Name)
	}
	return len(matched), nil
}

// importHook captures hooks for the named event (or all events when name is
// empty). name addresses an event, not an individual hook, so the count
// returned is the number of hook entries written across all matched events.
func importHook(cmd *cobra.Command, home string, c source.Canonical, name string) (int, error) {
	var matched []source.Hook
	for _, h := range c.Hooks {
		if name == "" || h.Event == name {
			matched = append(matched, h)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return 0, fmt.Errorf("hook event %q not found in native config", name)
		}
		return 0, nil
	}
	single := source.Canonical{Hooks: matched}
	if _, err := capture.Capture(home, &single, capture.Opts{Warn: cmd.ErrOrStderr()}); err != nil {
		return 0, err
	}
	// One file per event; report each, preserving first-seen order.
	perEvent := map[string]int{}
	var order []string
	for _, h := range matched {
		if _, seen := perEvent[h.Event]; !seen {
			order = append(order, h.Event)
		}
		perEvent[h.Event]++
	}
	for _, ev := range order {
		fmt.Fprintf(cmd.OutOrStdout(), "imported hooks/%s.toml (%d entries)\n", ev, perEvent[ev])
	}
	return len(matched), nil
}

func importLSP(cmd *cobra.Command, home string, c source.Canonical, name string) (int, error) {
	var matched []source.LSPServer
	for _, ls := range c.LSPServers {
		if name == "" || ls.ID == name {
			matched = append(matched, ls)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return 0, fmt.Errorf("lsp server %q not found in native config", name)
		}
		return 0, nil
	}
	single := source.Canonical{LSPServers: matched}
	if _, err := capture.Capture(home, &single, capture.Opts{Warn: cmd.ErrOrStderr()}); err != nil {
		return 0, err
	}
	for _, ls := range matched {
		fmt.Fprintf(cmd.OutOrStdout(), "imported lsp/%s.toml\n", ls.ID)
	}
	return len(matched), nil
}

func importMemory(cmd *cobra.Command, home string, c source.Canonical) (int, error) {
	// Memory is a single block, not a named collection; nothing to write when
	// the agent carries no memory (the common case during a full-agent import).
	if strings.TrimSpace(c.Memory.Body) == "" {
		return 0, nil
	}
	if err := source.WriteMemory(home, c.Memory); err != nil {
		return 0, fmt.Errorf("write memory: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "imported memory/AGENTS.md\n")
	return 1, nil
}
