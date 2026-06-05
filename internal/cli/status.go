package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
	"github.com/tailscale/hujson"
)

// statusItem is one tracked file or merged key and its drift classification.
type statusItem struct {
	Path string `json:"path"`
	// Pointer is the RFC-6901 JSON pointer for a key-merge item; empty for a
	// whole-file item.
	Pointer string `json:"pointer,omitempty"`
	Class   string `json:"class"`
}

// statusAgent groups one agent's tracked items.
type statusAgent struct {
	Agent string       `json:"agent"`
	Items []statusItem `json:"items"`
}

// statusModel is the full drift report, rendered either as the formatted
// dashboard or, under --json, verbatim. Summary tallies items by drift class.
type statusModel struct {
	Agents  []statusAgent  `json:"agents"`
	Summary map[string]int `json:"summary"`
}

func newStatusCmd() *cobra.Command {
	var (
		scopeFlag   string
		projectFlag string
		jsonOut     bool
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "report drift across registered agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd)
			if err != nil {
				return err
			}
			home := paths.AgentsyncHome(paths.OSEnv{})
			// Load WITH the plugin cache so drift classification sees the
			// same plugin-projected components `apply` writes; source.Load
			// alone would report plugin-managed files/keys as untracked.
			userHome := paths.HomeDir(paths.OSEnv{})
			c, sc, projectRoot, err := loadProjectedForScope(cmd, afero.NewOsFs(), home, scopeFlag, projectFlag, true)
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
			if len(agents) == 0 {
				if jsonOut {
					emitStatusWarnings(p, c, reg, s, agents)
					return emitJSON(p.Out, statusModel{Agents: []statusAgent{}, Summary: map[string]int{}})
				}
				fmt.Fprintln(p.Out, "no agents enabled; run `agentsync agent add claude` (or opencode)")
				emitStatusWarnings(p, c, reg, s, agents)
				return nil
			}
			// apply WRITES (and RecordOpsState HASHES) the secret-RESOLVED
			// content, so status must hash the resolved source too — otherwise a
			// synced ${secret:…}/${env:…} item compares templated-vs-resolved and
			// classifies as phantom "pending" forever. Resolve like apply; fall
			// back to the templated render only when the backend is unavailable
			// (locked age key / CI), preserving offline status at the cost of the
			// pre-existing false-pending in that degraded mode. Resolved values
			// are only hashed here, never printed.
			rendered := secrets.ForRender(c)
			secBackend := secrets.SelectBackend(c.Config.Secrets, home, userHome)
			if resolved, serr := secrets.SubstituteCanonical(c, secBackend, secrets.EnvBackend{}); serr == nil {
				rendered = resolved
			}
			plan, err := render.Plan(rendered, reg, agents, sc, projectRoot, s, userHome)
			if err != nil {
				return err
			}

			model := buildStatusModel(plan, reg.Names(), s, userHome, sc, projectRoot)
			if jsonOut {
				// JSON to stdout, advisory warnings to stderr — keeps the
				// machine-readable payload cleanly parseable.
				emitStatusWarnings(p, c, reg, s, agents)
				return emitJSON(p.Out, model)
			}
			renderStatusText(p, model)
			emitStatusWarnings(p, c, reg, s, agents)
			return nil
		},
	}
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: user; prompts when run inside a project tree)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON instead of the formatted report")
	return cmd
}

// buildStatusModel classifies every tracked file/key/orphan across agents into
// the structured statusModel. It is the single source of truth both the
// formatted dashboard and --json render from.
func buildStatusModel(plan render.RenderPlan, names []string, s *state.Targets, userHome string, sc adapter.Scope, projectRoot string) statusModel {
	model := statusModel{Summary: map[string]int{}}
	for _, name := range names {
		res, ok := plan.PerAgent[name]
		if !ok {
			continue
		}
		ag := statusAgent{Agent: name}
		seen := map[string]bool{}
		// file-level: every non-key-merge op is a whole-file item (including the
		// "replace" strategy used by skills/subagents/commands/memory).
		for _, op := range res.Ops {
			if render.IsKeyMerge(op.MergeStrategy) {
				continue // covered key-by-key below
			}
			if seen[op.Path] {
				continue
			}
			seen[op.Path] = true
			hsrc := hashContent(op.Content)
			happlied := s.Files[stateFileKey(userHome, name, sc, projectRoot, op.Path)].SHA256
			hdest := hashFile(op.Path)
			cls := drift.Classify(hsrc, happlied, hdest).String()
			ag.Items = append(ag.Items, statusItem{Path: op.Path, Class: cls})
			model.Summary[cls]++
		}
		// key-level: walk owned pointers for each merge op.
		for _, op := range res.Ops {
			if !render.IsKeyMerge(op.MergeStrategy) {
				continue
			}
			var ours map[string]any
			_ = json.Unmarshal(op.Content, &ours)
			final := readDestFile(op.MergeStrategy, op.Path)
			for _, ptr := range render.CollectPointers(ours, "") {
				hsrc := hashAnyValue(getPointerValue(ours, ptr))
				happlied := s.Keys[stateKeyKey(userHome, name, sc, projectRoot, op.Path, ptr)].SHA256
				hdest := hashAnyValue(getPointerValue(final, ptr))
				cls := drift.Classify(hsrc, happlied, hdest).String()
				ag.Items = append(ag.Items, statusItem{Path: op.Path, Pointer: ptr, Class: cls})
				model.Summary[cls]++
			}
		}
		// orphans: whole-file dests this agent still owns in state but no longer
		// renders (the source component was removed). Without these, status
		// reports nothing for a file that lingers and the next apply/reconcile
		// would act on.
		for _, orphan := range render.OrphanFiles(s, userHome, name, sc, projectRoot, res.Ops) {
			happlied := s.Files[stateFileKey(userHome, name, sc, projectRoot, orphan)].SHA256
			cls := drift.Classify("", happlied, hashFile(orphan)).String()
			ag.Items = append(ag.Items, statusItem{Path: orphan, Class: cls})
			model.Summary[cls]++
		}
		model.Agents = append(model.Agents, ag)
	}
	return model
}

// classOrder is the stable display order for the summary footer and groups the
// drift classes by severity for coloring.
var classOrder = []string{
	"clean", "converged", "new", "pending",
	"drift", "conflict", "foreign-collision", "orphan", "orphan-drifted",
}

// styleClass maps a drift class to its glyph and color. Green = synced, cyan =
// a pending change apply will make, red = unexpected/destructive drift, yellow
// = an orphan needing a decision.
func styleClass(p *ui.Printer, cls string) (glyph string, color func(string) string) {
	switch cls {
	case "clean", "converged":
		return ui.GlyphOK, p.Green
	case "new", "pending":
		return ui.GlyphArrow, p.Cyan
	case "drift", "conflict", "foreign-collision", "orphan-drifted":
		return ui.GlyphErr, p.Red
	case "orphan":
		return ui.GlyphWarn, p.Yellow
	default:
		return ui.GlyphInfo, p.Faint
	}
}

// renderStatusText prints the formatted drift dashboard: a bold header per
// agent, a glyph + color-coded class per item, and a one-line summary footer.
func renderStatusText(p *ui.Printer, model statusModel) {
	for _, ag := range model.Agents {
		fmt.Fprintln(p.Out, p.Bold("["+ag.Agent+"]"))
		for _, it := range ag.Items {
			disp := it.Path
			if it.Pointer != "" {
				disp = it.Path + "#" + it.Pointer
			}
			glyph, color := styleClass(p, it.Class)
			// Pad the plain "glyph class" to a fixed visible width BEFORE
			// coloring so ANSI bytes never shift the path column.
			label := ui.Pad(glyph+" "+it.Class, 20)
			fmt.Fprintf(p.Out, "  %s %s\n", color(label), disp)
		}
	}

	// Summary footer lists only non-zero classes, so the words "drift" /
	// "conflict" / "pending" never appear when there is none of that state.
	var segs []string
	for _, cls := range classOrder {
		n := model.Summary[cls]
		if n == 0 {
			continue
		}
		_, color := styleClass(p, cls)
		segs = append(segs, color(fmt.Sprintf("%d %s", n, cls)))
	}
	if len(segs) > 0 {
		fmt.Fprintln(p.Out, "")
		fmt.Fprintln(p.Out, strings.Join(segs, "  ·  "))
	}
	renderStatusLegend(p, model.Summary)
}

// classLegend gives a one-line, action-focused explanation of what `apply`
// will do for an item in each drift class. Phrased so each line reads as a
// continuation of "apply will…". "clean" is intentionally omitted: the word
// is self-evident and adding "no action" would just be noise; the legend
// itself is also skipped entirely when the summary contains nothing but
// "clean" items, so a fully-synced status report stays as terse as today.
var classLegend = map[string]string{
	"converged":         "no action (source and dest now match)",
	"new":               "will be created",
	"pending":           "will be updated to match source",
	"drift":             "will be overwritten (use reconcile to keep the dest edit)",
	"conflict":          "will be overwritten (use reconcile to merge the dest edit)",
	"foreign-collision": "will be backed up and overwritten",
	"orphan":            "will be deleted",
	"orphan-drifted":    "will be deleted (a local edit will be lost)",
}

// renderStatusLegend prints a brief glossary of the drift classes that
// actually appear in the summary. Each line is colored to match the per-item
// dashboard above and prefixed with the same glyph, so the user can scan
// from a body row to its meaning by shape and color, not just by word.
func renderStatusLegend(p *ui.Printer, summary map[string]int) {
	type entry struct {
		cls, msg string
	}
	var rows []entry
	for _, cls := range classOrder {
		if summary[cls] == 0 {
			continue
		}
		msg, ok := classLegend[cls]
		if !ok {
			continue
		}
		rows = append(rows, entry{cls, msg})
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprintln(p.Out, "")
	fmt.Fprintln(p.Out, p.Faint("What `apply` will do:"))
	for _, r := range rows {
		glyph, color := styleClass(p, r.cls)
		label := ui.Pad(glyph+" "+r.cls, 20)
		fmt.Fprintf(p.Out, "  %s %s\n", color(label), p.Faint(r.msg))
	}
}

// emitStatusWarnings writes advisory diagnostics to stderr: orphaned state from
// a removed/disabled agent, and native plugins not yet declared in source.
// These are not part of the status model (and not the --json payload).
func emitStatusWarnings(p *ui.Printer, c source.Canonical, reg *adapter.Registry, s *state.Targets, agents []string) {
	for _, a := range orphanedStateAgents(s, agents) {
		fmt.Fprintf(p.Err, "%s agent %q is not enabled but still owns tracked files/keys in state; its "+
			"native config is orphaned. Run `agentsync agent disable %s --purge` to remove what agentsync wrote.\n",
			p.Yellow("warning:"), a, a)
	}
	// Nudge: plugins installed natively in an enabled agent but not yet declared
	// in source. agentsync treats them as foreign-managed (never drift), so this
	// is informational — it points at `import`.
	undeclared := undeclaredNativePlugins(c, reg, agents)
	for _, name := range reg.Names() {
		missing := undeclared[name]
		if len(missing) == 0 {
			continue
		}
		fmt.Fprintf(p.Err, "%s %d plugin(s) installed in %s are not in your source (%s); "+
			"run `agentsync import %s:plugin` to manage them.\n",
			p.Cyan("note:"), len(missing), name, strings.Join(missing, ", "), name)
	}
}

// orphanedStateAgents returns, sorted, the agent names that appear as a state
// key prefix ("agent:scope:…") but are not in the enabled set — i.e. agents
// whose rendered native config and state entries linger after a `remove` or a
// `disable` without `--purge`.
func orphanedStateAgents(s *state.Targets, enabled []string) []string {
	en := make(map[string]bool, len(enabled))
	for _, n := range enabled {
		en[n] = true
	}
	found := map[string]bool{}
	collect := func(key string) {
		if i := strings.IndexByte(key, ':'); i > 0 {
			if a := key[:i]; !en[a] {
				found[a] = true
			}
		}
	}
	for k := range s.Files {
		collect(k)
	}
	for k := range s.Keys {
		collect(k)
	}
	out := make([]string, 0, len(found))
	for a := range found {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// stateFileKey builds the state Files map key matching render.RecordOpsState.
// Format: "agent:scope:portableProject:portablePath" (project and path are
// HOME-relative against the user's $HOME so keys are portable across
// machines — userHome must be paths.HomeDir, NOT the agentsync home).
func stateFileKey(userHome, agent string, sc adapter.Scope, projectRoot, path string) string {
	return fmt.Sprintf("%s:%s:%s:%s", agent, sc.String(),
		paths.HomeRelative(userHome, projectRoot), paths.HomeRelative(userHome, path))
}

// stateKeyKey builds the state Keys map key matching render.RecordOpsState.
// Format: "agent:scope:portableProject:portablePath:ptr".
func stateKeyKey(userHome, agent string, sc adapter.Scope, projectRoot, path, ptr string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s", agent, sc.String(),
		paths.HomeRelative(userHome, projectRoot), paths.HomeRelative(userHome, path), ptr)
}

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hashFile returns the SHA-256 hex digest of the file at path. Returns
// the empty string on missing-file errors (which `drift.Classify` reads
// as "absent" — the expected signal for Orphan / OrphanDrifted).
//
// If the path is a symlink, hashFile returns a special marker so the
// drift classifier can flag the file as drifted in a way the user can
// act on. A managed file becoming a symlink (e.g. user replaced
// .claude.json with `ln -s /dev/null`) used to silently read through
// the link and compare hashes — making the swap invisible to status.
func hashFile(path string) string {
	info, lerr := os.Lstat(path)
	if lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		// Return a sentinel that will never match a content hash.
		// We don't include the link target to keep the sentinel stable
		// (target may resolve to whatever attacker chose); just signal
		// "this is a symlink now."
		return "symlink-not-regular-file"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return hashContent(data)
}

func hashAnyValue(v any) string {
	if v == nil {
		return ""
	}
	data, _ := json.Marshal(v)
	return hashContent(data)
}

// standardizeJSONC converts JSONC (comments, trailing commas) to plain JSON
// bytes so encoding/json can parse it. It mirrors how the adapters read these
// destination files (hujson.Parse + Standardize), keeping the drift/import
// read paths in agreement with the apply write path.
func standardizeJSONC(data []byte) ([]byte, error) {
	v, err := hujson.Parse(data)
	if err != nil {
		return nil, err
	}
	v.Standardize()
	return v.Pack(), nil
}

func getPointerValue(m map[string]any, ptr string) any {
	if !strings.HasPrefix(ptr, "/") {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	var cur any = m
	for _, p := range parts {
		// Decode RFC 6901 escapes so a managed id containing '~' or '/'
		// (which CollectPointers escaped to ~0/~1) matches the real key.
		// Without this, status/diff looked up the literal escaped key, found
		// nothing, and reported phantom drift forever for that item.
		p = strings.ReplaceAll(p, "~1", "/")
		p = strings.ReplaceAll(p, "~0", "~")
		mp, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mp[p]
	}
	return cur
}
