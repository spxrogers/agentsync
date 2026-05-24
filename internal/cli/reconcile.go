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
	"github.com/spxrogers/agentsync/internal/capture"
	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// reconcileItem describes one classified item in the reconcile pass.
type reconcileItem struct {
	agentName   string
	op          adapter.FileOp
	ptr         string // non-empty for key-level items
	cls         drift.Class
	hsrc        string
	happlied    string
	hdest       string
	scope       adapter.Scope
	projectRoot string
}

func newReconcileCmd() *cobra.Command {
	var (
		autoWB, autoOR, autoSafe bool
		scopeFlag, projectFlag   string
	)
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "interactively resolve drift",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error {
				return reconcileRun(cmd, cmd.InOrStdin(), autoWB, autoOR, autoSafe, scopeFlag, projectFlag)
			})
		},
	}
	cmd.Flags().BoolVar(&autoWB, "auto-writeback", false, "auto-resolve drift by writing dest back to source")
	cmd.Flags().BoolVar(&autoOR, "auto-override", false, "auto-resolve drift by re-applying source to dest")
	cmd.Flags().BoolVar(&autoSafe, "auto-safe", false, "auto-resolve only converged/pending/new (no-op)")
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: auto-detect from cwd)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project)")
	return cmd
}

func reconcileRun(cmd *cobra.Command, in io.Reader, autoWB, autoOR, autoSafe bool, scopeFlag, projectFlag string) error {
	// The three auto modes are mutually exclusive — writeback (dest→source)
	// and override (source→dest) are exact opposites, and silently accepting
	// both (writeback won) was a data-loss footgun.
	if n := b2i(autoWB) + b2i(autoOR) + b2i(autoSafe); n > 1 {
		return fmt.Errorf("--auto-writeback, --auto-override, and --auto-safe are mutually exclusive; pass at most one")
	}
	home := paths.AgentsyncHome(paths.OSEnv{})
	userHome := paths.HomeDir(paths.OSEnv{})
	// Project plugins like apply does so drift classification covers
	// plugin-managed components instead of reporting them as untracked.
	pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
	c, err := source.LoadWithCache(afero.NewOsFs(), home, pluginCacheRoot)
	if err != nil {
		return err
	}

	sc, projectRoot, err := resolveProjectScope(scopeFlag, projectFlag, c)
	if err != nil {
		return err
	}

	if sc == adapter.ScopeProject && projectRoot != "" {
		marker, merr := project.Discover(projectRoot)
		if merr != nil {
			return fmt.Errorf("load project marker: %w", merr)
		}
		if marker != nil {
			c = project.Merge(c, marker)
		}
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
	// reconcile hashes the rendered TEMPLATED source for drift; wrap as a
	// render-only Resolved without substituting (no backend needed).
	plan, err := render.Plan(secrets.ForRender(c), reg, agents, sc, projectRoot, s, userHome)
	if err != nil {
		return err
	}

	// Collect all items in order.
	items := collectItems(plan, reg, s, sc, projectRoot, userHome)

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

	// Track ops the user explicitly chose to override (re-apply source on
	// top of dest). We re-apply ONLY these ops at the end, never the full
	// plan — pressing [o] on one drifted item must not silently re-apply
	// every other item in the plan as a side effect.
	type overrideOp struct {
		agentName string
		op        adapter.FileOp
	}
	var overrideOps []overrideOp
	// dedupOverride keeps us from re-applying the same path twice when the
	// user picks [o] for two pointers inside the same merge file.
	dedupOverride := map[string]bool{}

	// bulkAction is set when user presses W/O/S to apply to all remaining items.
	bulkAction := byte(0)
	// autoSkipped counts items an --auto-* mode left unresolved, so the run
	// ends with a summary instead of silently doing nothing.
	autoSkipped := 0

	br := bufio.NewReader(in)

	for _, it := range items {
		if !requiresAction(it.cls) {
			continue
		}

		// Apply bulk action if set.
		action := bulkAction
		if action == 0 {
			switch {
			case autoWB:
				// ForeignCollision is a never-applied pre-existing native
				// file. Writing it back would overwrite the curated source
				// with foreign content — the worst data-loss path. Refuse to
				// do that non-interactively; leave it for an explicit choice.
				if it.cls == drift.ForeignCollision {
					fmt.Fprintf(w, "skipped (foreign-collision, would overwrite source): %s — resolve interactively\n", itemLabel(it))
					autoSkipped++
					action = 's'
				} else {
					action = 'w'
				}
			case autoOR:
				action = 'o'
			case autoSafe:
				// auto-safe: skip non-safe items (they require prompting, but
				// auto-safe only silently handles safe ones which never reach here).
				fmt.Fprintf(w, "skipped (needs manual review): %s (%s)\n", itemLabel(it), it.cls)
				autoSkipped++
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
						// Capital letter = "apply this choice to all
						// remaining items." Confirm before locking it
						// in — a stray shift-W on a hooks item used to
						// silently no-op data away across the whole
						// queue. Show the count and require an
						// explicit y/N. Default is N.
						remaining := 0
						for _, ri := range items {
							if requiresAction(ri.cls) {
								remaining++
							}
						}
						lower := ch | 0x20
						fmt.Fprintf(w, "%c\n", ch)
						fmt.Fprintf(w, "  apply '%c' to all %d remaining items? [y/N] ", lower, remaining)
						confirm, readErr := readChar(br)
						if readErr != nil {
							return nil
						}
						fmt.Fprintf(w, "%c\n", confirm)
						if confirm != 'y' && confirm != 'Y' {
							fmt.Fprintln(w, "  cancelled; choose a per-item action")
							continue
						}
						bulkAction = lower
						action = lower
						break prompt
					}
					action = ch | 0x20
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
			if err := writeBackItem(cmd, home, it); err != nil {
				fmt.Fprintf(w, "  write-back error: %v\n", err)
			} else {
				fmt.Fprintf(w, "  write-back: %s\n", itemLabel(it))
			}
		case 'o':
			// override: queue a re-apply of this item's op.
			dedupKey := it.agentName + "\x00" + it.op.Path
			if !dedupOverride[dedupKey] {
				dedupOverride[dedupKey] = true
				overrideOps = append(overrideOps, overrideOp{it.agentName, it.op})
			}
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
	// Execute override re-applies — ONLY for the ops the user opted into,
	// grouped by adapter so each adapter sees its own ops. The previous
	// implementation re-ran Apply for the entire plan, which silently
	// re-applied every other agent's ops as a side effect.
	if len(overrideOps) > 0 {
		byAgent := map[string][]adapter.FileOp{}
		for _, oo := range overrideOps {
			byAgent[oo.agentName] = append(byAgent[oo.agentName], oo.op)
		}
		for name, ops := range byAgent {
			a := reg.Lookup(name)
			if a == nil {
				return fmt.Errorf("reconcile override: adapter %q not registered", name)
			}
			rw := render.NewWriter(s, home, userHome, sc, projectRoot, name)
			if err := a.Apply(ops, rw); err != nil {
				return fmt.Errorf("reconcile override apply %s: %w", name, err)
			}
			for _, r := range rw.Reports() {
				fmt.Fprintf(w, "  backup: %s\n", r.String())
			}
			if err := render.RecordOpsState(s, userHome, name, sc, projectRoot, ops); err != nil {
				return err
			}
		}
		if err := state.Save(statePath, s); err != nil {
			return err
		}
		fmt.Fprintf(w, "override: applied %d item(s)\n", len(overrideOps))
	}

	if autoSkipped > 0 {
		fmt.Fprintf(w, "%d item(s) left unresolved; run `agentsync reconcile` interactively to handle them\n", autoSkipped)
	}
	return nil
}

// b2i returns 1 for true, 0 for false.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// collectItems builds the flat reconcile list from a rendered plan + state.
// userHome (the user's $HOME) is the HomeRelative base for state-key lookups.
func collectItems(plan render.RenderPlan, reg *adapter.Registry, s *state.Targets, sc adapter.Scope, projectRoot, userHome string) []reconcileItem {
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
					happlied := s.Keys[stateKeyKey(userHome, name, sc, projectRoot, op.Path, ptr)].SHA256
					hdest := hashAnyValue(getPointerValue(final, ptr))
					cls := drift.Classify(hsrc, happlied, hdest)
					items = append(items, reconcileItem{
						agentName:   name,
						op:          op,
						ptr:         ptr,
						cls:         cls,
						hsrc:        hsrc,
						happlied:    happlied,
						hdest:       hdest,
						scope:       sc,
						projectRoot: projectRoot,
					})
				}
			} else {
				if seen[op.Path] {
					continue
				}
				seen[op.Path] = true
				hsrc := hashContent(op.Content)
				happlied := s.Files[stateFileKey(userHome, name, sc, projectRoot, op.Path)].SHA256
				hdest := hashFile(op.Path)
				cls := drift.Classify(hsrc, happlied, hdest)
				items = append(items, reconcileItem{
					agentName:   name,
					op:          op,
					cls:         cls,
					hsrc:        hsrc,
					happlied:    happlied,
					hdest:       hdest,
					scope:       sc,
					projectRoot: projectRoot,
				})
			}
		}
	}
	return items
}

// requiresAction returns true for drift classes that need user (or auto) action.
// ForeignCollision is included: the very purpose of reconcile is to surface
// the pre-existing native files agentsync is about to back up and overwrite
// on the next apply. Hiding them meant `reconcile --auto-safe` reported
// "nothing to reconcile" on a populated machine, and the user only learned
// about the impending backups when the real apply ran.
func requiresAction(cls drift.Class) bool {
	switch cls {
	case drift.Drift, drift.Conflict, drift.OrphanDrifted, drift.ForeignCollision:
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
func writeBackItem(cmd *cobra.Command, home string, it reconcileItem) error {
	if it.ptr != "" {
		return writeBackKeyItem(cmd, home, it)
	}
	return writeBackFileItem(home, it)
}

// writeBackKeyItem handles key-level (merge-json-keys / merge-jsonc-keys) items.
// For MCP servers it reconstructs a source.MCPServer from the destination JSON
// and writes it with source.WriteMCP.
//
// For other key-level items (hooks, LSP, future shapes) write-back is not
// implemented and we return a clear error so the user is not silently lied
// to: the prior code returned nil and printed "write-back: <label>", giving
// the impression the hand-edit had been persisted when in fact it had not
// — the next apply would then destroy the user's edit.
func writeBackKeyItem(cmd *cobra.Command, home string, it reconcileItem) error {
	dest := readJSONFile(it.op.Path)
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
			// Server removed from dest; nothing to write back. We treat
			// this as a tombstone — the user wants the source dropped to
			// match — but persisting that requires source-side mutation
			// which isn't safe to do silently here. Surface as an error
			// so the user can pick [d]elete-source via a follow-up flow.
			return fmt.Errorf("destination dropped %s/%s — no write-back possible; remove the source manually or use [o]verride to push canonical back", parts[0], serverID)
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
		// The spec was reconstructed from the destination, where apply wrote any
		// ${secret:…} as resolved cleartext and which never carries source-only
		// fields (agents/enabled). capture.Capture re-references the secrets and
		// preserves those fields before writing — the same single boundary import
		// uses, so the two paths can't drift apart again.
		single := source.Canonical{MCPServers: []source.MCPServer{{ID: serverID, Server: spec}}}
		if _, err := capture.Capture(home, &single, capture.Opts{Warn: cmd.ErrOrStderr()}); err != nil {
			return err
		}
		return nil
	}
	// Unsupported pointer shape (hooks, lsp, …). DO NOT silently no-op —
	// the success message would be a lie.
	return fmt.Errorf("write-back for pointer %q is not implemented in v1; only /mcpServers/* items can be written back today — choose [o]verride to push canonical to the dest, or [i]gnore to suppress this item", it.ptr)
}

// writeBackFileItem handles file-level (replace strategy) items by copying
// the destination file back into the corresponding source location verbatim.
// This covers subagents, commands, memory, and skill files in v1.
//
// Two unsafe historical no-ops are now hard errors:
//   - SourceID == "" (no canonical home for this op)
//   - SourceID ends with "(multiple)" (the dest was assembled from N
//     source fragments; collapsing the whole dest back into ONE of them
//     would strand the others)
//
// Both used to return nil with a success message, hiding data loss.
func writeBackFileItem(home string, it reconcileItem) error {
	data, err := os.ReadFile(it.op.Path)
	if err != nil {
		return fmt.Errorf("read dest %s: %w", it.op.Path, err)
	}
	srcID := it.op.SourceID
	if srcID == "" {
		return fmt.Errorf("write-back for %s requires a single source-of-record; the rendering op has no SourceID (this happens for ad-hoc paths) — use [o]verride or [i]gnore", it.op.Path)
	}
	if strings.HasSuffix(srcID, "(multiple)") {
		return fmt.Errorf("write-back for %s is unsafe: the dest is the concatenation of multiple source fragments. Persisting the whole dest into one of them would strand the others. Edit the source fragments under %s/ directly, then apply", it.op.Path, home)
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
