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
		scopeFlag   string
		projectFlag string
		jsonOut     bool
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
					return emitJSON(p.Out, diffModel{Hunks: []diffHunk{}})
				}
				fmt.Fprintln(p.Out, "no agents enabled; run `agentsync agent add claude` (or opencode)")
				return nil
			}
			// diff renders the TEMPLATED canonical (it masks the destination's
			// resolved cleartext separately, below); wrap as a render-only
			// Resolved without substituting so it works even when the secrets
			// backend is locked.
			plan, err := render.Plan(secrets.ForRender(c), reg, agents, sc, projectRoot, s, userHome)
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
			var hunks []diffHunk
			for _, name := range reg.Names() {
				res, ok := plan.PerAgent[name]
				if !ok {
					continue
				}
				seen := map[string]bool{}
				for _, op := range res.Ops {
					if filterPath != "" && op.Path != filterPath {
						continue
					}
					if render.IsKeyMerge(op.MergeStrategy) {
						// Key-level diff: compare per pointer. NOT deduped by path —
						// one agent emits several key-merge ops to one file (codex
						// writes /mcp_servers AND /hooks to config.toml; claude writes
						// /hooks AND /lspServers to settings.json), each owning a
						// distinct section, so every op must be walked. Deduping by
						// path here dropped the second section's drift (status's key
						// loop and the apply pipeline never path-dedup key-merge ops).
						var ours map[string]interface{}
						_ = json.Unmarshal(op.Content, &ours)
						final := readDestFile(op.MergeStrategy, op.Path)
						for _, ptr := range render.CollectPointers(ours, "") {
							srcStr := secrets.MaskResolved(marshalPretty(getPointerValue(ours, ptr)), redact)
							dstStr := secrets.MaskResolved(marshalPretty(getPointerValue(final, ptr)), redact)
							if srcStr == dstStr {
								continue
							}
							hunks = append(hunks, diffHunk{Path: op.Path, Pointer: ptr, Source: srcStr, Dest: dstStr})
						}
					} else {
						// File-level diff.
						if seen[op.Path] {
							continue
						}
						seen[op.Path] = true
						srcStr := secrets.MaskResolved(string(op.Content), redact)
						dstBytes, readErr := os.ReadFile(op.Path)
						dstStr := ""
						if readErr == nil {
							dstStr = secrets.MaskResolved(string(dstBytes), redact)
						}
						if srcStr == dstStr {
							continue
						}
						hunks = append(hunks, diffHunk{Path: op.Path, Source: srcStr, Dest: dstStr})
					}
				}
			}

			if jsonOut {
				return emitJSON(p.Out, diffModel{Hunks: hunks})
			}

			if len(hunks) == 0 {
				fmt.Fprintln(p.Out, "no diff")
				return nil
			}

			dmp := diffmatchpatch.New()
			for _, h := range hunks {
				label := h.Path
				if h.Pointer != "" {
					label = h.Path + "#" + h.Pointer
				}
				fmt.Fprintf(p.Out, "%s %s\n", p.Red("--- source"), label)
				fmt.Fprintf(p.Out, "%s %s\n", p.Green("+++ dest  "), label)
				diffs := dmp.DiffMain(h.Dest, h.Source, false)
				fmt.Fprintln(p.Out, renderDiffText(p, diffs))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: user; prompts when run inside a project tree)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON instead of the formatted diff")
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
