package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

func newApplyCmd() *cobra.Command {
	var (
		dryRun      bool
		scopeFlag   string
		projectFlag string
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "render canonical config and write per agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
			c, err := source.LoadWithCache(afero.NewOsFs(), home, pluginCacheRoot)
			if err != nil {
				return err
			}

			// Discover project marker (walk-up from cwd, or explicit --project).
			sc, projectRoot, err := resolveProjectScope(scopeFlag, projectFlag, c)
			if err != nil {
				return err
			}

			// When project scope active, merge overlay into canonical.
			if sc == adapter.ScopeProject && projectRoot != "" {
				marker, merr := project.Discover(projectRoot)
				if merr != nil {
					return fmt.Errorf("load project marker: %w", merr)
				}
				if marker != nil {
					c = project.Merge(c, marker)
				}
			}

			// Resolve ${secret:...} and ${env:...} references before rendering.
			secBackend := secrets.SelectBackend(c.Config.Secrets, home)
			envBackend := secrets.EnvBackend{}
			if err := secrets.SubstituteCanonical(&c, secBackend, envBackend); err != nil {
				return err
			}

			agents := []string{}
			for name, ag := range c.Config.Agents {
				if ag.Enabled {
					agents = append(agents, name)
				}
			}

			reg := registryFactory()

			// Load state (needed for OwnedKeys injection in Plan).
			statePath := filepath.Join(home, ".state", "targets.json")
			s, err := state.Load(statePath)
			if err != nil {
				return err
			}

			if dryRun {
				plan, err := render.Plan(c, reg, agents, sc, projectRoot, s)
				if err != nil {
					return err
				}
				w := cmd.OutOrStdout()
				fmt.Fprintf(w, "Plan: %d ops total across %d agent(s)\n", plan.Total(), len(plan.PerAgent))
				for _, name := range reg.Names() {
					res, ok := plan.PerAgent[name]
					if !ok {
						continue
					}
					fmt.Fprintf(w, "  %-10s %d ops, %d skips\n", name, len(res.Ops), len(res.Skips))
				}
				report := render.BuildReport(c, plan, agents)
				if len(report.Rows) > 0 {
					fmt.Fprintln(w)
					report.PrintText(w)
				}
				return nil
			}

			// Real apply: render + write
			plan, err := render.Plan(c, reg, agents, sc, projectRoot, s)
			if err != nil {
				return err
			}
			if err := render.Apply(plan, reg); err != nil {
				return err
			}

			// Update state with post-apply hashes.
			for name, res := range plan.PerAgent {
				if err := render.RecordOpsState(s, name, sc, projectRoot, res.Ops); err != nil {
					return err
				}
			}
			if err := state.Save(statePath, s); err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			fmt.Fprintln(w, "applied:", plan.Total(), "ops")
			report := render.BuildReport(c, plan, agents)
			if len(report.Rows) > 0 {
				fmt.Fprintln(w)
				report.PrintText(w)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compute plan without writing destinations")
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: auto-detect from cwd)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project)")
	return cmd
}

// resolveProjectScope determines the effective scope and project root.
// Priority: --project flag > --scope flag > cwd walk-up auto-detect.
func resolveProjectScope(scopeFlag, projectFlag string, _ source.Canonical) (adapter.Scope, string, error) {
	// Explicit --project always implies project scope.
	if projectFlag != "" {
		abs, err := filepath.Abs(projectFlag)
		if err != nil {
			return adapter.ScopeUser, "", fmt.Errorf("resolve --project path: %w", err)
		}
		return adapter.ScopeProject, abs, nil
	}

	// --scope project without --project: walk up from cwd to find marker.
	if scopeFlag == "project" {
		cwd, err := os.Getwd()
		if err != nil {
			return adapter.ScopeUser, "", fmt.Errorf("getwd: %w", err)
		}
		marker, err := project.Discover(cwd)
		if err != nil {
			return adapter.ScopeUser, "", fmt.Errorf("discover project marker: %w", err)
		}
		if marker != nil {
			return adapter.ScopeProject, marker.Root, nil
		}
		// No marker found; fall through to user scope.
		return adapter.ScopeUser, "", nil
	}

	// Default / --scope user: auto-detect from cwd.
	if scopeFlag == "" || scopeFlag == "user" {
		// Auto-detect: if cwd has a marker, default to project scope.
		if scopeFlag == "" {
			cwd, err := os.Getwd()
			if err == nil {
				marker, merr := project.Discover(cwd)
				if merr == nil && marker != nil {
					return adapter.ScopeProject, marker.Root, nil
				}
			}
		}
		return adapter.ScopeUser, "", nil
	}

	return adapter.ScopeUser, "", fmt.Errorf("unknown --scope value %q; want user or project", scopeFlag)
}
