package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

func newStatusCmd() *cobra.Command {
	var (
		scopeFlag   string
		projectFlag string
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "report drift across registered agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			c, err := source.Load(afero.NewOsFs(), home)
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
			plan, err := render.Plan(c, reg, agents, sc, projectRoot, s)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			for _, name := range reg.Names() {
				res, ok := plan.PerAgent[name]
				if !ok {
					continue
				}
				fmt.Fprintf(w, "[%s]\n", name)
				seen := map[string]bool{}
				// file-level: for each op, classify
				for _, op := range res.Ops {
					if op.MergeStrategy != "" {
						continue // covered key-by-key below
					}
					if seen[op.Path] {
						continue
					}
					seen[op.Path] = true
					hsrc := hashContent(op.Content)
					happlied := s.Files[stateFileKey(name, sc, projectRoot, op.Path)].SHA256
					hdest := hashFile(op.Path)
					cls := drift.Classify(hsrc, happlied, hdest)
					fmt.Fprintf(w, "  %-20s %s\n", cls, op.Path)
				}
				// key-level: for each merge op, walk owned pointers
				for _, op := range res.Ops {
					if op.MergeStrategy != "merge-json-keys" && op.MergeStrategy != "merge-jsonc-keys" {
						continue
					}
					var ours map[string]any
					_ = json.Unmarshal(op.Content, &ours)
					final := readJSONFile(op.Path)
					for _, ptr := range render.CollectPointers(ours, "") {
						hsrc := hashAnyValue(getPointerValue(ours, ptr))
						happlied := s.Keys[stateKeyKey(name, sc, projectRoot, op.Path, ptr)].SHA256
						hdest := hashAnyValue(getPointerValue(final, ptr))
						cls := drift.Classify(hsrc, happlied, hdest)
						fmt.Fprintf(w, "  %-20s %s#%s\n", cls, op.Path, ptr)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: auto-detect from cwd)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project)")
	return cmd
}

// stateFileKey builds the state Files map key matching render.RecordOpsState.
// Format: "agent:scope:project:path" (project is "" for user scope).
func stateFileKey(agent string, sc adapter.Scope, projectRoot, path string) string {
	return fmt.Sprintf("%s:%s:%s:%s", agent, sc.String(), projectRoot, path)
}

// stateKeyKey builds the state Keys map key matching render.RecordOpsState.
// Format: "agent:scope:project:path:ptr".
func stateKeyKey(agent string, sc adapter.Scope, projectRoot, path, ptr string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s", agent, sc.String(), projectRoot, path, ptr)
}

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashFile(path string) string {
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

func readJSONFile(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m
}

func getPointerValue(m map[string]any, ptr string) any {
	if !strings.HasPrefix(ptr, "/") {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	var cur any = m
	for _, p := range parts {
		mp, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mp[p]
	}
	return cur
}
