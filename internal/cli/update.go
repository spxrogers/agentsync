package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

func newUpdateCmd() *cobra.Command {
	var (
		apply       bool
		autoSafe    bool
		scopeFlag   string
		projectFlag string
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "poll marketplaces for new plugin versions and report pending bumps",
		Long: `update re-fetches all registered marketplaces, computes which installed
plugins have newer versions available, and prints the pending bumps.

By default, update is read-only (it does NOT touch agent configs). Use
--apply to immediately upgrade all track-mode plugins and apply the result.
Use --auto-safe to only bump plugins whose translation is non-lossy (requires
--apply).

When --apply is set, the same scope/project resolution as 'agentsync apply'
is used (auto-detect from cwd, --scope project, --project <path>) so the
re-render lands in the right place when running inside a project.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error {
				return updateRun(cmd, apply, autoSafe, scopeFlag, projectFlag)
			})
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "upgrade plugins and apply to agents after polling")
	cmd.Flags().BoolVar(&autoSafe, "auto-safe", false, "only bump plugins with non-lossy translation (requires --apply)")
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: auto-detect from cwd) — only used with --apply")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project) — only used with --apply")
	return cmd
}

func updateRun(cmd *cobra.Command, doApply, _ bool, scopeFlag, projectFlag string) error {
	home := paths.AgentsyncHome(paths.OSEnv{})
	userHome := paths.HomeDir(paths.OSEnv{})
	statePath := filepath.Join(home, ".state", "targets.json")

	// Load canonical source.
	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		return fmt.Errorf("load source: %w", err)
	}

	// Load state.
	st, err := state.Load(statePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// Re-fetch all registered marketplaces and build a fresh index of plugins.
	fetched := map[string]map[string]marketplace.PluginEntry{} // mpName → pluginName → entry

	for _, mp := range c.Marketplaces {
		mpName := mp.Name
		cacheDir := marketplaceCacheDir(home, mpName)

		src, perr := parseMarketplaceSource(mp.Marketplace.URL)
		if perr != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "warning: marketplace %s has unparseable URL %q: %v\n", mpName, mp.Marketplace.URL, perr)
			continue
		}
		if mp.Marketplace.Ref != "" {
			src.Ref = mp.Marketplace.Ref
		}

		fetcher := marketplace.Dispatch(src)
		result, err := fetcher.Fetch(src, cacheDir)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "warning: re-fetch marketplace %s failed: %v\n", mpName, err)
			continue
		}

		// Update state with new fetch time and SHA.
		st.Marketplaces[mpName] = state.Marketplace{
			URL:       mp.Marketplace.URL,
			HeadSHA:   result.HeadSHA,
			FetchedAt: time.Now().UTC(),
		}

		// Index plugins from the freshly-fetched marketplace.json.
		mpJSON := filepath.Join(cacheDir, ".claude-plugin", "marketplace.json")
		if data, err := os.ReadFile(mpJSON); err == nil {
			var mpDoc marketplace.Marketplace
			if json.Unmarshal(data, &mpDoc) == nil {
				entries := make(map[string]marketplace.PluginEntry, len(mpDoc.Plugins))
				for _, pe := range mpDoc.Plugins {
					entries[pe.Name] = pe
				}
				fetched[mpName] = entries
			}
		}

		fmt.Fprintf(cmd.OutOrStdout(), "fetched marketplace %s (sha=%s)\n",
			mpName, truncate(result.HeadSHA, 12))
	}

	// Compute fresh manifest SHAs for installed plugins (for SHA drift detection).
	freshSHAs := computeFreshPluginSHAs(home, c.Plugins)

	// Detect re-uploaded (same version, different SHA) plugins.
	shaWarnings := marketplace.DetectSHADrift(c.Plugins, freshSHAs)
	for _, w := range shaWarnings {
		fmt.Fprintf(cmd.OutOrStdout(),
			"warning: manifest-sha-mismatch plugin=%s version=%s recorded=%s fetched=%s (re-uploaded?)\n",
			w.ID, w.Version, truncate(w.RecordedSHA, 12), truncate(w.FetchedSHA, 12))
	}

	// Compute pending bumps.
	bumps := marketplace.ComputePendingBumps(st, c.Marketplaces, c.Plugins, fetched)

	if len(bumps) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "all plugins are up to date")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "\npending bumps (%d):\n", len(bumps))
		for _, b := range bumps {
			fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s → %s  [%s]\n", b.ID, b.From, b.To, b.UpdateMode)
		}
	}

	// Persist state (updated fetch timestamps + SHAs).
	if err := state.Save(statePath, st); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	if !doApply {
		if len(bumps) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "\nRun `agentsync update --apply` to upgrade and apply.")
		}
		return nil
	}

	// --apply: upgrade each plugin with a pending bump.
	for _, b := range bumps {
		if err := applyPluginBump(home, b, fetched); err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "warning: upgrade %s failed: %v\n", b.ID, err)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "upgraded %s %s → %s\n", b.ID, b.From, b.To)
		}
	}

	// Re-apply to agents if any bumps were applied. Mirror the apply
	// pipeline: project-overlay merge, secret substitution, scope-aware
	// state recording. Without this, a project-scope user would have their
	// project state silently ignored, and \${secret:...\} references would
	// land literally in agent native files.
	if len(bumps) > 0 {
		pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
		c2, err := source.LoadWithCache(afero.NewOsFs(), home, pluginCacheRoot)
		if err != nil {
			return fmt.Errorf("reload source after upgrade: %w", err)
		}

		sc, projectRoot, err := resolveProjectScope(scopeFlag, projectFlag, c2)
		if err != nil {
			return fmt.Errorf("resolve scope after update: %w", err)
		}
		if sc == adapter.ScopeProject && projectRoot != "" {
			marker, merr := project.Discover(projectRoot)
			if merr != nil {
				return fmt.Errorf("load project marker after update: %w", merr)
			}
			if marker != nil {
				c2 = project.Merge(c2, marker)
			}
		}

		secBackend := secrets.SelectBackend(c2.Config.Secrets, home, userHome)
		envBackend := secrets.EnvBackend{}
		if err := secrets.SubstituteCanonical(&c2, secBackend, envBackend); err != nil {
			return fmt.Errorf("substitute secrets after update: %w", err)
		}

		agents := []string{}
		for name, ag := range c2.Config.Agents {
			if ag.Enabled {
				agents = append(agents, name)
			}
		}
		reg := registryFactory()
		plan, err := render.Plan(c2, reg, agents, sc, projectRoot, st, userHome)
		if err != nil {
			return fmt.Errorf("plan after update: %w", err)
		}
		collisions, _, err := render.Apply(plan, reg, st, home, userHome, sc, projectRoot)
		if err != nil {
			return fmt.Errorf("apply after update: %w", err)
		}
		if len(collisions) > 0 {
			ew := cmd.ErrOrStderr()
			fmt.Fprintf(ew, "agentsync: update --apply backed up %d pre-existing target(s):\n", len(collisions))
			for _, r := range collisions {
				fmt.Fprintf(ew, "  %s\n", r.String())
			}
		}
		for name, res := range plan.PerAgent {
			render.PruneStaleState(st, userHome, name, sc, projectRoot, res.Ops)
		}
		for name, res := range plan.PerAgent {
			if err := render.RecordOpsState(st, userHome, name, sc, projectRoot, res.Ops); err != nil {
				return err
			}
		}
		if err := state.Save(statePath, st); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "applied:", plan.Total(), "ops")
	}

	return nil
}

// computeFreshPluginSHAs reads each installed plugin's cached plugin.json and
// computes its sha256 hex.  Returns a map of plugin ID → sha hex.  Missing or
// unreadable plugin.json files are silently skipped (they may not be cached yet).
func computeFreshPluginSHAs(home string, plugins []source.Plugin) map[string]string {
	out := make(map[string]string, len(plugins))
	for _, pl := range plugins {
		id := pl.ID
		cacheDir := pluginCacheDir(home, id)
		pluginJSONPath := filepath.Join(cacheDir, ".claude-plugin", "plugin.json")
		data, err := os.ReadFile(pluginJSONPath)
		if err != nil {
			continue
		}
		h := sha256.Sum256(data)
		out[id] = hex.EncodeToString(h[:])
	}
	return out
}

// applyPluginBump re-fetches a single plugin and updates its plugins/<id>.toml.
func applyPluginBump(home string, b marketplace.Bump, fetched map[string]map[string]marketplace.PluginEntry) error {
	pluginPath := filepath.Join(home, "plugins", b.ID+".toml")
	existing, err := readPluginTOML(pluginPath)
	if err != nil {
		return err
	}

	// Find the marketplace entry for re-fetch.
	_, mpName := splitPluginRef(existing.Plugin.ID)
	if mpName == "" {
		mpName = "default"
	}

	entries, ok := fetched[mpName]
	if !ok {
		return fmt.Errorf("marketplace %q not in fetched index", mpName)
	}
	mpEntry, ok := entries[b.ID]
	if !ok {
		return fmt.Errorf("plugin %q not found in marketplace %q", b.ID, mpName)
	}

	// Re-fetch the plugin source into its cache directory.
	cacheDir := pluginCacheDir(home, b.ID)
	mpCacheRoot := marketplaceCacheDir(home, mpName)
	src := mpEntry.Source
	if src.Relative != "" {
		src.Relative = filepath.Join(mpCacheRoot, src.Relative)
		src.RootDir = mpCacheRoot
	}
	fetcher := marketplace.Dispatch(src)
	if _, err := fetcher.Fetch(src, cacheDir); err != nil {
		return fmt.Errorf("fetch plugin %s: %w", b.ID, err)
	}

	// Update the plugin TOML. Recompute the manifest SHA from the freshly
	// fetched cache exactly as `plugin install` does (computeManifestSHA),
	// for every fetcher type. The previous code only set the SHA for git
	// fetchers — and to result.HeadSHA, a git commit SHA rather than the
	// sha256(plugin.json) that verifyPluginManifestSHA compares against —
	// so npm/relative/strict plugins kept the stale pre-bump SHA and the
	// immediate re-apply hard-failed "manifest SHA mismatch".
	existing.Plugin.Version = b.To
	if sha := computeManifestSHA(home, b.ID, mpEntry, nil, cacheDir); sha != "" {
		existing.Plugin.ManifestSHA = sha
	}

	data, err := toml.Marshal(existing)
	if err != nil {
		return err
	}
	return iox.AtomicWrite(pluginPath, data, 0o644)
}
