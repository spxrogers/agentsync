package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// reconcileItem describes one classified item in the reconcile pass.
type reconcileItem struct {
	agentName string
	op        adapter.FileOp
	ptr       string // non-empty for key-level items
	cls       drift.Class
	hsrc      string
	happlied  string
	hdest     string
}

func newReconcileCmd() *cobra.Command {
	var autoWB, autoOR, autoSafe bool
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "interactively resolve drift",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return reconcileRun(cmd, cmd.InOrStdin(), autoWB, autoOR, autoSafe)
		},
	}
	cmd.Flags().BoolVar(&autoWB, "auto-writeback", false, "auto-resolve drift by writing dest back to source")
	cmd.Flags().BoolVar(&autoOR, "auto-override", false, "auto-resolve drift by re-applying source to dest")
	cmd.Flags().BoolVar(&autoSafe, "auto-safe", false, "auto-resolve only converged/pending/new (no-op)")
	return cmd
}

func reconcileRun(cmd *cobra.Command, in io.Reader, autoWB, autoOR, autoSafe bool) error {
	home := paths.AgentsyncHome(paths.OSEnv{})
	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		return err
	}
	statePath := filepath.Join(home, ".state", "targets.json")
	s, err := state.Load(statePath)
	if err != nil {
		return err
	}
	reg := registryFactory()
	var agents []string
	for name, ag := range c.Config.Agents {
		if ag.Enabled {
			agents = append(agents, name)
		}
	}
	plan, err := render.Plan(c, reg, agents, adapter.ScopeUser, "", s)
	if err != nil {
		return err
	}

	// Collect all items in order.
	items := collectItems(plan, reg, s)

	w := cmd.OutOrStdout()

	// No drifted items?
	needsPrompt := 0
	for _, it := range items {
		if requiresAction(it.cls) {
			needsPrompt++
		}
	}
	if needsPrompt == 0 {
		fmt.Fprintln(w, "nothing to reconcile")
		return nil
	}

	// Track whether we need to re-apply for override actions.
	var overrideOps []struct {
		agentName string
		op        adapter.FileOp
	}

	// bulkAction is set when user presses W/O/S to apply to all remaining items.
	bulkAction := byte(0)

	br := bufio.NewReader(in)

	for _, it := range items {
		if !requiresAction(it.cls) {
			continue
		}

		// Apply bulk action if set.
		action := bulkAction
		if action == 0 {
			if autoWB {
				action = 'w'
			} else if autoOR {
				action = 'o'
			} else if autoSafe {
				// auto-safe: skip non-safe items (they require prompting, but
				// auto-safe only silently handles safe ones which never reach here).
				action = 's'
			}
		}

		if action == 0 {
			// Interactive prompt.
			label := itemLabel(it)
			fmt.Fprintf(w, "\n%s  (%s)\n", label, it.cls)
			fmt.Fprintf(w, "  source:      %s\n", shortVal(it.hsrc))
			fmt.Fprintf(w, "  destination: %s\n", shortVal(it.hdest))
			fmt.Fprintf(w, "  [w]rite-back  [o]verride  [s]kip  [i]gnore  [d]iff  [q]uit\n  > ")

		prompt:
			for {
				ch, readErr := readChar(br)
				if readErr != nil {
					return nil // EOF → quit gracefully
				}
				switch ch {
				case 'w', 'W', 'o', 'O', 's', 'S', 'i', 'q', 'Q':
					if ch == 'W' || ch == 'O' || ch == 'S' {
						bulkAction = ch | 0x20 // lowercase
					}
					action = ch | 0x20 // normalize to lowercase for switch below
					fmt.Fprintf(w, "%c\n", ch)
					break prompt
				case 'd':
					printItemDiff(w, it)
					fmt.Fprintf(w, "  [w]rite-back  [o]verride  [s]kip  [i]gnore  [d]iff  [q]uit\n  > ")
				default:
					// ignore unknown key
				}
			}
		}

		switch action {
		case 'w':
			// write-back: persist destination value into the canonical source.
			if err := writeBackItem(home, it); err != nil {
				fmt.Fprintf(w, "  write-back error: %v\n", err)
			} else {
				fmt.Fprintf(w, "  write-back: %s\n", itemLabel(it))
			}
		case 'o':
			// override: queue a re-apply of this item's op.
			overrideOps = append(overrideOps, struct {
				agentName string
				op        adapter.FileOp
			}{it.agentName, it.op})
		case 's':
			// skip: do nothing.
		case 'i':
			// ignore: append to ignore.toml (best-effort).
			_ = appendIgnore(home, itemLabel(it))
			fmt.Fprintf(w, "  ignored: %s\n", itemLabel(it))
		case 'q':
			fmt.Fprintln(w, "quit")
			goto done
		}
	}

done:
	// Execute override re-applies.
	if len(overrideOps) > 0 {
		// Re-run Apply for the full plan; re-renders source values on top of dest.
		if err := render.Apply(plan, reg); err != nil {
			return fmt.Errorf("reconcile override apply: %w", err)
		}
		// Update state.
		for name, res := range plan.PerAgent {
			if err := render.RecordOpsState(s, name, adapter.ScopeUser, "", res.Ops); err != nil {
				return err
			}
		}
		if err := state.Save(statePath, s); err != nil {
			return err
		}
		fmt.Fprintf(w, "override: applied %d item(s)\n", len(overrideOps))
	}

	return nil
}

// collectItems builds the flat reconcile list from a rendered plan + state.
func collectItems(plan render.RenderPlan, reg *adapter.Registry, s *state.Targets) []reconcileItem {
	var items []reconcileItem
	for _, name := range reg.Names() {
		res, ok := plan.PerAgent[name]
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for _, op := range res.Ops {
			if op.MergeStrategy == "merge-json-keys" || op.MergeStrategy == "merge-jsonc-keys" {
				if seen[op.Path] {
					continue
				}
				seen[op.Path] = true
				var ours map[string]interface{}
				_ = json.Unmarshal(op.Content, &ours)
				final := readJSONFile(op.Path)
				for _, ptr := range render.CollectPointers(ours, "") {
					hsrc := hashAnyValue(getPointerValue(ours, ptr))
					happlied := s.Keys[fmt.Sprintf("%s:user::%s:%s", name, op.Path, ptr)].SHA256
					hdest := hashAnyValue(getPointerValue(final, ptr))
					cls := drift.Classify(hsrc, happlied, hdest)
					items = append(items, reconcileItem{
						agentName: name,
						op:        op,
						ptr:       ptr,
						cls:       cls,
						hsrc:      hsrc,
						happlied:  happlied,
						hdest:     hdest,
					})
				}
			} else {
				if seen[op.Path] {
					continue
				}
				seen[op.Path] = true
				hsrc := hashContent(op.Content)
				happlied := s.Files[fmt.Sprintf("%s:user::%s", name, op.Path)].SHA256
				hdest := hashFile(op.Path)
				cls := drift.Classify(hsrc, happlied, hdest)
				items = append(items, reconcileItem{
					agentName: name,
					op:        op,
					cls:       cls,
					hsrc:      hsrc,
					happlied:  happlied,
					hdest:     hdest,
				})
			}
		}
	}
	return items
}

// requiresAction returns true for drift classes that need user (or auto) action.
func requiresAction(cls drift.Class) bool {
	switch cls {
	case drift.Drift, drift.Conflict, drift.OrphanDrifted:
		return true
	}
	return false
}

func itemLabel(it reconcileItem) string {
	if it.ptr != "" {
		return fmt.Sprintf("%s#%s", it.op.Path, it.ptr)
	}
	return it.op.Path
}

func shortVal(hash string) string {
	if hash == "" {
		return "<absent>"
	}
	if len(hash) > 16 {
		return hash[:16] + "..."
	}
	return hash
}

func printItemDiff(w io.Writer, it reconcileItem) {
	fmt.Fprintf(w, "  --- source\n  +++ dest\n  (hash) src=%s dest=%s\n", shortVal(it.hsrc), shortVal(it.hdest))
}

// readChar reads a single non-whitespace character from r.
func readChar(r *bufio.Reader) (byte, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b != '\n' && b != '\r' && b != ' ' && b != '\t' {
			return b, nil
		}
	}
}

// writeBackItem persists the current destination value for item it back into
// the canonical source (~/.agentsync/). Only MCP-server items are fully
// supported in v1; other item types fall back to a raw file copy.
func writeBackItem(home string, it reconcileItem) error {
	if it.ptr != "" {
		return writeBackKeyItem(home, it)
	}
	return writeBackFileItem(home, it)
}

// writeBackKeyItem handles key-level (merge-json-keys / merge-jsonc-keys) items.
// For MCP servers it reconstructs a source.MCPServer from the destination JSON
// and writes it with source.WriteMCP. For other key-level items it is a no-op
// (unsupported in v1).
func writeBackKeyItem(home string, it reconcileItem) error {
	dest := readJSONFile(it.op.Path)
	// Extract mcpServers section if this is an MCP ptr.
	// Expected ptr shape: /mcpServers/<serverID>/...
	parts := strings.SplitN(strings.TrimPrefix(it.ptr, "/"), "/", 3)
	if len(parts) >= 2 && parts[0] == "mcpServers" {
		serverID := parts[1]
		mcpServers, _ := dest["mcpServers"].(map[string]any)
		if mcpServers == nil {
			return fmt.Errorf("mcpServers not found in destination")
		}
		specRaw, ok := mcpServers[serverID]
		if !ok {
			// Server removed from dest; skip write-back.
			return nil
		}
		// Round-trip through JSON to get a typed spec.
		specBytes, err := json.Marshal(specRaw)
		if err != nil {
			return fmt.Errorf("marshal mcp spec %s: %w", serverID, err)
		}
		var spec source.MCPServerSpec
		if err := json.Unmarshal(specBytes, &spec); err != nil {
			return fmt.Errorf("unmarshal mcp spec %s: %w", serverID, err)
		}
		m := source.MCPServer{ID: serverID, Server: spec}
		return source.WriteMCP(home, serverID, m)
	}
	// Other key-level items not yet supported.
	return nil
}

// writeBackFileItem handles file-level (replace strategy) items by copying
// the destination file back into the corresponding source location verbatim.
// This covers subagents, commands, memory, and skill files in v1.
func writeBackFileItem(home string, it reconcileItem) error {
	data, err := os.ReadFile(it.op.Path)
	if err != nil {
		return fmt.Errorf("read dest %s: %w", it.op.Path, err)
	}
	// Derive a relative source path from the SourceID field when possible.
	// SourceID examples: "agents/reviewer.md", "skills/foo/SKILL.md",
	//                    "memory/AGENTS.md", "commands/review.md".
	srcID := it.op.SourceID
	if srcID == "" || strings.HasSuffix(srcID, "(multiple)") {
		// Cannot determine a single source file; skip.
		return nil
	}
	dest := filepath.Join(home, srcID)
	return iox.AtomicWrite(dest, data, 0o644)
}

// appendIgnore appends the label to ~/.agentsync/ignore.toml (best-effort).
func appendIgnore(home, label string) error {
	p := filepath.Join(home, "ignore.toml")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "ignore = %q\n", strings.TrimSpace(label))
	return err
}
