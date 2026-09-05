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
	"github.com/spxrogers/agentsync/internal/ui"
)

// pollOpts configures the shared marketplace-poll engine behind `plugin
// outdated` and `plugin upgrade --all`.
type pollOpts struct {
	// doApply upgrades every pending bump and re-applies to the agents.
	doApply bool
	// lossless drops bumps whose candidate version would introduce a new
	// adapter Skip for an enabled agent. Only ever set together with doApply:
	// `plugin outdated` does not define the flag, and `plugin upgrade` reaches
	// this engine only under --all, which always applies.
	lossless bool
}

// pollPluginsRun re-fetches every registered marketplace, records the fresh
// fetch timestamps + head SHAs, checks installed plugins' manifest SHAs for a
// same-version re-upload, and reports the pending bumps. With doApply it also
// upgrades each pending bump and re-applies to the agents.
//
// It is NOT read-only even without doApply: the marketplace re-fetch writes the
// cache, and the fetch timestamps + SHAs land in the state file. `plugin
// outdated`'s help owns that, because the `npm outdated` prior reads as pure.
func pollPluginsRun(cmd *cobra.Command, o pollOpts) error {
	p, err := newPrinter(cmd)
	if err != nil {
		return err
	}
	// The two helpers below take a plain io.Writer for their warnings and emit
	// the bare "warning: " sentinel. Route it through a WarnWriter on STDERR so
	// they pick up the same ⚠ WARN label as everything else — they previously
	// wrote the raw sentinel to STDOUT, which both skipped the styling and put a
	// diagnostic in the middle of the command's own output.
	warnW := ui.NewWarnWriter(p.Err, p)
	// Flush drains (and terminates) any partial line the writer still holds. It
	// runs on the error return path too, where an unterminated fragment would put
	// main's terminal ✗ ERROR line on the same row. A no-op in the normal case,
	// since every emitter ends with \n.
	defer warnW.Flush()
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
			p.Warnf("marketplace %s has unparseable URL %q: %v", mpName, mp.Marketplace.URL, perr)
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
			p.Warnf("re-fetch marketplace %s failed: %v", mpName, err)
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

		p.Successf(ui.EmojiSuccess, "fetched marketplace %s (sha=%s)",
			mpName, truncate(result.HeadSHA, 12))
	}

	// Compute fresh manifest SHAs for installed plugins (for SHA drift detection).
	stopSpin := p.Spin("checking plugin manifests")
	freshSHAs := computeFreshPluginSHAs(home, c.Plugins, fetched, warnW)
	stopSpin()

	// Detect re-uploaded (same version, different SHA) plugins.
	shaWarnings := marketplace.DetectSHADrift(c.Plugins, freshSHAs)
	for _, w := range shaWarnings {
		p.Warnf("manifest-sha-mismatch plugin=%s version=%s recorded=%s fetched=%s (re-uploaded?)",
			w.ID, w.Version, truncate(w.RecordedSHA, 12), truncate(w.FetchedSHA, 12))
	}

	// Compute pending bumps.
	bumps := marketplace.ComputePendingBumps(st, c.Marketplaces, c.Plugins, fetched, c.Config.Updates.DefaultMode)

	// --lossless: drop bumps whose candidate version
	// would introduce a new translation loss (an adapter Skip) for any enabled
	// agent. Each bump is evaluated by projecting the plugin's installed vs
	// candidate manifest and diffing the skip identities a render emits;
	// comparing both under identical conditions makes any render quirk cancel, so
	// the delta is exactly the bump's effect. An excluded bump is REPORTED, never
	// silently dropped. Evaluation failures are excluded too (conservative), but
	// reported as what they are rather than as measured losses.
	var excluded int
	if o.lossless {
		safe, lossy, unevaluable := filterSafeBumps(home, bumps, fetched, c.Config, userHome, warnW)
		for _, b := range lossy {
			p.Infof("lossless: skipping lossy bump %s %s → %s (candidate version drops translation for an agent)",
				b.ID, b.From, b.To)
		}
		// Printed ONCE for the whole run rather than per bump: the caveat is a
		// property of the check, not of any one bump, and repeating it per line
		// would bury the bumps it is meant to qualify.
		//
		// Gated on `lossy` alone, NOT on everything excluded: the caveat says the
		// loss may fall on an agent this plugin is not projected to, and offers
		// re-running without --lossless. Neither sentence is true of a bump that
		// could not be evaluated — there is no identified loss and no agent — and
		// telling that user to drop the flag would invert a refusal that exists
		// precisely because nothing is known.
		//
		// Emitted between the two loops, not after both. Detailf is an UNLABELED
		// continuation line that hangs under whatever diagnostic precedes it, so
		// printing it last would render it attached to an unevaluable bump — the
		// very misattribution the gate above exists to prevent. Gating and
		// placement have to agree.
		if len(lossy) > 0 {
			p.Detailf("%s", losslessTargetingCaveat)
		}
		for _, b := range unevaluable {
			p.Infof("lossless: skipping bump %s %s → %s (could not be evaluated; see the warning above)",
				b.ID, b.From, b.To)
		}
		excluded = len(lossy) + len(unevaluable)
		bumps = safe
	}

	if len(bumps) == 0 {
		// "up to date" is only true if nothing was held back. Announcing refusals
		// and then reporting success contradicts them — and undoes the point of
		// saying why the upgrade was declined in the first place.
		//
		// Written to Out, not via Infof: this is the command's terminal RESULT,
		// the sibling of the "up to date" line below and of the pending-bumps
		// list, not a diagnostic about it (see the Infof doc in internal/ui).
		// Plain rather than Successf because the emoji vocabulary has no glyph
		// for "held back", and ✅ would restate the contradiction this replaces.
		if excluded > 0 {
			what := fmt.Sprintf("all %d pending bumps", excluded)
			if excluded == 1 {
				what = "the only pending bump"
			}
			fmt.Fprintf(p.Out, "no upgrades applied: --lossless excluded %s\n", what)
		} else {
			p.Successf(ui.EmojiSuccess, "all plugins are up to date")
		}
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

	if !o.doApply {
		if len(bumps) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "\nRun `agentsync plugin upgrade --all` to upgrade and apply.")
		}
		return nil
	}

	// Upgrade each plugin with a pending bump.
	for _, b := range bumps {
		if err := applyPluginBump(home, b, fetched); err != nil {
			p.Warnf("upgrade %s failed: %v", b.ID, err)
		} else {
			p.Successf(ui.EmojiSuccess, "upgraded %s %s → %s", b.ID, b.From, b.To)
		}
	}

	if len(bumps) == 0 {
		return nil
	}
	return reapplyAfterPluginChange(cmd, home)
}

// reapplyAfterPluginChange re-renders the canonical to the agents after a
// plugin's pinned version changed, so a plugin upgrade lands in the agents in
// the same command rather than leaving them stale until the next `apply`.
//
// It runs the REAL apply pipeline — runApplyPipeline, the same callable `apply`
// runs — rather than a transcription of it. The previous copy had already
// fallen behind: it took no pre-apply git baseline and no checkpoint
// (#118/#143), so an upgrade overwrote ~/.claude with nothing for `agentsync
// revert` to undo; it printed an unconditional "applied: N ops" instead of the
// removal-aware headline; it never pruned the collision backups; and it
// printed no translation report, which is what tells the user whether the new
// version still translates (#231).
//
// applyOpts{} is the right zero: the plugin commands define none of `apply`'s
// three flags (--dry-run, --no-git-backup, --agents), so the re-apply is a
// real apply of every enabled agent, with git backup governed by
// [destination_directory_git_backup] exactly as it is for `apply`.
//
// State is loaded INSIDE the pipeline, after the source reload, and
// deliberately not passed in by the caller. loadProjectedForScope can run the
// pending subagent migration, which rewrites this tree's recorded source_id
// values in targets.json — so a *state.Targets read before that call is stale
// the moment it happens, and saving it at the end silently undoes the rewrite.
// That was reachable via `plugin upgrade <id>` on an unmigrated tree; it used
// to fail loudly on a lock deadlock instead, which hid it. The ordering is
// pinned by TestApplyPipelineLoadsStateAfterSourceReload, whose second half
// also requires this function to stay a delegation.
func reapplyAfterPluginChange(cmd *cobra.Command, home string) error {
	if err := runApplyPipeline(cmd, home, applyOpts{}); err != nil {
		return fmt.Errorf("re-apply after plugin upgrade: %w", err)
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

// filterSafeBumps partitions bumps for `plugin upgrade --all --lossless` into
// three sets: those whose candidate version adds no new translation losses
// (safe), those a render judged to add one (lossy), and those that could not be
// evaluated at all (unevaluable). Only `safe` is applied — an evaluation error
// is still excluded conservatively, so a fetch/parse failure can never let a
// lossy bump slip through.
//
// unevaluable is kept SEPARATE from lossy rather than folded into it, even
// though both are excluded, because the two have opposite explanations: a lossy
// bump was measured and found to drop translation, while an unevaluable one was
// never measured. Reporting them as one set produced a "candidate version drops
// translation for an agent" line for a bump nothing had judged, and attached
// losslessTargetingCaveat — whose advice is "the affected agent may not receive
// this plugin; re-run without --lossless" — to a refusal that had no affected
// agent and was not about targeting at all.
func filterSafeBumps(home string, bumps []marketplace.Bump, fetched map[string]map[string]marketplace.PluginEntry, cfg source.Config, userHome string, warn io.Writer) (safe, lossy, unevaluable []marketplace.Bump) {
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
			fmt.Fprintf(warn, "warning: lossless: cannot evaluate %s (%v); excluding to be safe\n", b.ID, err)
			unevaluable = append(unevaluable, b)
			continue
		}
		if isLossy {
			lossy = append(lossy, b)
		} else {
			safe = append(safe, b)
		}
	}
	return safe, lossy, unevaluable
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

	return entryIsLossy(home, bID, mpEntry, marketplaceCacheDir(home, mpName), cfg, reg, agents, userHome)
}

// entryIsLossy is the id-level lossiness check bumpIsLossy delegates to, shared
// with `plugin upgrade <id> --lossless` (which has no marketplace.Bump to work
// from — it upgrades to whatever the marketplace currently advertises). It
// compares the skip identities rendered from the plugin's INSTALLED cache
// against those from a freshly-fetched candidate in a throwaway dir, and
// reports whether the candidate adds one.
func entryIsLossy(home, id string, mpEntry marketplace.PluginEntry, mpCacheRoot string, cfg source.Config, reg *adapter.Registry, agents []string, userHome string) (bool, error) {
	oldSkips, err := projectedSkips(mpEntry, pluginCacheDir(home, id), cfg, reg, agents, userHome)
	if err != nil {
		return false, err
	}

	// Fetch the candidate into a throwaway temp dir (never the live cache).
	src := mpEntry.Source
	if src.Relative != "" {
		src.Relative = filepath.Join(mpCacheRoot, src.Relative)
		src.RootDir = mpCacheRoot
	}
	tmp, err := os.MkdirTemp("", "agentsync-lossless-")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if _, err := marketplace.Dispatch(src).Fetch(src, tmp); err != nil {
		return false, fmt.Errorf("fetch candidate %s: %w", id, err)
	}
	newSkips, err := projectedSkips(mpEntry, tmp, cfg, reg, agents, userHome)
	if err != nil {
		return false, err
	}

	for skipID := range newSkips {
		if !oldSkips[skipID] {
			return true, nil
		}
	}
	return false, nil
}

// losslessTargetingCaveat qualifies every `--lossless` refusal that a render
// actually judged lossy. A bump excluded because it could not be EVALUATED does
// not carry it: there is no identified loss and no affected agent, so neither
// half of the caveat's advice applies.
//
// The lossiness probe renders the plugin for every ENABLED agent, from a
// canonical that carries no plugin provenance (see the KNOWN GAP on
// projectedSkips), so `render.Plan`'s per-agent narrowing does not run: a skip
// on an agent this plugin's `agents` / `native_agents` exclude still counts as
// loss and still refuses the upgrade. That refusal is safe — it declines rather
// than performs — but on its own it is undiagnosable, because the user sees an
// upgrade blocked over an agent they know does not receive this plugin, with
// nothing in the message to search for.
//
// Naming the caveat is deliberately NOT a fix. The fix is to stamp provenance
// on the probe's canonical, which means threading the plugin's targeting lists
// down through entryIsLossy's callers; until then the honest thing is to say
// what the check did and did not consider.
// The text opens capitalized: Detailf is UNLABELED, so unlike Warnf/Infof no
// level label supplies the sentence start (cf. the sibling prose Detailf in
// revert.go).
const losslessTargetingCaveat = "This check renders every ENABLED agent and does not honour the plugin's " +
	"`agents` / `native_agents` targeting, so the loss may fall on an agent this plugin is not " +
	"projected to. Check `agentsync plugin explain <id>`; if the affected agent is not listed as " +
	"receiving it, the upgrade is safe for you — re-run without --lossless."

// projectedSkips projects a plugin via marketplace.Project — the SAME single
// projector apply now uses (marketplace.LoadProjected) — and returns the set of
// "agent\x00component\x00name" skip identities rendering just that plugin's
// components for the given agents would emit. Using apply's projector keeps the
// lossiness decision faithful to what apply will actually render. Skips are
// structural (independent of resolved secret values), so the templated render
// is sufficient and no secrets backend is required.
//
// KNOWN GAP: the mini canonical is built from raw marketplace.Project output,
// which is NOT stamped with plugin provenance (that happens in
// namespaceProjected, on the loadprojected path). So every component here has an
// empty Plugin, source.PluginTargetsAgent returns true, and render.Plan's
// per-agent narrowing is a no-op — meaning this probe can report an upgrade as
// lossy because of a skip on an agent the plugin's `agents` / `native_agents`
// exclude. This GATES BEHAVIOUR, not just a warning: pluginUpgradeRun returns
// without upgrading and filterSafeBumps drops the bump. It errs safe (it
// declines rather than performs), but a user can be blocked over an agent that
// never receives this plugin — which is why every such refusal carries
// losslessTargetingCaveat. Closing the gap means threading the plugin's two
// targeting lists down from entryIsLossy's callers. Deliberately deferred; fix
// it there rather than by re-deriving the gates here, which would be a second
// place they can drift.
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
