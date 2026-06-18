package cli

import (
	"encoding/json"
	"fmt"
	"io"
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
--apply). "Non-lossy" means the candidate version introduces no new translation
loss (adapter skip) for an enabled agent, judged by projecting the plugin
exactly as apply does and diffing the skips.

When --apply is set, the same scope/project resolution as 'agentsync apply'
is used (--scope project or --project <path>; default user) so the
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
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: user; prompts when run inside a project tree) — only used with --apply")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project) — only used with --apply")
	return cmd
}

func updateRun(cmd *cobra.Command, doApply, autoSafe bool, scopeFlag, projectFlag string) error {
	if autoSafe && !doApply {
		return fmt.Errorf("--auto-safe requires --apply (it only filters which bumps are applied)")
	}
	p, err := newPrinter(cmd)
	if err != nil {
		return err
	}
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
		// mpName is untrusted.Text: printed via %s it sanitizes itself, so the
		// warnings/notices below need no explicit ui.Sanitize; Unverified() is the
		// raw value for filesystem/map use.
		mpName := mp.Name
		cacheDir := marketplaceCacheDir(home, mpName.Unverified())

		src, perr := parseMarketplaceSource(mp.Marketplace.URL)
		if perr != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "warning: marketplace %s has unparseable URL %q: %v\n", mpName, mp.Marketplace.URL, perr)
			continue
		}
		if mp.Marketplace.Ref != "" {
			src.Ref = mp.Marketplace.Ref
		}

		fetcher := marketplace.Dispatch(src)
		stopSpin := p.Spin(fmt.Sprintf("fetching marketplace %s", mpName))
		result, err := fetcher.Fetch(src, cacheDir)
		stopSpin()
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "warning: re-fetch marketplace %s failed: %v\n", mpName, err)
			continue
		}

		// Update state with new fetch time and SHA.
		st.Marketplaces[mpName.Unverified()] = state.Marketplace{
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
					entries[pe.Name.Unverified()] = pe
				}
				fetched[mpName.Unverified()] = entries
			}
		}

		fmt.Fprintf(cmd.OutOrStdout(), "fetched marketplace %s (sha=%s)\n",
			mpName, truncate(result.HeadSHA, 12))
	}

	// Compute fresh manifest SHAs for installed plugins (for SHA drift detection).
	stopSpin := p.Spin("checking plugin manifests")
	freshSHAs := computeFreshPluginSHAs(home, c.Plugins, fetched, cmd.OutOrStdout())
	stopSpin()

	// Detect re-uploaded (same version, different SHA) plugins.
	shaWarnings := marketplace.DetectSHADrift(c.Plugins, freshSHAs)
	for _, w := range shaWarnings {
		fmt.Fprintf(cmd.OutOrStdout(),
			"warning: manifest-sha-mismatch plugin=%s version=%s recorded=%s fetched=%s (re-uploaded?)\n",
			w.ID, w.Version, truncate(w.RecordedSHA, 12), truncate(w.FetchedSHA, 12))
	}

	// Compute pending bumps.
	bumps := marketplace.ComputePendingBumps(st, c.Marketplaces, c.Plugins, fetched, c.Config.Updates.DefaultMode)

	// --auto-safe: drop bumps whose candidate version would introduce a new
	// translation loss (an adapter Skip) for any enabled agent. Each bump is
	// evaluated by projecting the plugin's installed vs candidate manifest and
	// diffing the skip identities a render emits; comparing both under identical
	// conditions makes any render quirk cancel, so the delta is exactly the
	// bump's effect. Evaluation failures are treated as lossy (conservative).
	if autoSafe {
		safe, lossy := filterSafeBumps(home, bumps, fetched, c.Config, userHome, cmd.OutOrStdout())
		for _, b := range lossy {
			fmt.Fprintf(cmd.OutOrStdout(),
				"auto-safe: skipping lossy bump %s %s → %s (candidate version drops translation for an agent)\n",
				b.ID, b.From, b.To)
		}
		bumps = safe
	}

	if len(bumps) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "all plugins are up to date")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "\npending bumps (%d):\n", len(bumps))
		for _, b := range bumps {
			fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s → %s  [%s]\n",
				b.ID, b.From, b.To, b.UpdateMode)
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
			fmt.Fprintf(cmd.OutOrStdout(), "upgraded %s %s → %s\n",
				b.ID, b.From, b.To)
		}
	}

	// Re-apply to agents if any bumps were applied. Mirror the apply
	// pipeline: project-overlay merge, secret substitution, scope-aware
	// state recording. Without this, a project-scope user would have their
	// project state silently ignored, and \${secret:...\} references would
	// land literally in agent native files.
	if len(bumps) > 0 {
		c2, sc, projectRoot, err := loadProjectedForScope(cmd, afero.NewOsFs(), home, scopeFlag, projectFlag, false)
		if err != nil {
			return fmt.Errorf("reload source after upgrade: %w", err)
		}

		secBackend := secrets.SelectBackend(c2.Config.Secrets, home, userHome)
		envBackend := secrets.EnvBackend{}
		resolved, serr := secrets.SubstituteCanonical(c2, secBackend, envBackend)
		if serr != nil {
			return fmt.Errorf("substitute secrets after update: %w", serr)
		}

		agents := []string{}
		for name, ag := range c2.Config.Agents {
			if ag.Enabled {
				agents = append(agents, name)
			}
		}
		reg := registryFactory()
		plan, err := render.Plan(resolved, reg, agents, sc, projectRoot, st, userHome)
		if err != nil {
			return fmt.Errorf("plan after update: %w", err)
		}
		collisions, written, _, applyErr := render.Apply(plan, reg, st, home, userHome, sc, projectRoot)
		if applyErr != nil {
			// Mirror `apply`: if render.Apply fails mid-pipeline, the files
			// that already landed must be recorded so the next apply doesn't
			// treat them as foreign collisions. Without this best-effort save,
			// a half-applied bump leaves the dest diverged from state.
			_ = saveBestEffortState(st, statePath, plan, userHome, sc, projectRoot, written)
			return fmt.Errorf("apply after update: %w", applyErr)
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

// computeFreshPluginSHAs re-fetches each installed plugin's CURRENT upstream
// manifest and computes the SHA the same way `plugin install` recorded it
// (computeManifestSHA: sha256(plugin.json) when present, else sha256(entry)).
// The result feeds marketplace.DetectSHADrift, which flags a plugin re-uploaded
// at the SAME version with DIFFERENT content (tamper / upstream rollback at the
// same tag).
//
// Crucially it must NOT read the plugin's installed cache: that is
// byte-identical to what produced the recorded SHA, so drift would be
// structurally undetectable (the original dead-code bug). For a Relative source
// the fresh content already lives in this run's freshly re-fetched marketplace
// cache, so we read it in place (no extra fetch). For git/npm sources we fetch
// into a throwaway temp dir so the installed cache is never clobbered. A fetch
// failure is warned and skipped — a read-only poll must not fail because one
// upstream is unreachable, and an un-fetchable plugin is unknown, not "clean".
//
// Plugins with no recorded SHA (legacy / hand-managed) are skipped: DetectSHADrift
// ignores them anyway, so there is no point paying to re-fetch them.
func computeFreshPluginSHAs(home string, plugins []source.Plugin, fetched map[string]map[string]marketplace.PluginEntry, warn io.Writer) map[string]string {
	out := make(map[string]string, len(plugins))
	for _, pl := range plugins {
		if pl.Plugin.ManifestSHA == "" {
			continue
		}
		// plID is the raw plugin id for filesystem/map use; pl.ID prints itself
		// sanitized when used directly in the warnings below.
		plID := pl.ID.Unverified()
		_, mpName := splitPluginRef(pl.Plugin.ID.Unverified())
		if mpName == "" {
			mpName = "default"
		}
		entries, ok := fetched[mpName]
		if !ok {
			continue
		}
		mpEntry, ok := entries[plID]
		if !ok {
			continue
		}
		// Drift detection is a SAME-version re-upload check. If the upstream now
		// advertises a DIFFERENT version, that's a pending bump (handled by
		// ComputePendingBumps), not a re-upload — comparing the recorded SHA
		// against a different version's manifest would be a false positive.
		if !mpEntry.Version.Empty() && mpEntry.Version != pl.Plugin.Version {
			continue
		}
		mpCacheRoot := marketplaceCacheDir(home, mpName)
		src := mpEntry.Source

		// Relative source: read the freshly re-fetched marketplace cache in
		// place — the plugin.json lives at <mpCacheRoot>/<relative>/.claude-plugin/.
		if src.Relative != "" {
			relCacheDir := filepath.Join(mpCacheRoot, src.Relative)
			if sha := computeManifestSHA(home, plID, mpEntry, nil, relCacheDir); sha != "" {
				out[plID] = sha
			}
			continue
		}

		// git/npm source: fetch into a throwaway temp dir.
		tmp, err := os.MkdirTemp("", "agentsync-drift-")
		if err != nil {
			if warn != nil {
				fmt.Fprintf(warn, "warning: drift check for %s skipped: %v\n", pl.ID, err)
			}
			continue
		}
		if _, ferr := marketplace.Dispatch(src).Fetch(src, tmp); ferr != nil {
			if warn != nil {
				fmt.Fprintf(warn, "warning: drift check for %s skipped (re-fetch failed): %v\n", pl.ID, ferr)
			}
			_ = os.RemoveAll(tmp)
			continue
		}
		if sha := computeManifestSHA(home, plID, mpEntry, nil, tmp); sha != "" {
			out[plID] = sha
		}
		_ = os.RemoveAll(tmp)
	}
	return out
}

// applyPluginBump re-fetches a single plugin and updates its plugins/<id>.toml.
func applyPluginBump(home string, b marketplace.Bump, fetched map[string]map[string]marketplace.PluginEntry) error {
	// bID is the raw id for path/map use; b.ID prints itself sanitized in errors.
	bID := b.ID.Unverified()
	pluginPath := filepath.Join(home, "plugins", bID+".toml")
	existing, err := readPluginTOML(pluginPath)
	if err != nil {
		return err
	}

	// Find the marketplace entry for re-fetch.
	_, mpName := splitPluginRef(existing.Plugin.ID.Unverified())
	if mpName == "" {
		mpName = "default"
	}

	entries, ok := fetched[mpName]
	if !ok {
		return fmt.Errorf("marketplace %q not in fetched index", mpName)
	}
	mpEntry, ok := entries[bID]
	if !ok {
		return fmt.Errorf("plugin %q not found in marketplace %q", b.ID, mpName)
	}

	cacheDir := pluginCacheDir(home, bID)
	mpCacheRoot := marketplaceCacheDir(home, mpName)
	src := mpEntry.Source
	if src.Relative != "" {
		src.Relative = filepath.Join(mpCacheRoot, src.Relative)
		src.RootDir = mpCacheRoot
	}

	// Fetch into a TEMP cache, not the live cache. The live cache and
	// plugins/<id>.toml must never diverge: if the bump overwrote the live
	// cache and then the TOML write failed, the recorded version+SHA would
	// stay old while the cache is new, and the immediate re-apply's
	// LoadProjected would hard-fail manifest-SHA verification — bricking the
	// WHOLE update so other plugins' bumps never reach the agents. By staging
	// in a temp dir and swapping in the cache only AFTER the TOML is durably
	// written, a failure leaves both old (consistent) and the re-apply proceeds.
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0o755); err != nil {
		return fmt.Errorf("prepare cache dir for %s: %w", b.ID, err)
	}
	tmpCache, err := os.MkdirTemp(filepath.Dir(cacheDir), ".bump-"+bID+"-")
	if err != nil {
		return fmt.Errorf("temp cache for %s: %w", b.ID, err)
	}
	defer func() { _ = os.RemoveAll(tmpCache) }()

	fetcher := marketplace.Dispatch(src)
	if _, err := fetcher.Fetch(src, tmpCache); err != nil {
		return fmt.Errorf("fetch plugin %s: %w", b.ID, err)
	}

	// Recompute the manifest SHA from the freshly fetched cache exactly as
	// `plugin install` does (computeManifestSHA), for every fetcher type, so
	// the recorded SHA matches the new plugin.json the re-apply will project.
	prevTOML, _ := os.ReadFile(pluginPath) // for rollback if the cache swap fails
	existing.Plugin.Version = b.To
	if sha := computeManifestSHA(home, bID, mpEntry, nil, tmpCache); sha != "" {
		existing.Plugin.ManifestSHA = sha
	}
	data, err := toml.Marshal(existing)
	if err != nil {
		return err
	}
	if err := iox.AtomicWrite(pluginPath, data, 0o644); err != nil {
		return err
	}

	// TOML committed; swap the fetched cache into place. If the swap fails,
	// roll the TOML back so cache (old) and TOML (old) stay consistent rather
	// than leaving a new-SHA TOML over an old cache.
	if err := swapDir(tmpCache, cacheDir); err != nil {
		if prevTOML != nil {
			_ = iox.AtomicWrite(pluginPath, prevTOML, 0o644)
		}
		return fmt.Errorf("commit plugin cache %s: %w", b.ID, err)
	}
	return nil
}

// swapDir replaces dst with src by removing dst and renaming src into place.
// src and dst must be on the same filesystem (callers create src as a sibling
// of dst). After a successful swap src no longer exists.
func swapDir(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// filterSafeBumps partitions bumps into those whose candidate version adds no
// new translation losses (safe) and those that do (lossy), for `update
// --auto-safe`. An evaluation error is conservatively treated as lossy so a
// fetch/parse failure can never cause a lossy bump to slip through.
func filterSafeBumps(home string, bumps []marketplace.Bump, fetched map[string]map[string]marketplace.PluginEntry, cfg source.Config, userHome string, warn io.Writer) (safe, lossy []marketplace.Bump) {
	reg := registryFactory()
	var agents []string
	for name, ag := range cfg.Agents {
		if ag.Enabled {
			agents = append(agents, name)
		}
	}
	for _, b := range bumps {
		isLossy, err := bumpIsLossy(home, b, fetched, cfg, reg, agents, userHome)
		if err != nil {
			fmt.Fprintf(warn, "warning: auto-safe: cannot evaluate %s (%v); excluding to be safe\n", b.ID, err)
			lossy = append(lossy, b)
			continue
		}
		if isLossy {
			lossy = append(lossy, b)
		} else {
			safe = append(safe, b)
		}
	}
	return safe, lossy
}

// bumpIsLossy reports whether applying b introduces a new adapter Skip (a
// translation loss) for any enabled agent. It renders the plugin's installed
// (current cache) vs candidate (freshly fetched) projection in isolation and
// returns true if the candidate emits a skip identity the current one did not.
// Rendering both under identical conditions cancels any pipeline quirk, so the
// delta is exactly the bump's effect.
func bumpIsLossy(home string, b marketplace.Bump, fetched map[string]map[string]marketplace.PluginEntry, cfg source.Config, reg *adapter.Registry, agents []string, userHome string) (bool, error) {
	bID := b.ID.Unverified()
	existing, err := readPluginTOML(filepath.Join(home, "plugins", bID+".toml"))
	if err != nil {
		return false, err
	}
	_, mpName := splitPluginRef(existing.Plugin.ID.Unverified())
	if mpName == "" {
		mpName = "default"
	}
	entries, ok := fetched[mpName]
	if !ok {
		return false, fmt.Errorf("marketplace %q not in fetched index", mpName)
	}
	mpEntry, ok := entries[bID]
	if !ok {
		return false, fmt.Errorf("plugin %q not found in marketplace %q", b.ID, mpName)
	}

	oldSkips, err := projectedSkips(mpEntry, pluginCacheDir(home, bID), cfg, reg, agents, userHome)
	if err != nil {
		return false, err
	}

	// Fetch the candidate into a throwaway temp dir (never the live cache).
	mpCacheRoot := marketplaceCacheDir(home, mpName)
	src := mpEntry.Source
	if src.Relative != "" {
		src.Relative = filepath.Join(mpCacheRoot, src.Relative)
		src.RootDir = mpCacheRoot
	}
	tmp, err := os.MkdirTemp("", "agentsync-autosafe-")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if _, err := marketplace.Dispatch(src).Fetch(src, tmp); err != nil {
		return false, fmt.Errorf("fetch candidate %s: %w", b.ID, err)
	}
	newSkips, err := projectedSkips(mpEntry, tmp, cfg, reg, agents, userHome)
	if err != nil {
		return false, err
	}

	for id := range newSkips {
		if !oldSkips[id] {
			return true, nil
		}
	}
	return false, nil
}

// projectedSkips projects a plugin via marketplace.Project — the SAME single
// projector apply now uses (marketplace.LoadProjected) — and returns the set of
// "agent\x00component\x00name" skip identities rendering just that plugin's
// components for the given agents would emit. Using apply's projector keeps the
// lossiness decision faithful to what apply will actually render. Skips are
// structural (independent of resolved secret values), so the templated render
// is sufficient and no secrets backend is required.
func projectedSkips(entry marketplace.PluginEntry, cacheDir string, cfg source.Config, reg *adapter.Registry, agents []string, userHome string) (map[string]bool, error) {
	proj, err := marketplace.Project(entry, cacheDir)
	if err != nil {
		return nil, err
	}
	mini := source.Canonical{
		Config:     cfg,
		MCPServers: proj.MCPServers,
		Skills:     proj.Skills,
		Subagents:  proj.Subagents,
		Commands:   proj.Commands,
		Hooks:      proj.Hooks,
		LSPServers: proj.LSPServers,
	}
	plan, err := render.Plan(secrets.ForRender(mini), reg, agents, adapter.ScopeUser, "", nil, userHome)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for name, res := range plan.PerAgent {
		for _, sk := range res.Skips {
			out[name+"\x00"+sk.Component+"\x00"+sk.Name] = true
		}
	}
	return out, nil
}
