package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
)

// diffHunk is one changed file or merged key. Source and Dest are the
// secret-MASKED renderings; --json emits these verbatim, so the same redaction
// that protects the formatted diff protects the JSON.
type diffHunk struct {
	Path    string `json:"path"`
	Pointer string `json:"pointer,omitempty"`
	Source  string `json:"source"`
	Dest    string `json:"dest"`
}

type diffModel struct {
	Hunks []diffHunk `json:"hunks"`
}

func newDiffCmd() *cobra.Command {
	var (
		jsonOut   bool
		exitCode  bool
		agentsCSV string
	)
	cmd := &cobra.Command{
		Use:   "diff [<path>]",
		Short: "print unified diff between source-rendered content and destination",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, perr := newPrinter(cmd)
			if perr != nil {
				return perr
			}
			filterPath := ""
			if len(args) == 1 {
				fp, err := filepath.Abs(args[0])
				if err != nil {
					return fmt.Errorf("resolve path: %w", err)
				}
				filterPath = fp
			}

			home := paths.AgentsyncHome(paths.OSEnv{})
			// Load WITH the plugin cache so the preview projects installed
			// plugins exactly as `apply` does — otherwise diff omits every
			// plugin-derived MCP server / skill / command and silently
			// disagrees with what apply will write.
			userHome := paths.HomeDir(paths.OSEnv{})
			c, sc, projectRoot, err := loadProjectedForScope(cmd, afero.NewOsFs(), home, true)
			if err != nil {
				return err
			}

			statePath := filepath.Join(home, ".state", "targets.json")
			s, err := state.Load(statePath)
			if err != nil {
				return err
			}
			reg := registryFactory()
			var enabledAgents []string
			enabled := map[string]bool{}
			for name, ag := range c.Config.Agents {
				if ag.Enabled {
					enabledAgents = append(enabledAgents, name)
					enabled[name] = true
				}
			}
			// --agents narrows the diff to a validated allowlist, mirroring
			// `status --agents` exactly (same split/star/validation and the same
			// empty-rejection message) so the two read-only commands stay
			// symmetric.
			selected, aerr := selectAgents(cmd, enabledAgents, enabled, agentsCSV)
			if aerr != nil {
				return aerr
			}
			if len(selected) == 0 {
				if jsonOut {
					return emitJSON(p.Out, diffModel{Hunks: []diffHunk{}})
				}
				fmt.Fprintln(p.Out, noAgentsEnabledHint(sc, projectRoot))
				return nil
			}
			// diff renders the TEMPLATED canonical (it masks the destination's
			// resolved cleartext separately, below); wrap as a render-only
			// Resolved without substituting so it works even when the secrets
			// backend is locked.
			plan, err := render.Plan(secrets.ForRender(c), reg, selected, sc, projectRoot, s, userHome)
			if err != nil {
				return err
			}

			// Build the secret-redaction map BEFORE diffing. The
			// destination file was written by a prior apply with secrets
			// substituted in cleartext (ghp_…, sk-…), so reading it back
			// and printing the diff would otherwise leak credentials to
			// stdout / log files / screenshots. We resolve every
			// reference in the canonical, then mask its resolved value
			// in both src and dst before the diff runs.
			secBackend := secrets.SelectBackend(c.Config.Secrets, home, userHome)
			envBackend := secrets.EnvBackend{}
			// Fail closed: if any ${secret:…} reference cannot be resolved now
			// (age identity locked/absent, backend misconfigured), the cleartext
			// value a prior apply substituted into the destination file cannot be
			// redacted — CollectResolved silently skips unresolvable refs — so
			// printing the diff would leak it. Refuse with an actionable message
			// rather than risk emitting a credential to stdout / logs.
			if missing := secrets.UnresolvedSecretRefs(&c, secBackend, envBackend); len(missing) > 0 {
				return fmt.Errorf("diff: cannot resolve reference(s) %s; "+
					"the destination file may contain a cleartext secret/env value that diff cannot redact "+
					"(an env var set at apply time but unset now, or a locked secrets backend). "+
					"Set the env var(s) / unlock the backend ([secrets] in agentsync.toml) and retry",
					strings.Join(missing, ", "))
			}
			redact := secrets.CollectResolved(&c, secBackend, envBackend)

			// Collect all hunks (masked src/dst) first, then render either the
			// formatted diff or --json. Pretty rendering and JSON share the
			// same masked strings, so the secret-leak guards above protect
			// both modes.
			hunks, filterMatched := collectDiffHunks(plan, reg.Names(), filterPath, redact)

			// A <path> that matched no rendered op is a typo or an unmanaged file
			// — distinct from a managed path that is in sync ("no diff"). Fail with
			// an actionable message rather than the ambiguous "no diff" so the user
			// knows the path (not the sync state) was the problem.
			if !filterMatched {
				return fmt.Errorf("path %s is not managed by agentsync (no enabled agent renders it); "+
					"diff takes a filesystem path, not an agent name", ui.Sanitize(filterPath))
			}

			switch {
			case jsonOut:
				if err := emitJSON(p.Out, diffModel{Hunks: hunks}); err != nil {
					return err
				}
			case len(hunks) == 0:
				fmt.Fprintln(p.Out, "no diff")
			default:
				dmp := diffmatchpatch.New()
				for _, h := range hunks {
					label := h.Path
					if h.Pointer != "" {
						label = h.Path + "#" + h.Pointer
					}
					// label embeds a config-derived component name/id; sanitize on
					// display so an ESC in a shared config's name can't inject escapes
					// into the diff header (issue #93/#171).
					label = ui.Sanitize(label)
					fmt.Fprintf(p.Out, "%s %s\n", p.Red("--- source"), label)
					fmt.Fprintf(p.Out, "%s %s\n", p.Green("+++ dest  "), label)
					diffs := dmp.DiffMain(h.Dest, h.Source, false)
					fmt.Fprintln(p.Out, renderDiffText(p, diffs))
				}
			}
			// --exit-code turns diff into a CI gate: non-zero (stable) when any
			// hunk exists, 0 when clean. The diff above is emitted first, so output
			// is unchanged — only the process exit differs.
			if exitCode && len(hunks) > 0 {
				return newDriftExitError()
			}
			return nil
		},
	}
	markScopeAware(cmd)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON instead of the formatted diff")
	cmd.Flags().BoolVar(&exitCode, "exit-code", false, fmt.Sprintf("exit %d if any diff hunk exists (0 when clean); for CI gates", exitCodeDrift))
	addAgentsFlag(cmd, &agentsCSV, "diff")
	return cmd
}

// renderDiffText turns a diffmatchpatch result into a printable string. In
// color mode, insertions and deletions are highlighted green/red inline; in
// plain mode (NO_COLOR, --color=never, or a non-terminal), inserts wrap as
// {+text+} and deletes as [-text-] so the change is still legible when piped
// to a file or grep. The previous implementation emitted raw ANSI in either
// case, leaking escape codes into redirects.
func renderDiffText(p *ui.Printer, diffs []diffmatchpatch.Diff) string {
	var b strings.Builder
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			if p.Color() {
				b.WriteString(p.Green(d.Text))
			} else {
				b.WriteString("{+" + d.Text + "+}")
			}
		case diffmatchpatch.DiffDelete:
			if p.Color() {
				b.WriteString(p.Red(d.Text))
			} else {
				b.WriteString("[-" + d.Text + "-]")
			}
		default:
			b.WriteString(d.Text)
		}
	}
	return b.String()
}

// modeHunk describes a permission-bit mismatch between the mode apply would
// maintain for a whole-file item (op.Mode) and the file's current perm on
// disk, both as the walk recorded them. ok is false when they match, op.Mode
// is 0 (unspecified), or the file is absent/refused-symlink/non-regular (the
// content path already covers those) — planItem.opModeDrifted's gate. It lets
// `diff` surface a content-identical chmod — which yields no text hunk — rather
// than silently reporting "no diff".
func modeHunk(it planItem) (source, dest string, ok bool) {
	if !it.opModeDrifted() {
		return "", "", false
	}
	return fmt.Sprintf("mode %04o", os.FileMode(it.op.Mode).Perm()),
		fmt.Sprintf("mode %04o", os.FileMode(it.destPerm).Perm()), true
}

// symlinkRefusedHunkDest is the Dest of a "symlink" hunk. A CONSTANT,
// deliberately: the link TARGET is attacker-choosable and this string reaches
// the terminal unsanitized (only the hunk label goes through ui.Sanitize), so
// embedding it would reopen the #93/#171 escape-injection class.
const symlinkRefusedHunkDest = "symlink (not compared through; set " + iox.AllowSymlinkDestEnv +
	"=1 to read and write through the link)"

// symlinkUnresolvableHunkDest is its sibling for a link the user opted into
// that does not resolve (dangling, loop); apply fails on it the same way.
const symlinkUnresolvableHunkDest = "symlink (target cannot be resolved: dangling, loop, or unreadable; apply refuses it too)"

// symlinkHunk describes a destination that is a symlink the read side will not
// look through — refused by AGENTSYNC_ALLOW_SYMLINK_DEST being unset, the same
// condition under which iox.AtomicWrite refuses to write through one, or
// unresolvable. A whole-file hunk with an empty Dest was rejected: it asserts
// the destination is EMPTY, which is false, and renders the entire source as
// one insertion.
func symlinkHunk(it planItem) (source, dest string, ok bool) {
	if !it.destSymlinkRefused() {
		return "", "", false
	}
	if it.hdest == symlinkUnresolvableSentinel {
		return "regular file", symlinkUnresolvableHunkDest, true
	}
	return "regular file", symlinkRefusedHunkDest, true
}

// shapeHunkDest is the Dest of a "shape" hunk; a constant for the same reason
// as the symlink ones.
const shapeHunkDest = "not a regular file (FIFO, device, socket or directory); remove or replace it"

// shapeHunk describes a whole-file destination readDestBytes refused by shape
// — a bare FIFO, or a link to one. Its text read is "" (there is no content to
// compare), so without this hunk diff rendered the whole source as an
// insertion against an "empty" destination that is not empty at all.
func shapeHunk(it planItem) (source, dest string, ok bool) {
	if it.ptr != "" || it.hdest != shapeSentinel {
		return "", "", false
	}
	return "regular file", shapeHunkDest, true
}

func marshalPretty(v any) string {
	if v == nil {
		return "<absent>"
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimSpace(string(data))
}

// collectDiffHunks runs every selected agent's rendered ops through
// walkPlanItems and collects the masked source/dest hunks that differ, in walk
// order (registry order, plan order, merged keys sorted). names is the agent
// iteration order (reg.Names()); filterPath, when non-empty, narrows the walk
// to ops whose Path equals it exactly. filterMatched reports whether that path
// matched ANY rendered op — it is set inside the walk's matchOp, on op match,
// before any item is produced, so a matching op that yields no item (an emptied
// "{}" section) still counts as managed rather than as a typo (#229 amendment
// A3).
//
// diff never consults state: it has no "applied" side, and whether a hunk
// prints is decided by MASKED-TEXT equality, never by the walk's class — a
// templated source against a cleartext destination classifies `conflict` yet
// masks to equal, and diff must print nothing there. The walk therefore runs
// against an empty state, exactly as the pre-#229 copy consulted none; the
// classes it computes are unused here.
func collectDiffHunks(plan render.RenderPlan, names []string, filterPath string,
	redact map[string]string,
) (hunks []diffHunk, filterMatched bool) {
	filterMatched = filterPath == ""
	items := walkPlanItems(planWalk{
		plan: plan, agents: names, state: state.New(),
		withText: true,
		matchOp: func(_ string, op adapter.FileOp) bool {
			if filterPath != "" && op.Path != filterPath {
				return false
			}
			filterMatched = true
			return true
		},
	})
	for _, it := range items {
		srcStr := secrets.MaskResolved(it.srcText, redact)
		dstStr := secrets.MaskResolved(it.dstText, redact)
		if it.ptr != "" {
			// Key-level diff: one hunk per differing pointer.
			if srcStr == dstStr {
				continue
			}
			hunks = append(hunks, diffHunk{Path: it.op.Path, Pointer: it.ptr, Source: srcStr, Dest: dstStr})
			continue
		}
		// File-level diff. The symlink check runs BEFORE the text comparison:
		// a refused link's dstText is "", and an empty op.Content must not fall
		// through to "equal" and then into the mode branch.
		if src, dst, ok := symlinkHunk(it); ok {
			hunks = append(hunks, diffHunk{Path: it.op.Path, Pointer: "symlink", Source: src, Dest: dst})
			continue
		}
		if src, dst, ok := shapeHunk(it); ok {
			hunks = append(hunks, diffHunk{Path: it.op.Path, Pointer: "shape", Source: src, Dest: dst})
			continue
		}
		if srcStr == dstStr {
			// Content identical: surface a mode-only drift as a "mode" hunk.
			if src, dst, ok := modeHunk(it); ok {
				hunks = append(hunks, diffHunk{Path: it.op.Path, Pointer: "mode", Source: src, Dest: dst})
			}
			continue
		}
		hunks = append(hunks, diffHunk{Path: it.op.Path, Source: srcStr, Dest: dstStr})
	}
	return hunks, filterMatched
}
