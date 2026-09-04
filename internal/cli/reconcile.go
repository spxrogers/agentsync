package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
	"github.com/spxrogers/agentsync/internal/capture"
	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
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
	orphan      bool // owned-in-state whole-file dest no agent renders anymore
	// srcText/dstText carry the actual (masked-on-display) source and destination
	// content so the prompt/[d]iff can show a real value diff instead of only SHA
	// prefixes. hasText is false for items with no textual content — orphans,
	// and a whole-file destination the walk refused to read (a symlink not
	// read through, or a shape readDestBytes refuses) — which fall back to
	// the hash display.
	srcText string
	dstText string
	hasText bool
	// pluginOwner is the plugin id providing this component, empty for a
	// hand-authored one. Write-back is refused for a plugin-provided component:
	// see writeBackItem. Set for both shapes — whole-file items via the
	// component SourceID, key-level MCP/LSP items via pluginOwnerForKeyItem.
	pluginOwner string
}

// errDestDroppedServer signals that the destination no longer contains the MCP
// server a key-level write-back item targets — the user hand-deleted it in the
// native config. attemptWriteBack turns it into a guarded deletion of the
// canonical mcp/<id>.toml: a pure deletion carries no secret to re-reference, so
// os.Remove (the same primitive `mcp remove` uses) is the approved funnel, and it
// runs only when the user explicitly chose [w]rite-back for that item.
var errDestDroppedServer = errors.New("destination dropped server")

func newReconcileCmd() *cobra.Command {
	var (
		autoWB, autoOR, autoSafe bool
		agentsCSV                string
	)
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "interactively resolve drift between source and destination",
		Long: `reconcile walks the items whose DESTINATION has diverged from what agentsync
last wrote, and asks what to do with each: adopt the destination edit into your
canonical source ([w]rite-back), re-impose the source over it ([o]verride),
[s]kip, [i]gnore, [d]iff, or [q]uit. Bulk hotkeys (W/O/S) and the
--auto-writeback / --auto-override / --auto-safe flags exist for scripting.

Reach for reconcile when agentsync ALREADY manages a file and it has drifted.
Its sibling is 'agentsync import', which is for config agentsync does NOT manage
yet — it captures an agent's native config into the canonical source for the
first time. Rule of thumb: import adopts, reconcile resolves.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error {
				return reconcileRun(cmd, cmd.InOrStdin(), autoWB, autoOR, autoSafe, agentsCSV)
			})
		},
	}
	cmd.Flags().BoolVar(&autoWB, "auto-writeback", false, "auto-resolve drift by writing dest back to source")
	cmd.Flags().BoolVar(&autoOR, "auto-override", false, "auto-resolve drift by re-applying source to dest")
	cmd.Flags().BoolVar(&autoSafe, "auto-safe", false, "non-interactive: resolve nothing, report every item that needs a human")
	addAgentsFlag(cmd, &agentsCSV, "reconcile pass")
	markScopeAware(cmd)
	return cmd
}

func reconcileRun(cmd *cobra.Command, in io.Reader, autoWB, autoOR, autoSafe bool, agentsCSV string) error {
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
	c, sc, projectRoot, err := loadProjectedForScope(cmd, afero.NewOsFs(), home, false)
	if err != nil {
		return err
	}

	p, err := newPrinter(cmd)
	if err != nil {
		return err
	}

	// Redaction map for the prompt/[d]iff value display. The destination content
	// was written by a prior apply with ${secret:…} substituted to cleartext, so
	// showing the real value would leak it; mask every resolved value back to its
	// placeholder — the same fail-closed pattern diff.go uses. When any reference
	// cannot be resolved now (locked/absent backend), the dest cleartext cannot be
	// masked, so canMask is false and the display falls back to SHA prefixes rather
	// than risk printing a credential.
	secBackend := secrets.SelectBackend(c.Config.Secrets, home, userHome)
	envBackend := secrets.EnvBackend{}
	redact := secrets.CollectResolved(&c, secBackend, envBackend)
	canMask := len(secrets.UnresolvedSecretRefs(&c, secBackend, envBackend)) == 0

	statePath := filepath.Join(home, ".state", "targets.json")
	s, err := state.Load(statePath)
	if err != nil {
		return err
	}
	reg := registryFactory()
	var agents []string
	enabled := map[string]bool{}
	for name, ag := range c.Config.Agents {
		if ag.Enabled {
			agents = append(agents, name)
			enabled[name] = true
		}
	}
	// --agents narrows the pass, with the same parsing status/diff/apply use.
	if len(agents) > 0 {
		sel, aerr := selectAgents(cmd, agents, enabled, agentsCSV)
		if aerr != nil {
			return aerr
		}
		agents = sel
	}
	// reconcile hashes the rendered TEMPLATED source for drift; wrap as a
	// render-only Resolved without substituting (no backend needed).
	plan, err := render.Plan(secrets.ForRender(c), reg, agents, sc, projectRoot, s, userHome)
	if err != nil {
		return err
	}

	// Collect all items in order, then append orphaned whole-file dests
	// (owned in state, no longer rendered) for interactive delete/keep.
	// Provenance must come from the SAME canonical the plan rendered from: at
	// project scope every adapter renders the project-only overlay (`renderC =
	// *c.Project`), so tagging items from the merged canonical would let a
	// user-scope plugin claim a project component that merely shares its name.
	ownerSrc := c
	if sc == adapter.ScopeProject && c.Project != nil {
		ownerSrc = *c.Project
	}
	items, orphans := collectReconcileItems(plan, reg, s, sc, projectRoot, userHome, pluginProvidedSourceIDs(ownerSrc))
	items = append(items, orphans...)

	w := p.Out
	// stateDirty tracks orphan removals so we persist the pruned state at the end.
	stateDirty := false

	// No actionable items?
	needsPrompt := 0
	for _, it := range items {
		if requiresAction(it.cls) || it.orphan {
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
	// writeBackFailed counts [w]rite-back attempts that errored. A failed
	// write-back did NOT persist the user's dest edit, so the run must exit
	// non-zero rather than report success (a scripted `reconcile --auto-writeback
	// && deploy` must not proceed, and the next apply would clobber the edit).
	writeBackFailed := 0
	// writtenSources records, per canonical source file written this run, the
	// bytes that landed — so a SECOND write-back to the same file (a server/skill
	// that fanned out to multiple agents, each drifted differently) is detected
	// instead of silently last-writer-wins clobbering the first.
	writtenSources := map[string][]byte{}

	br := bufio.NewReader(in)

	for idx := range items {
		it := items[idx]
		if !requiresAction(it.cls) && !it.orphan {
			continue
		}

		// Orphans get a dedicated delete/keep prompt — deletion is never done
		// in an auto mode (too destructive to do non-interactively).
		if it.orphan {
			if autoWB || autoOR || autoSafe {
				fmt.Fprintf(w, "orphan left in place (run `agentsync reconcile` interactively to remove): %s\n", ui.Sanitize(it.op.Path))
				autoSkipped++
				continue
			}
			fmt.Fprintf(w, "\n%s  (orphan — source no longer produces this file)\n", ui.Sanitize(it.op.Path))
			// [k]eep is honest only for the kinds `apply` does NOT auto-reclaim.
			// Skills, subagents, and commands are reclaimed on the next apply, so
			// "keep" there means "keep until then" — say so rather than imply it
			// is permanent.
			//
			// This asks the KIND question (OrphanIsReclaimable), not the
			// readability one (OrphanDeleteWillProceed): a reclaimable orphan that
			// happens to be unreadable right now is retried on every subsequent
			// apply, so "until the next apply reclaims it" is still the truth for
			// it. Using the readability predicate here would print the permanent
			// wording for exactly the file apply keeps coming back to.
			if render.OrphanIsReclaimable(it.op.SourceID) {
				fmt.Fprintf(w, "  [r]emove (backs up first)  [k]eep (until the next apply reclaims it)  [q]uit\n  > ")
			} else {
				fmt.Fprintf(w, "  [r]emove (backs up first)  [k]eep  [q]uit\n  > ")
			}
		orphanPrompt:
			for {
				ch, readErr := readChar(br)
				if readErr != nil {
					goto done // EOF → finish (persist any pruned state)
				}
				switch ch {
				case 'r', 'R':
					fmt.Fprintf(w, "%c\n", ch)
					bk, berr := render.BackupFile(home, it.op.Path)
					if berr != nil {
						p.Fdiagf(w, ui.LevelError, "backup failed, NOT removing: %s", ui.Sanitize(berr.Error()))
						break orphanPrompt
					}
					if bk != "" {
						fmt.Fprintf(w, "  backup: %s\n", ui.Sanitize(bk))
					}
					if rmErr := os.Remove(it.op.Path); rmErr != nil && !os.IsNotExist(rmErr) { //nolint:forbidigo // the one NATIVE-destination delete outside DestWriter: interactive orphan removal, safe only because render.BackupFile succeeded just above
						p.Fdiagf(w, ui.LevelError, "remove failed: %s", ui.Sanitize(rmErr.Error()))
						break orphanPrompt
					}
					pruneStateFilesForPath(s, userHome, it.op.Path)
					stateDirty = true
					fmt.Fprintf(w, "  removed: %s\n", ui.Sanitize(it.op.Path))
					break orphanPrompt
				case 'k', 'K':
					fmt.Fprintf(w, "%c\n", ch)
					fmt.Fprintf(w, "  kept: %s\n", ui.Sanitize(it.op.Path))
					break orphanPrompt
				case 'q', 'Q':
					fmt.Fprintln(w, "quit")
					goto done
				default:
					// ignore unknown key, re-read
				}
			}
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
				switch {
				case it.cls == drift.ForeignCollision:
					fmt.Fprintf(w, "skipped (foreign-collision, would overwrite source): %s — resolve interactively\n", itemLabelDisp(it))
					autoSkipped++
					action = 's'
				case it.pluginOwner != "":
					// A plugin-provided component cannot be written back at all —
					// it has no canonical file of its own. That refusal is
					// STRUCTURAL and permanent, not a transient failure, so
					// letting it count as a write-back FAILURE would make
					// `reconcile --auto-writeback` exit non-zero on every run
					// forever, breaking any `reconcile && deploy` until the user
					// disables the plugin. Skip it like a foreign collision — the
					// same "cannot be resolved non-interactively" shape.
					fmt.Fprintf(w, "skipped (provided by plugin %q, no canonical file to write into): %s — use [o]verride, or change it upstream\n",
						ui.Sanitize(it.pluginOwner), itemLabelDisp(it))
					autoSkipped++
					action = 's'
				default:
					action = 'w'
				}
			case autoOR:
				action = 'o'
			case autoSafe:
				// auto-safe resolves nothing: everything that reaches this loop
				// needs a human.
				fmt.Fprintf(w, "skipped (needs manual review): %s (%s)\n", itemLabelDisp(it), it.cls)
				autoSkipped++
				action = 's'
			}
		}

		if action == 0 {
			// Interactive prompt.
			label := itemLabelDisp(it)
			fmt.Fprintf(w, "\n%s  (%s)\n", label, it.cls)
			renderItemValues(w, p, it, redact, canMask)
			fmt.Fprintf(w, "  [w]rite-back  [o]verride  [s]kip  [i]gnore  [d]iff  [q]uit\n  > ")

		prompt:
			for {
				ch, readErr := readChar(br)
				if readErr != nil {
					// EOF → finish gracefully, but reach `done:` so any queued
					// [o]verride ops are applied and pruned/dirty state is flushed
					// (a bare `return nil` here dropped both — issue #171).
					goto done
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
						//
						// The count is the TRUE blast radius of this bulk action:
						// the items from HERE forward it will actually act on —
						// remaining actionable, non-orphan items (orphans have their
						// own r/k prompt and are never swept by a bulk choice). The
						// prior count walked the WHOLE queue including items already
						// handled, overstating the reach.
						remaining := 0
						for j := idx; j < len(items); j++ {
							if requiresAction(items[j].cls) && !items[j].orphan {
								remaining++
							}
						}
						lower := ch | 0x20
						fmt.Fprintf(w, "%c\n", ch)
						fmt.Fprintf(w, "  apply '%c' to all %d remaining items? [y/N] ", lower, remaining)
						confirm, readErr := readChar(br)
						if readErr != nil {
							goto done // EOF mid-confirm → flush queued overrides + state (issue #171)
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
					printItemDiff(w, p, it, redact, canMask)
					fmt.Fprintf(w, "  [w]rite-back  [o]verride  [s]kip  [i]gnore  [d]iff  [q]uit\n  > ")
				default:
					// ignore unknown key
				}
			}
		}

		switch action {
		case 'w':
			// write-back: persist destination value into the canonical source.
			if attemptWriteBack(cmd, p, w, home, it, canonicalHookEvents(c), writtenSources) {
				writeBackFailed++
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
			fmt.Fprintf(w, "  ignored: %s\n", itemLabelDisp(it))
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
		stateDirty = false // override save already persisted the pruned state
		fmt.Fprintf(w, "override: applied %d item(s)\n", len(overrideOps))
	}

	// Persist state if orphan removals pruned ownership and the override block
	// above didn't already save.
	if stateDirty {
		if err := state.Save(statePath, s); err != nil {
			return err
		}
	}

	if autoSkipped > 0 {
		fmt.Fprintf(w, "%d item(s) left unresolved; run `agentsync reconcile` interactively to handle them\n", autoSkipped)
	}
	// A write-back that errored did NOT persist the edit; surface it as a
	// non-zero exit so callers (and scripts) don't treat the sync as complete.
	if writeBackFailed > 0 {
		return fmt.Errorf("reconcile: %d item(s) failed to write back", writeBackFailed)
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

// pluginProvidedSourceIDs maps each canonical SourceID a plugin provides to the
// providing plugin's id, for the projected canonical c. Skills contribute one
// entry per file in the directory (SKILL.md plus every bundled resource), since
// each renders its own op with its own SourceID.
//
// It is reconcile's counterpart to import's pluginProvided(): the same "don't
// capture agentsync's own output back into the canonical source" rule, keyed the
// way each caller can match it. Reconcile works from the PROJECTED canonical, so
// provenance is already on the components and no re-projection is needed; import
// works from the agent's NATIVE ingest, which carries no provenance at all, so it
// has to project separately and match by component name.
func pluginProvidedSourceIDs(c source.Canonical) map[string]string {
	out := map[string]string{}
	// No co-declaration override here, deliberately. Import needs one for HOOKS,
	// because source.WriteHooks replaces the whole event file and skipping a
	// handler the user also declares would ERASE it (see pluginProvided,
	// internal/cli/import.go). This map registers no hook keys at all — hook
	// write-back is unimplemented — and for every other kind the override would
	// be harmful rather than absent: capturing a component the plugin also
	// provides makes the two DIVERGE, and every later mutating load then fails in
	// checkProjectedConflicts. Refusing the write-back costs nothing by
	// comparison, so a plugin-provided key stays the plugin's.
	claim := func(key, plugin string) {
		if plugin != "" {
			out[key] = plugin
		}
	}
	// MCP/LSP servers are keyed under BOTH forms, because the two adapters
	// families render them differently:
	//
	//   "<kind>/<id>"       — the key-merge shape (most adapters fold every
	//                         server into one config file, so the op carries the
	//                         section-wide SourceID "mcp/* (multiple)" and only
	//                         the item's JSON pointer identifies the server).
	//                         Resolved by pluginOwnerForKeyItem.
	//   "<kind>/<id>.toml"  — the whole-file shape. Continue renders ONE FILE PER
	//                         SERVER (.continue/mcpServers/<id>.yaml) with the
	//                         canonical SourceID "mcp/<id>.toml", so its items go
	//                         through the SourceID lookup like a subagent would.
	//                         Keying only the bare form left that path unguarded.
	for _, s := range c.MCPServers {
		claim("mcp/"+s.ID, s.Plugin)
		claim(filepath.ToSlash(filepath.Join("mcp", s.ID+".toml")), s.Plugin)
	}
	for _, s := range c.LSPServers {
		claim("lsp/"+s.ID, s.Plugin)
		claim(filepath.ToSlash(filepath.Join("lsp", s.ID+".toml")), s.Plugin)
	}
	for _, sk := range c.Skills {
		claim(filepath.ToSlash(filepath.Join("skills", sk.Name, "SKILL.md")), sk.Plugin)
		for _, f := range sk.Files {
			claim(filepath.ToSlash(filepath.Join("skills", sk.Name, f.Path)), sk.Plugin)
		}
	}
	for _, sa := range c.Subagents {
		claim(filepath.ToSlash(source.SubagentSourceID(sa.Name)), sa.Plugin)
	}
	for _, cm := range c.Commands {
		claim(filepath.ToSlash(filepath.Join("commands", cm.Name+".md")), cm.Plugin)
	}
	return out
}

// pluginOwnerForKeyItem resolves the plugin owning the MCP/LSP server a
// key-level item points at, or "" for a hand-declared one.
//
// The component KIND comes from the op's SourceID, not from the pointer's root
// key, for two independent reasons:
//
//   - Root keys are per-agent DATA, not a fixed set. Alongside /mcpServers
//     (claude, cursor, gemini, …), /mcp (opencode) and /mcp_servers (codex), the
//     generic tier carries `RootKey` in a table that grows with every agent added
//     (/context_servers, /servers, /amp.mcpServers). A hand-maintained allowlist
//     silently drops the refusal for each one someone forgets to add.
//   - Guessing the kind by probing the id against both maps is WRONG, not merely
//     imprecise. A plugin shipping an LSP server whose id is a common MCP name
//     ("github", "postgres") would make an /mcpServers/github pointer resolve to
//     that plugin, refusing write-back of the user's OWN hand-declared MCP server
//     and blaming a plugin that does not own it. Over-refusal here IS the harm.
//
// Every key-merge op carries a section-wide SourceID naming its kind —
// "mcp/* (multiple)", "hooks/* (multiple)" — so the kind is already unambiguous
// at the call site.
//
// The lsp branch is likewise unreachable TODAY — no adapter renders LSP servers
// in v1 (every one reports them as a Skip), so no op carries an "lsp/" SourceID
// and the lsp keys pluginProvidedSourceIDs registers are never looked up. It is
// kept because it is symmetric, free, and correct the day an adapter does render
// them; without it, that adapter would ship with the refusal silently absent.
//
// Hooks resolve to "" by construction: hook write-back is not implemented
// (writeBackKeyItem errors for every non-MCP pointer), so pluginProvidedSourceIDs
// registers no hook keys for this lookup to find. Import is where plugin hooks
// are filtered, at handler granularity. Should hook write-back ever be
// implemented, this returns "" rather than silently permitting the capture — the
// empty map is the thing to fix, and it is one place.
//
// Pointer segments are JSON-pointer encoded, so ~1/~0 are decoded first
// (RFC 6901 §3) — an id containing '/' would otherwise never match.
func pluginOwnerForKeyItem(sourceID, ptr string, owners map[string]string) string {
	var kind string
	switch {
	case strings.HasPrefix(sourceID, "mcp/"):
		kind = "mcp"
	case strings.HasPrefix(sourceID, "lsp/"):
		kind = "lsp"
	default:
		return "" // hooks, or a shape with no per-entry provenance
	}
	parts := strings.SplitN(strings.TrimPrefix(ptr, "/"), "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return owners[kind+"/"+unescapeJSONPointer(parts[1])]
}

// unescapeJSONPointer decodes one JSON-pointer reference token (RFC 6901 §3):
// "~1" is '/' and "~0" is '~'. Order matters — ~0 must be decoded last, or
// "~01" would wrongly become "/" instead of "~1".
func unescapeJSONPointer(tok string) string {
	return strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
}

// collectReconcileItems builds reconcile's flat item list from a rendered plan
// + state with ONE walkPlanItems call. It answers two slices so the caller can
// keep reconcile's prompt order — ALL rendered items, then ALL orphans — rather
// than the walk's per-agent interleaving: items holds every whole-file / key
// item in walk order (agents in registry order, ops in plan order, merged keys
// sorted); orphans holds the whole-file dests agentsync still OWNS in state but
// NO enabled agent renders anymore (the source component was removed), offered
// for interactive delete/keep.
//
// userHome (the user's $HOME) is the HomeRelative base for state-key lookups.
// pluginOwners (pluginProvidedSourceIDs) tags each item whose component comes
// from a plugin, so write-back can refuse it.
//
// The walk's orphan set is per agent and unfiltered; reconcile narrows it: a
// path that ANY enabled agent still renders is excluded — never offer to
// delete a file another agent depends on (the shared-skill case) — and the
// rest is deduped by path so a file owned by two agents is prompted once.
func collectReconcileItems(plan render.RenderPlan, reg *adapter.Registry, s *state.Targets, sc adapter.Scope, projectRoot, userHome string, pluginOwners map[string]string) (items, orphans []reconcileItem) {
	rendered := map[string]bool{}
	for _, name := range reg.Names() {
		res, ok := plan.PerAgent[name]
		if !ok {
			continue
		}
		for _, op := range res.Ops {
			if op.Action != adapter.ActionWrite {
				continue
			}
			if render.IsKeyMerge(op.MergeStrategy) {
				continue
			}
			rendered[op.Path] = true
		}
	}
	seen := map[string]bool{}
	walk := planWalk{
		plan: plan, agents: reg.Names(), state: s, userHome: userHome, scope: sc, projectRoot: projectRoot,
		includeOrphans: true,
		withText:       true,
	}
	for _, it := range walkPlanItems(walk) {
		ri := reconcileItem{
			agentName:   it.agent,
			op:          it.op,
			ptr:         it.ptr,
			cls:         it.cls,
			hsrc:        it.hsrc,
			happlied:    it.happlied,
			hdest:       it.hdest,
			scope:       sc,
			projectRoot: projectRoot,
			orphan:      it.orphan,
		}
		if it.orphan {
			if rendered[it.op.Path] || seen[it.op.Path] {
				continue
			}
			seen[it.op.Path] = true
			orphans = append(orphans, ri)
			continue
		}
		// A refused destination has no text to show: the SHA display, not the
		// whole source rendered against an "empty" destination.
		ri.srcText, ri.dstText, ri.hasText = it.srcText, it.dstText, !it.destSymlinkRefused() && !it.destShapeRefused()
		if it.ptr != "" {
			ri.pluginOwner = pluginOwnerForKeyItem(it.op.SourceID, it.ptr, pluginOwners)
		} else {
			ri.pluginOwner = pluginOwners[filepath.ToSlash(it.op.SourceID)]
		}
		items = append(items, ri)
	}
	return items, orphans
}

// pruneStateFilesForPath removes every agent's Files state entry for a single
// dest path (after the user removes an orphan). The path is its own field of the
// typed state key, so the match is exact for any path — including one containing
// ':', which the previous suffix match only happened to get right.
func pruneStateFilesForPath(s *state.Targets, userHome, absPath string) {
	portable := paths.HomeRelative(userHome, absPath)
	for key := range s.Files {
		if key.Path == portable {
			delete(s.Files, key)
		}
	}
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

// itemLabelDisp is itemLabel sanitized for TERMINAL DISPLAY only. itemLabel's raw
// value is written verbatim to ignore.toml (appendIgnore), so the path/pointer —
// which embeds a config-derived component name/id that can hold control bytes —
// must be sanitized at the display boundary, never in itemLabel itself
// (issue #93/#171).
func itemLabelDisp(it reconcileItem) string { return ui.Sanitize(itemLabel(it)) }

func shortVal(hash string) string {
	if hash == "" {
		return "<absent>"
	}
	if len(hash) > 16 && isHexDigest(hash) {
		return hash[:16] + "..."
	}
	return hash // a sentinel (the symlink and shape tokens) is shown whole
}

func isHexDigest(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// renderItemValues shows the differing source/destination CONTENT for an item as
// a masked text diff, so a destructive [w]rite-back/[o]verride is an informed
// choice rather than a blind pick between two SHA-256 prefixes. It reuses the
// same masking (secrets.MaskResolved over the resolved-value map) and text-diff
// renderer as `agentsync diff`, so a resolved secret is shown as its ${secret:…}
// placeholder, never in cleartext.
//
// It falls back to the SHA-prefix display when there is no textual content
// (orphans) OR when we cannot safely mask (an unresolved reference means the
// destination cleartext can't be redacted) — never print a value we can't mask.
func renderItemValues(w io.Writer, p *ui.Printer, it reconcileItem, redact map[string]string, canMask bool) {
	if !it.hasText || !canMask {
		fmt.Fprintf(w, "  source:      %s\n", shortVal(it.hsrc))
		fmt.Fprintf(w, "  destination: %s\n", shortVal(it.hdest))
		return
	}
	src := secrets.MaskResolved(it.srcText, redact)
	dst := secrets.MaskResolved(it.dstText, redact)
	fmt.Fprintf(w, "  %s\n", p.Red("--- source"))
	fmt.Fprintf(w, "  %s\n", p.Green("+++ dest"))
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(dst, src, false)
	for _, line := range strings.Split(renderDiffText(p, diffs), "\n") {
		fmt.Fprintf(w, "  %s\n", line)
	}
}

func printItemDiff(w io.Writer, p *ui.Printer, it reconcileItem, redact map[string]string, canMask bool) {
	renderItemValues(w, p, it, redact, canMask)
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
// attemptWriteBack writes one item back and guards against silent
// last-writer-wins when a single reconcile run writes the SAME canonical source
// file from more than one agent. A server/skill that fans out to claude AND
// opencode produces two drift items pointing at one source file
// (mcp/<id>.toml, …); if the user edited the two destinations DIFFERENTLY, the
// second capture would clobber the first with no warning and leave the first
// agent stuck in `conflict`. So: if a prior write this run produced this source
// file and this write changes it, revert to the first write and report a
// conflict (counted as a failure → non-zero exit) for the user to resolve.
// Returns true on failure/conflict.
func attemptWriteBack(cmd *cobra.Command, p *ui.Printer, w io.Writer, home string, it reconcileItem, hookEvents []string, writtenSources map[string][]byte) bool {
	srcFile := itemSourceFile(home, it, hookEvents)
	var prior []byte
	priorWritten := false
	if srcFile != "" {
		prior, priorWritten = writtenSources[srcFile]
	}
	werr := writeBackItem(cmd, home, it)
	if errors.Is(werr, errDestDroppedServer) {
		// Tombstone: the user deleted this MCP server from the native config, and
		// chose [w]rite-back to persist that. A pure deletion carries no secret, so
		// remove the canonical mcp/<id>.toml directly (the same os.Remove primitive
		// `mcp remove` uses) rather than routing an empty spec through capture.
		return removeDroppedSource(p, w, home, it, srcFile, prior, priorWritten, writtenSources)
	}
	if werr != nil {
		p.Fdiagf(w, ui.LevelError, "write-back: %s", ui.Sanitize(werr.Error()))
		return true
	}
	if srcFile != "" {
		if after, rerr := os.ReadFile(srcFile); rerr == nil {
			if priorWritten && string(prior) != string(after) {
				revertSource(srcFile, prior) // undo this write; keep the first
				rel, _ := filepath.Rel(home, srcFile)
				fmt.Fprintf(w, "  conflict: %s — another agent drifted the same source (%s) to a different "+
					"value this run; kept the first write and skipped this one. Make the agents agree, or "+
					"reconcile one at a time, then re-run.\n", itemLabelDisp(it), ui.Sanitize(rel))
				return true
			}
			writtenSources[srcFile] = after
		}
	}
	fmt.Fprintf(w, "  write-back: %s\n", itemLabelDisp(it))
	return false
}

// removeDroppedSource persists a destination-side MCP-server deletion by removing
// the canonical mcp/<id>.toml. It guards the same multi-agent fan-out
// attemptWriteBack's content path does: if a PRIOR write this run already
// produced this source file (another agent still renders the server and wrote its
// drifted value), deleting it would strand that write — so refuse and keep it,
// reporting a conflict (non-zero exit). On success it records a deletion sentinel
// (nil bytes) in writtenSources so a LATER content write-back to the same file
// this run is likewise flagged rather than silently resurrecting it. Returns true
// on failure/conflict.
func removeDroppedSource(p *ui.Printer, w io.Writer, home string, it reconcileItem, srcFile string, prior []byte, priorWritten bool, writtenSources map[string][]byte) bool {
	if srcFile == "" {
		p.Fdiagf(w, ui.LevelError, "write-back: %s — cannot locate the canonical source file to delete", itemLabelDisp(it))
		return true
	}
	// Defense-in-depth: srcFile derives from a native-config-supplied server id;
	// never let a traversal segment escape ~/.agentsync into an arbitrary unlink.
	if !withinDir(home, srcFile) {
		p.Fdiagf(w, ui.LevelError, "write-back: %s — refusing to delete outside the source tree", itemLabelDisp(it))
		return true
	}
	if priorWritten && len(prior) > 0 {
		rel, _ := filepath.Rel(home, srcFile)
		p.Fdiagf(w, ui.LevelError, "conflict: %s — another agent wrote this source (%s) this run, so it is still in use; "+
			"not deleting it. Make the agents agree (remove it from every native config), then re-run.",
			itemLabelDisp(it), ui.Sanitize(rel))
		return true
	}
	if rmErr := os.Remove(srcFile); rmErr != nil && !os.IsNotExist(rmErr) { //nolint:forbidigo // removes a canonical source file under ~/.agentsync (withinDir-guarded above), not a native destination
		p.Fdiagf(w, ui.LevelError, "write-back: remove %s: %s", itemLabelDisp(it), ui.Sanitize(rmErr.Error()))
		return true
	}
	writtenSources[srcFile] = nil // deletion sentinel for a later same-file write
	rel, _ := filepath.Rel(home, srcFile)
	fmt.Fprintf(w, "  write-back: removed source %s (destination dropped %s)\n", ui.Sanitize(rel), itemLabelDisp(it))
	return false
}

// revertSource undoes this run's write to srcFile, restoring the FIRST writer's
// bytes. When that first write was itself a deletion (prior is empty), restoring
// it means deleting the file again rather than writing a 0-byte one.
func revertSource(srcFile string, prior []byte) {
	if len(prior) == 0 {
		_ = os.Remove(srcFile) //nolint:forbidigo // reverts this run's own canonical-source write, not a native destination
		return
	}
	_ = iox.AtomicWrite(srcFile, prior, 0o644)
}

// itemSourceFile returns the absolute canonical source file a write-back item
// targets, so two agents writing the same component can be detected. Both the
// claude (/mcpServers/<id>) and opencode (/mcp/<id>) pointers map to the SAME
// mcp/<id>.toml. Returns "" for items with no single source-of-record.
func itemSourceFile(home string, it reconcileItem, hookEvents []string) string {
	if it.ptr == "" {
		if it.op.SourceID == "" || strings.HasSuffix(it.op.SourceID, "(multiple)") {
			return ""
		}
		return filepath.Join(home, it.op.SourceID)
	}
	return pointerSourceFile(home, it.agentName, it.ptr, hookEvents)
}

// pointerSourceFile maps a NATIVE key-merge JSON pointer back to the canonical
// source file that produced it. It is shared by reconcile's write-back and
// `agentsync explain <path>#<pointer>`, which is the feature that made the
// renamed-hook-event translation below load-bearing rather than latent.
//
// agent names the rendering adapter, which matters for hooks: a RENAMING agent
// (gemini `BeforeTool`, cursor `preToolUse`) spells the pointer segment
// natively, so it must be translated back through adapter.HookEventNamer.
// Without that, `/hooks/BeforeTool` resolved to a nonexistent
// `hooks/BeforeTool.toml` — a wrong answer for explain and, once hook
// write-back is implemented, a write to the wrong path for reconcile.
//
// canonicalEvents is the set of canonical hook events to invert against: the
// events the loaded canonical actually carries. Every rendered hook pointer
// comes from one of them, so scanning that set is both sufficient and free of a
// second, drift-prone enumeration of the event vocabulary.
//
// Returns "" when the pointer names no single canonical source-of-record.
func pointerSourceFile(home, agent, ptr string, canonicalEvents []string) string {
	parts := strings.SplitN(strings.TrimPrefix(ptr, "/"), "/", 3)
	if len(parts) < 2 || parts[1] == "" {
		return ""
	}
	switch parts[0] {
	case "mcpServers", "mcp", "mcp_servers":
		return filepath.Join(home, "mcp", parts[1]+".toml")
	case "lspServers", "lsp":
		return filepath.Join(home, "lsp", parts[1]+".toml")
	case "hooks":
		event, ok := canonicalHookEvent(agent, parts[1], canonicalEvents)
		if !ok {
			return ""
		}
		return source.HookPath(home, event)
	}
	return ""
}

// canonicalHookEvent inverts an agent's native hook-event spelling back to the
// canonical one. An adapter that renders hooks under the canonical name (claude,
// codex — no HookEventNamer) passes the segment through. A renaming adapter is
// inverted by asking it for the native spelling of each candidate canonical
// event; ok is false when nothing matches.
func canonicalHookEvent(agent, native string, canonicalEvents []string) (string, bool) {
	namer, ok := registryFactory().Lookup(agent).(adapter.HookEventNamer)
	if !ok {
		return native, true
	}
	for _, canonical := range canonicalEvents {
		if got, has := namer.NativeHookEvent(canonical); has && got == native {
			return canonical, true
		}
	}
	return "", false
}

// canonicalHookEvents lists the canonical hook events the loaded model carries
// — the inversion candidates for canonicalHookEvent. Deliberately derived from
// the model rather than from a second hard-coded event vocabulary: every hook
// pointer a render emits comes from one of these, and a standalone list would
// be one more thing to keep in sync with the adapters.
func canonicalHookEvents(c source.Canonical) []string {
	if len(c.Hooks) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(c.Hooks))
	for _, h := range c.Hooks {
		e := h.Event.Unverified()
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

func writeBackItem(cmd *cobra.Command, home string, it reconcileItem) error {
	// A plugin-provided component has no canonical file of its own: it is
	// re-derived from the plugin cache on every load. Writing the destination
	// back would MINT one under ~/.agentsync/, and the next load would hold two
	// definitions — the captured copy and the plugin's own projection. For a
	// name-keyed component that is two writes to one destination path; for an
	// MCP/LSP server it is a same-id divergence the moment the plugin updates,
	// which checkProjectedConflicts then refuses. Either way the user is stuck.
	//
	// The check sits at the dispatch waist so BOTH shapes are covered — the
	// whole-file path (skills/subagents/commands) and the key-merge path
	// (MCP/LSP servers) — rather than being duplicated in each and drifting.
	// [o]verride restores the plugin's version; the edit belongs upstream.
	if it.pluginOwner != "" {
		what := it.op.Path
		if it.ptr != "" {
			what = fmt.Sprintf("%s (%s)", it.op.Path, ui.Sanitize(it.ptr))
		}
		return fmt.Errorf("write-back for %s is unsafe: this component is projected from the plugin %q, "+
			"so it has no canonical file to write into. Capturing it would create one that collides with "+
			"the plugin's own on the next apply. Use [o]verride to restore the plugin's version, change it "+
			"upstream, or run `agentsync plugin disable %q` to stop projecting it",
			what, it.pluginOwner, it.pluginOwner)
	}
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
	dest := readDestFile(it.op.MergeStrategy, it.op.Path)
	// Expected ptr shape: /mcpServers/<serverID>/... (claude), /mcp/<serverID>/...
	// (opencode), or /mcp_servers/<serverID>/... (codex). The container key also
	// tells us the dest shape: Claude's `mcpServers` value matches the canonical
	// model 1:1, but OpenCode's `mcp` and Codex's `mcp_servers` values are NATIVE
	// shapes (OpenCode: command as a string array, `environment` not `env`, type
	// local|remote; Codex: `http_headers`, url-implies-http), so they must be
	// translated through the adapter's inverse-of-Render rather than unmarshaled.
	parts := strings.SplitN(strings.TrimPrefix(it.ptr, "/"), "/", 3)
	if len(parts) >= 2 && (parts[0] == "mcpServers" || parts[0] == "mcp" || parts[0] == "mcp_servers") {
		topKey := parts[0]
		serverID := parts[1]
		// serverID comes from the (native-config-derived) JSON pointer and can
		// hold control bytes; keep the raw value for lookup/canonical ID but use
		// a sanitized copy in any error surfaced to the terminal (issue #93/#171).
		serverIDDisp := ui.Sanitize(serverID)
		mcpServers, _ := dest[topKey].(map[string]any)
		if mcpServers == nil {
			return fmt.Errorf("%s not found in destination", topKey)
		}
		specRaw, ok := mcpServers[serverID]
		if !ok {
			// Server removed from dest: the user deleted it from the native
			// config and chose [w]rite-back to persist that. Signal a tombstone;
			// attemptWriteBack deletes the canonical mcp/<id>.toml through the
			// approved os.Remove funnel (a pure deletion carries no secret), with
			// the multi-agent fan-out guard.
			return errDestDroppedServer
		}
		var spec source.MCPServerSpec
		switch topKey {
		case "mcp":
			// OpenCode native shape → canonical, via the single adapter translator.
			rawMap, _ := specRaw.(map[string]any)
			if rawMap == nil {
				return fmt.Errorf("opencode mcp spec %s is not an object", serverIDDisp)
			}
			spec = opencode.IngestMCPSpec(rawMap)
		case "mcp_servers":
			// Codex native shape (TOML-decoded map) → canonical.
			rawMap, _ := specRaw.(map[string]any)
			if rawMap == nil {
				return fmt.Errorf("codex mcp spec %s is not an object", serverIDDisp)
			}
			spec = codex.IngestMCPSpec(rawMap)
		default:
			// Claude's mcpServers value matches the canonical model 1:1.
			specBytes, err := json.Marshal(specRaw)
			if err != nil {
				return fmt.Errorf("marshal mcp spec %s: %w", serverIDDisp, err)
			}
			if err := json.Unmarshal(specBytes, &spec); err != nil {
				return fmt.Errorf("unmarshal mcp spec %s: %w", serverIDDisp, err)
			}
			// json.Unmarshal into the struct drops unmodeled native keys; capture
			// them into Extra so write-back is not field-lossy (matching ingest and
			// the opencode/codex branches above).
			if rawMap, ok := specRaw.(map[string]any); ok {
				spec.Extra = claude.ExtraNativeKeys(rawMap, "type", "command", "args", "env", "url", "headers")
			}
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
	return fmt.Errorf("write-back for pointer %q is not implemented in v1; only /mcpServers/* (claude), /mcp/* (opencode) and /mcp_servers/* (codex) items can be written back today — choose [o]verride to push canonical to the dest, or [i]gnore to suppress this item", it.ptr)
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
	readPath, why := destReadPath(it.op.Path)
	switch why {
	case symlinkRefusedByEnv:
		// The drift walk classified this item without reading through the
		// link, so [w] must not quietly capture through it. Neither symlink
		// arm's advice suggests [o]verride: Writer.Write's convergence read
		// follows the link, and its mode arm chmods the TARGET through it
		// before the symlink policy is consulted (#248).
		return fmt.Errorf("read dest %s: destination is a symlink agentsync is not reading through — "+
			"set %s=1 (for apply, status, diff, reconcile and explain) to read and write through the link, replace the link with a "+
			"regular file, or [i]gnore to suppress this item", it.op.Path, iox.AllowSymlinkDestEnv)
	case symlinkUnresolvable:
		return fmt.Errorf("read dest %s: destination is a symlink whose target cannot be resolved "+
			"(dangling, loop, or unreadable; apply refuses it too) — fix or replace the link, or [i]gnore "+
			"to suppress this item", it.op.Path)
	}
	data, err := readDestBytes(readPath)
	if err != nil {
		// Named next steps, like this function's other refusals: the user is
		// mid-prompt with a keystroke to choose, and "read dest X: not a regular
		// file" alone does not tell them which one gets them unstuck.
		//
		// This arm's advice deliberately omits [o]verride, as the symlink
		// arms' does, unlike the absent arm below. It re-applies through
		// render.Writer.Write, whose convergence read is not
		// shape-guarded, so on this exact item it does not fail — it HANGS
		// (measured: `reconcile --auto-override` rc=124).
		// An earlier version of this message recommended it, which walked the
		// user out of a clean refusal and into an unbounded wedge. Restore that
		// suggestion only once #241 is fixed.
		if errors.Is(err, errDestNotRegular) {
			return fmt.Errorf("read dest %s: %w — remove or replace the non-regular file at "+
				"that path and re-run, or [i]gnore to suppress this item", it.op.Path, err)
		}
		// Any other read failure keeps the peers' remedy set. The common one is
		// an ABSENT destination — the user deleted a managed file, which is
		// itself drift — and there [o]verride is both safe and usually the fix:
		// Writer.Write's convergence read gets ENOENT and falls straight
		// through to the write. Leaving it out of the advice is only right for
		// the non-regular case above.
		return fmt.Errorf("read dest %s: %w — use [o]verride to restore it from canonical, "+
			"or [i]gnore to suppress this item", it.op.Path, err)
	}
	srcID := it.op.SourceID
	if srcID == "" {
		return fmt.Errorf("write-back for %s requires a single source-of-record; the rendering op has no SourceID (this happens for ad-hoc paths) — use [o]verride or [i]gnore", it.op.Path)
	}
	if strings.HasSuffix(srcID, "(multiple)") {
		return fmt.Errorf("write-back for %s is unsafe: the dest is the concatenation of multiple source fragments. Persisting the whole dest into one of them would strand the others. Edit the source fragments under %s/ directly, then apply", it.op.Path, home)
	}
	// Memory is fragment-aware. If apply wrote fragment markers, reverse them
	// into AGENTS.md + the fragment files instead of writing the expanded dest
	// verbatim (which would put markers in the source and never restore the
	// fragments). With no markers but a fragment-composed source, writing back
	// would inline every @import and orphan the fragments — refuse.
	if filepath.ToSlash(srcID) == "memory/AGENTS.md" {
		// The rendered memory carries the agentsync managed-file banner
		// (RenderManagedMemory). Strip it before anything else so it never enters
		// the canonical source — both the fragment-marker reversal AND the plain
		// verbatim fall-through below then operate on banner-free content.
		data = []byte(source.StripManagedBanner(string(data)))
		mem, hadMarkers, cerr := source.CollapseMemoryMarkers(string(data))
		switch {
		case cerr != nil:
			return fmt.Errorf("write-back for %s is unsafe: memory fragment markers could not be reversed (%w); reconcile memory/ by hand, then apply", it.op.Path, cerr)
		case hadMarkers:
			return source.WriteMemory(home, mem)
		case source.MemoryHasFragments(home):
			return fmt.Errorf("write-back for %s is unsafe: canonical memory is composed of fragments/ and the dest has no reversible markers. Persisting it would inline every @import and orphan the fragments. Edit the fragments under %s/memory/ directly, then apply", it.op.Path, home)
		}
		// No fragments: fall through to the plain verbatim write below.
	}
	dest := filepath.Join(home, srcID)
	// Defense-in-depth: srcID derives from a component Name, and AtomicWrite
	// does no containment check. A "../" segment in the name would let this
	// reverse (dest→source) write escape ~/.agentsync and clobber an arbitrary
	// file. The forward import boundary (source.Write*) is fenced with
	// validateComponentID; mirror that here. Every name reaching this path is
	// sanitized today (loader basenames, projection's validateProjectedName),
	// so this guards future callers, not a live exploit.
	if !withinDir(home, dest) {
		return fmt.Errorf("write-back for %s escapes the source tree %s (SourceID %q has a traversal segment); refusing", it.op.Path, home, srcID)
	}
	// Preserve the rendering op's mode so an executable bundled skill script
	// (scripts/*.sh, scripts/*.py) keeps its +x bit through write-back. Text
	// components (subagents/commands/memory) render with Mode 0o644, so this is
	// a no-op for them; only bundled skill files carry a non-default mode.
	mode := os.FileMode(it.op.Mode)
	if mode == 0 {
		mode = 0o644
	}
	return iox.AtomicWrite(dest, data, mode)
}

// withinDir reports whether path is dir itself or sits lexically inside it,
// after Clean. Used to bound dest→source write-backs to ~/.agentsync.
func withinDir(dir, path string) bool {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

// appendIgnore appends the label to ~/.agentsync/ignore.toml (best-effort).
func appendIgnore(home, label string) error {
	p := filepath.Join(home, "ignore.toml")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644) //nolint:forbidigo // appends to ~/.agentsync/ignore.toml (canonical source), not a native destination
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "ignore = %q\n", strings.TrimSpace(label))
	return err
}
