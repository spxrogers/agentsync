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
	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/state"
)

func newDiffCmd() *cobra.Command {
	var (
		scopeFlag   string
		projectFlag string
	)
	cmd := &cobra.Command{
		Use:   "diff [<path>]",
		Short: "print unified diff between source-rendered content and destination",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
			userHome := paths.HomeDir(paths.OSEnv{})
			c, err := marketplace.LoadProjected(afero.NewOsFs(), home, pluginCacheRoot)
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
			if len(agents) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no agents enabled; run `agentsync agent add claude` (or opencode)")
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

			dmp := diffmatchpatch.New()
			w := cmd.OutOrStdout()
			hasDiff := false

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
					if op.MergeStrategy == "merge-json-keys" || op.MergeStrategy == "merge-jsonc-keys" {
						// Key-level diff: compare per pointer.
						if seen[op.Path] {
							continue
						}
						seen[op.Path] = true
						var ours map[string]interface{}
						_ = json.Unmarshal(op.Content, &ours)
						final := readJSONFile(op.Path)
						for _, ptr := range render.CollectPointers(ours, "") {
							srcVal := getPointerValue(ours, ptr)
							dstVal := getPointerValue(final, ptr)
							srcStr := secrets.MaskResolved(marshalPretty(srcVal), redact)
							dstStr := secrets.MaskResolved(marshalPretty(dstVal), redact)
							if srcStr == dstStr {
								continue
							}
							hasDiff = true
							fmt.Fprintf(w, "--- source  %s#%s\n", op.Path, ptr)
							fmt.Fprintf(w, "+++ dest    %s#%s\n", op.Path, ptr)
							diffs := dmp.DiffMain(dstStr, srcStr, false)
							fmt.Fprintln(w, dmp.DiffPrettyText(diffs))
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
						hasDiff = true
						fmt.Fprintf(w, "--- source  %s\n", op.Path)
						fmt.Fprintf(w, "+++ dest    %s\n", op.Path)
						diffs := dmp.DiffMain(dstStr, srcStr, false)
						fmt.Fprintln(w, dmp.DiffPrettyText(diffs))
					}
				}
			}

			if !hasDiff {
				fmt.Fprintln(w, "no diff")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: auto-detect from cwd)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project)")
	return cmd
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
