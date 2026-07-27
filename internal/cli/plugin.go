package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

func newPluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "manage plugins from marketplaces",
	}
	cmd.AddCommand(
		newPluginInstallCmd(),
		newPluginOutdatedCmd(),
		newPluginUpgradeCmd(),
		newPluginEnableCmd(),
		newPluginDisableCmd(),
		newPluginRemoveCmd(),
		newPluginListCmd(),
		newPluginExplainCmd(),
	)
	// Plugins are a user-scope concept across every supported harness (Claude
	// installs under ~/.claude, not per-repo), and the registry + cache live
	// under ~/.agentsync — so the group refuses scope flags. `plugin upgrade`
	// is the one exception: its re-apply step honors scope, and re-marks itself.
	markGroupScopeUnaware(cmd, "plugins are a user-scope concept — the registry (plugins/*.toml) and cache "+
		"live under ~/.agentsync. A project tree can only DISABLE a plugin, by committing plugins/<id>.toml with `disabled = true`")
	markScopeAware(findSub(cmd, "upgrade"))
	return cmd
}

// findSub returns the named direct subcommand, or nil. Used to re-mark one
// member of a group whose blanket stance does not apply to it.
func findSub(parent *cobra.Command, name string) *cobra.Command {
	for _, sub := range parent.Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	return nil
}

// pluginTOML is the shape of plugins/<id>.toml.
type pluginTOML struct {
	Plugin pluginTOMLSpec `toml:"plugin"`
}

type pluginTOMLSpec struct {
	// ID/Version mirror source.PluginSpec and originate from fetched marketplace
	// metadata, so they are untrusted.Text (sanitize-on-print). The SHA/update
	// fields are agentsync-controlled and stay plain strings.
	ID          untrusted.Text `toml:"id"`
	Version     untrusted.Text `toml:"version,omitempty"`
	ManifestSHA string         `toml:"manifest_sha,omitempty"`
	Update      string         `toml:"update,omitempty"`
	Agents      []string       `toml:"agents,omitempty"`
	Disabled    bool           `toml:"disabled,omitempty"`
}

// ---- install ----------------------------------------------------------------

func newPluginInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install <id[@marketplace]>",
		Short: "fetch a plugin and register it",
		Args:  cobra.ExactArgs(1),
		RunE:  lockedRun(pluginInstallRun),
	}
}

func pluginInstallRun(cmd *cobra.Command, args []string) error {
	id, mpName := splitPluginRef(args[0])
	if err := validateCacheKey("plugin", id); err != nil {
		return err
	}
	if mpName != "" {
		if err := validateCacheKey("marketplace", mpName); err != nil {
			return err
		}
	}
	home := paths.AgentsyncHome(paths.OSEnv{})

	spec, err := installPluginInto(home, id, mpName)
	if err != nil {
		return err
	}
	// spec.Version is untrusted.Text from the fetched manifest, so printing it via
	// %s sanitizes it by construction — a control sequence in the version string
	// cannot smuggle terminal escapes into the status line. (id is the user's own
	// argument and sha is hex, so neither needs it.)
	fmt.Fprintf(cmd.OutOrStdout(), "installed plugin %s (version=%s sha=%s)",
		id, spec.Version, truncate(spec.ManifestSHA, 12))
	// When a re-install PRESERVED user-authored lifecycle fields that differ from
	// the first-install defaults, surface it instead of reporting an unqualified
	// success — the user's fan-out scope / update policy / disabled state was kept,
	// not reset (issue #140). Agents/Update values are user-authored canonical
	// config; route them through ui.Sanitize so a hand-edited control byte cannot
	// smuggle terminal escapes into the status line, mirroring the Version guard.
	if kept := keptLifecycleSummary(spec); kept != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " (kept %s)", kept)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	return nil
}

// keptLifecycleSummary describes which user-authored lifecycle fields a
// re-install preserved that differ from the first-install defaults
// (Agents=["*"], Update="track", Disabled=false). It returns "" when the written
// spec matches those defaults — a genuine first install, or a re-install over a
// TOML that already held the defaults — so the install status line only mentions
// preserved state when there is some to mention.
func keptLifecycleSummary(spec pluginTOMLSpec) string {
	var parts []string
	defaultAgents := len(spec.Agents) == 1 && spec.Agents[0] == "*"
	if !defaultAgents {
		sanitized := make([]string, len(spec.Agents))
		for i, a := range spec.Agents {
			sanitized[i] = ui.Sanitize(a)
		}
		parts = append(parts, fmt.Sprintf("agents=[%s]", strings.Join(sanitized, ",")))
	}
	if spec.Update != "track" {
		parts = append(parts, fmt.Sprintf("update=%s", ui.Sanitize(spec.Update)))
	}
	if spec.Disabled {
		parts = append(parts, "disabled=true")
	}
	return strings.Join(parts, ", ")
}

// installPluginInto fetches plugin id from the named marketplace into the
// plugin cache and writes plugins/<id>.toml, returning the written spec. mpName
// may be empty (the marketplace is then searched for across all caches). It
// does not print or acquire the global lock — callers do. Both `plugin install`
// and `import` use it so the two produce byte-identical canonical artifacts.
func installPluginInto(home, id, mpName string) (pluginTOMLSpec, error) {
	// Resolve marketplace.json from the marketplace cache.
	mpData, mpEntry, resolvedMP, err := resolveMarketplaceEntry(home, mpName, id)
	if err != nil {
		return pluginTOMLSpec{}, err
	}

	// Compute cache path.
	cacheDir := pluginCacheDir(home, id)

	// Fetch plugin source into cache.
	src := mpEntry.Source
	// For relative sources, resolve relative to the marketplace cache root
	// AND constrain the fetcher so a hostile entry cannot escape that root.
	// resolvedMP, not mpName: a bare-id install (mpName == "") found the entry
	// by searching the caches, and the relative source must resolve against
	// the marketplace that actually supplied it — the raw "" would sanitize to
	// the nonexistent cache dir "_" and fail the fetch.
	if src.Relative != "" {
		mpCacheRoot := marketplaceCacheDir(home, resolvedMP)
		src.Relative = filepath.Join(mpCacheRoot, src.Relative)
		src.RootDir = mpCacheRoot
	}
	fetcher := marketplace.Dispatch(src)
	result, err := fetcher.Fetch(src, cacheDir)
	if err != nil {
		return pluginTOMLSpec{}, fmt.Errorf("fetch plugin %s: %w", id, err)
	}

	// Compute manifest SHA.
	manifestSHA := computeManifestSHA(home, id, mpEntry, mpData, cacheDir)
	if result.HeadSHA != "" && manifestSHA == "" {
		manifestSHA = result.HeadSHA
	}

	// Write plugins/<id>.toml.
	pluginPath := filepath.Join(home, "plugins", id+".toml")

	// Lifecycle fields (Agents allowlist, Update policy, Disabled bit) are
	// user-authored, security-relevant on-disk state: the allowlist scopes which
	// agents a credential-bearing plugin fans out to. A re-install over an
	// existing plugins/<id>.toml must PRESERVE them rather than reset them to the
	// first-install defaults — silently re-broadening a narrowed allowlist would
	// ship those credentials to agents the user deliberately excluded (issue
	// #140). Only ID/Version/ManifestSHA are legitimately refreshed by install.
	//
	// A genuine first install (no prior TOML — a not-exist read falls through
	// here) keeps the historical defaults byte-for-byte, so `install` and
	// `import` still produce byte-identical canonical artifacts (see the doc
	// comment above): Agents=["*"], Update="track", Disabled absent.
	agents := []string{"*"}
	update := "track"
	disabled := false
	switch existing, rerr := readPluginTOML(pluginPath); {
	case rerr == nil:
		if len(existing.Plugin.Agents) > 0 {
			agents = existing.Plugin.Agents
		}
		if existing.Plugin.Update != "" {
			update = existing.Plugin.Update
		}
		disabled = existing.Plugin.Disabled
	case !errors.Is(rerr, os.ErrNotExist):
		// The file EXISTS but is unparseable. Falling through to the ["*"]/track/false
		// defaults would silently re-broaden a narrowed allowlist — the exact #140 loss
		// this preserve path prevents — so refuse rather than reset a corrupt-but-present
		// lifecycle file. The user can fix or remove it and re-install. ("Unparseable"
		// here means a TOML *parse error*; a semantically-empty file — blank or
		// comment-only — parses to rerr==nil above and legitimately carries no allowlist
		// to preserve, so it falls through to defaults. agentsync's own iox.AtomicWrite
		// never produces such a file; only external truncation would.)
		return pluginTOMLSpec{}, fmt.Errorf("existing %s is unreadable (%w); refusing to overwrite its agents/update/disabled with defaults — fix or remove it, then re-install", pluginPath, rerr)
	}
	// A genuine first install (readPluginTOML returned os.ErrNotExist) keeps the
	// byte-identical defaults so install/import still produce identical artifacts.

	spec := pluginTOMLSpec{
		// id/marketplace are validated cache keys; wrap the composed id as Text to
		// match the canonical model (the ManifestSHA below stays a plain string).
		ID:          untrusted.Wrap(id + "@" + resolveMarketplaceName(mpName)),
		Version:     mpEntry.Version,
		ManifestSHA: manifestSHA,
		Update:      update,
		Agents:      agents,
		Disabled:    disabled,
	}
	if result.Version != "" {
		spec.Version = untrusted.Wrap(result.Version)
	}
	data, err := toml.Marshal(pluginTOML{Plugin: spec})
	if err != nil {
		return pluginTOMLSpec{}, fmt.Errorf("marshal plugin toml: %w", err)
	}
	if err := iox.AtomicWrite(pluginPath, data, 0o644); err != nil {
		return pluginTOMLSpec{}, fmt.Errorf("write %s: %w", pluginPath, err)
	}
	return spec, nil
}

// ---- outdated ---------------------------------------------------------------

// newPluginOutdatedCmd polls the registered marketplaces and reports pending
// version bumps. It is `agentsync update`'s read side, moved under the noun it
// operates on (#200 F2).
func newPluginOutdatedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "outdated",
		Short: "(network) poll marketplaces and report pending plugin version bumps",
		Long: `outdated re-fetches every registered marketplace, computes which installed
plugins have newer versions available, and prints the pending bumps.

It does NOT touch agent configs — run 'agentsync plugin upgrade --all' for that.
It is not, however, a pure read: unlike the 'npm outdated' its name evokes, this
command USES THE NETWORK (it re-fetches each marketplace into the cache) and
WRITES STATE (each marketplace's fetch timestamp and head SHA land in
.state/targets.json). It also re-checks every installed plugin's manifest SHA
and warns when a plugin was re-uploaded at the same version with different
content.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error {
				return pollPluginsRun(cmd, pollOpts{})
			})
		},
	}
}

// ---- upgrade ----------------------------------------------------------------

// newPluginUpgradeCmd upgrades one plugin, or every plugin with a pending bump
// (--all). BOTH forms finish by re-applying to the agents: one verb, one ending
// state. That is a behavior change to the pre-#200 `plugin upgrade <id>`, which
// re-fetched and left the agents stale until the next `apply`; leaving the id
// form fetch-only while --all re-applied would make the same verb mean two
// different things.
func newPluginUpgradeCmd() *cobra.Command {
	var (
		all      bool
		lossless bool
	)
	cmd := &cobra.Command{
		Use:   "upgrade [<id>]",
		Short: "(network) re-fetch a plugin (or every pending bump) and re-apply",
		Long: `upgrade re-fetches a plugin, updates its recorded version + manifest sha,
and re-applies the result to your agents so the upgrade lands in the same
command.

With --all it first polls every registered marketplace (like 'plugin outdated'),
then upgrades every plugin with a pending bump and re-applies once.

--lossless only upgrades when the candidate version introduces no NEW
translation loss (an adapter skip) for an enabled agent, judged by projecting
the plugin exactly as apply does and diffing the skips. An excluded upgrade is
reported, never silently dropped.

The same scope/project resolution as 'agentsync apply' is used (--scope project
or --project <path>; default user) so the re-render lands in the right place
when running inside a project.`,
		Args: cobra.MaximumNArgs(1),
		RunE: lockedRun(func(cmd *cobra.Command, args []string) error {
			switch {
			case all && len(args) > 0:
				return fmt.Errorf("--all does not take a plugin id; it upgrades every plugin with a pending bump")
			case !all && len(args) == 0:
				return fmt.Errorf("upgrade needs a plugin id, or --all to upgrade every plugin with a pending bump")
			case all:
				return pollPluginsRun(cmd, pollOpts{
					doApply:      true,
					lossless:     lossless,
					losslessFlag: "--lossless",
					applyFlag:    "--all",
				})
			}
			return pluginUpgradeRun(cmd, args, lossless)
		}),
	}
	cmd.Flags().BoolVar(&all, "all", false, "poll marketplaces and upgrade every plugin with a pending bump")
	cmd.Flags().BoolVar(&lossless, "lossless", false, "only upgrade when the candidate version introduces no new translation loss")
	markScopeAware(cmd)
	return cmd
}

func pluginUpgradeRun(cmd *cobra.Command, args []string, lossless bool) error {
	// Accept the id@marketplace ref that `install` accepts; operate on the bare id
	// (the on-disk file is plugins/<id>.toml). The stored id (below) is
	// authoritative for the marketplace; a typed qualifier must MATCH it (checked
	// below) rather than being silently ignored.
	id, typedMP := splitPluginRef(args[0])
	if err := validateCacheKey("plugin", id); err != nil {
		return err
	}
	home := paths.AgentsyncHome(paths.OSEnv{})

	// Read existing plugin toml.
	pluginPath := filepath.Join(home, "plugins", id+".toml")
	existing, err := readPluginTOML(pluginPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("plugin %q is not installed", id)
		}
		return err
	}
	if err := checkPluginRefMarketplace(existing, id, typedMP); err != nil {
		return err
	}

	// Parse marketplace name from the stored id "name@marketplace". The
	// "default" sentinel (bare-id install) names no real marketplace — map it
	// to "" so the entry is re-resolved by searching all caches, exactly like
	// the original install.
	_, mpName := splitPluginRef(existing.Plugin.ID.Unverified())
	if mpName == "default" {
		mpName = ""
	}

	// Re-resolve marketplace entry.
	mpData, mpEntry, resolvedMP, err := resolveMarketplaceEntry(home, mpName, id)
	if err != nil {
		return err
	}

	// --lossless: judge the candidate BEFORE touching the live cache. The check
	// renders the installed cache and a throwaway fetch of the candidate and
	// compares skip identities; a candidate that adds one is reported and the
	// upgrade is refused, leaving cache + TOML exactly as they were. An
	// evaluation failure is conservatively lossy, matching --all's filter.
	if lossless {
		userHome := paths.HomeDir(paths.OSEnv{})
		c, cerr := source.Load(afero.NewOsFs(), home)
		if cerr != nil {
			return fmt.Errorf("load source: %w", cerr)
		}
		var agents []string
		for name, ag := range c.Config.Agents {
			if ag.Enabled {
				agents = append(agents, name)
			}
		}
		isLossy, lerr := entryIsLossy(home, id, mpEntry, marketplaceCacheDir(home, resolvedMP), c.Config, registryFactory(), agents, userHome)
		switch {
		case lerr != nil:
			return fmt.Errorf("--lossless: cannot evaluate %s (%w); refusing the upgrade to be safe", id, lerr)
		case isLossy:
			fmt.Fprintf(cmd.OutOrStdout(),
				"lossless: skipping lossy upgrade %s (candidate version drops translation for an agent)\n", id)
			return nil
		}
	}

	cacheDir := pluginCacheDir(home, id)
	// Remove old cache so we get a fresh fetch.
	_ = os.RemoveAll(cacheDir) //nolint:forbidigo // clears the plugin cache under .state/cache for a fresh fetch, not a native destination

	src := mpEntry.Source
	if src.Relative != "" {
		mpCacheRoot := marketplaceCacheDir(home, resolvedMP)
		src.Relative = filepath.Join(mpCacheRoot, src.Relative)
		src.RootDir = mpCacheRoot
	}
	fetcher := marketplace.Dispatch(src)
	result, err := fetcher.Fetch(src, cacheDir)
	if err != nil {
		return fmt.Errorf("fetch plugin %s: %w", id, err)
	}

	manifestSHA := computeManifestSHA(home, id, mpEntry, mpData, cacheDir)
	if result.HeadSHA != "" && manifestSHA == "" {
		manifestSHA = result.HeadSHA
	}

	existing.Plugin.ManifestSHA = manifestSHA
	if !mpEntry.Version.Empty() {
		existing.Plugin.Version = mpEntry.Version
	}
	if result.Version != "" {
		existing.Plugin.Version = untrusted.Wrap(result.Version)
	}

	data, err := toml.Marshal(existing)
	if err != nil {
		return err
	}
	if err := iox.AtomicWrite(pluginPath, data, 0o644); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "upgraded plugin %s (sha=%s)\n",
		id, truncate(manifestSHA, 12))

	// Re-apply so the upgraded plugin's components reach the agents now. Same
	// ending state as `plugin upgrade --all` — see the command's doc comment.
	userHome := paths.HomeDir(paths.OSEnv{})
	statePath := filepath.Join(home, ".state", "targets.json")
	st, err := state.Load(statePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	return reapplyAfterPluginChange(cmd, home, userHome, statePath, st)
}

// ---- enable -----------------------------------------------------------------

func newPluginEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <id>",
		Short: "enable a disabled plugin",
		Args:  cobra.ExactArgs(1),
		RunE:  lockedRun(pluginEnableRun),
	}
}

func pluginEnableRun(cmd *cobra.Command, args []string) error {
	id, typedMP := splitPluginRef(args[0]) // accept id@marketplace like install; a qualifier must match the recorded marketplace
	if err := validateCacheKey("plugin", id); err != nil {
		return err
	}
	home := paths.AgentsyncHome(paths.OSEnv{})
	pluginPath := filepath.Join(home, "plugins", id+".toml")

	existing, err := readPluginTOML(pluginPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("plugin %q is not installed", id)
		}
		return err
	}
	if err := checkPluginRefMarketplace(existing, id, typedMP); err != nil {
		return err
	}
	// Only flip the disabled bit. Do NOT touch Agents: re-materializing ["*"] over
	// a previously-narrowed allowlist silently re-broadens a plugin's
	// credential-bearing components to agents the user excluded (issue #140). An
	// empty/omitted allowlist already means "all agents" downstream, so there is
	// nothing to backfill here.
	existing.Plugin.Disabled = false
	data, err := toml.Marshal(existing)
	if err != nil {
		return err
	}
	if err := iox.AtomicWrite(pluginPath, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "enabled plugin %s\n", id)
	return nil
}

// ---- disable ----------------------------------------------------------------

func newPluginDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <id>",
		Short: "disable a plugin without removing it",
		Args:  cobra.ExactArgs(1),
		RunE:  lockedRun(pluginDisableRun),
	}
}

func pluginDisableRun(cmd *cobra.Command, args []string) error {
	id, typedMP := splitPluginRef(args[0]) // accept id@marketplace like install; a qualifier must match the recorded marketplace
	if err := validateCacheKey("plugin", id); err != nil {
		return err
	}
	home := paths.AgentsyncHome(paths.OSEnv{})
	pluginPath := filepath.Join(home, "plugins", id+".toml")

	existing, err := readPluginTOML(pluginPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("plugin %q is not installed", id)
		}
		return err
	}
	if err := checkPluginRefMarketplace(existing, id, typedMP); err != nil {
		return err
	}
	// Only set the disabled bit. The `disabled = true` flag alone suppresses the
	// plugin's projection at apply (source.PluginSpec.Disabled), so emptying
	// Agents was never necessary — and doing so destroyed the user's narrowed
	// allowlist, which the next `enable` then re-broadened to ["*"] (issue #140).
	// Preserve Agents (and Update) untouched across disable.
	existing.Plugin.Disabled = true
	data, err := toml.Marshal(existing)
	if err != nil {
		return err
	}
	if err := iox.AtomicWrite(pluginPath, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "disabled plugin %s\n", id)
	return nil
}

// ---- remove -----------------------------------------------------------------

func newPluginRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "remove a plugin and its cached files",
		Args:  cobra.ExactArgs(1),
		RunE:  lockedRun(pluginRemoveRun),
	}
}

func pluginRemoveRun(cmd *cobra.Command, args []string) error {
	id, typedMP := splitPluginRef(args[0]) // accept id@marketplace like install; a qualifier must match the recorded marketplace
	if err := validateCacheKey("plugin", id); err != nil {
		return err
	}
	home := paths.AgentsyncHome(paths.OSEnv{})

	pluginPath := filepath.Join(home, "plugins", id+".toml")
	// A qualified ref must name the marketplace the plugin was actually installed
	// from, so read the TOML before deleting anything. (A corrupt TOML therefore
	// refuses a qualified remove — the bare id still removes it.)
	if typedMP != "" {
		existing, err := readPluginTOML(pluginPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("plugin %q is not installed", id)
			}
			return err
		}
		if err := checkPluginRefMarketplace(existing, id, typedMP); err != nil {
			return err
		}
	}
	if err := os.Remove(pluginPath); err != nil { //nolint:forbidigo // removes plugins/<id>.toml (canonical source), not a native destination
		// Match upgrade/enable/disable: a typo'd or already-removed id is an
		// error, not a cheerful "removed plugin X" no-op.
		if os.IsNotExist(err) {
			return fmt.Errorf("plugin %q is not installed", id)
		}
		return fmt.Errorf("remove %s: %w", pluginPath, err)
	}

	cacheDir := pluginCacheDir(home, id)
	if err := os.RemoveAll(cacheDir); err != nil { //nolint:forbidigo // removes the plugin cache under .state/cache, not a native destination
		return fmt.Errorf("remove cache %s: %w", cacheDir, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "removed plugin %s\n", id)
	return nil
}

// ---- list -------------------------------------------------------------------

func newPluginListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list installed plugins",
		Args:  cobra.NoArgs,
		RunE:  pluginListRun,
	}
}

func pluginListRun(cmd *cobra.Command, _ []string) error {
	home := paths.AgentsyncHome(paths.OSEnv{})
	dir := filepath.Join(home, "plugins")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", dir, err)
	}

	var names []string
	plugins := map[string]pluginTOMLSpec{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".toml")
		p, err := readPluginTOML(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		names = append(names, id)
		plugins[id] = p.Plugin
	}
	sort.Strings(names)

	if len(names) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "(no plugins installed; try: agentsync plugin install <id@marketplace>)")
		return nil
	}
	for _, name := range names {
		p := plugins[name]
		status := "enabled"
		if p.Disabled {
			status = "disabled"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-20s version=%-10s sha=%-14s %s\n",
			ui.Sanitize(name), p.Version, truncate(p.ManifestSHA, 12), status)
	}
	return nil
}

// ---- helpers ----------------------------------------------------------------

// splitPluginRef splits "id@marketplace" → (id, marketplace).
// If no "@" is present, marketplace is "".
func splitPluginRef(ref string) (id, mpName string) {
	if idx := strings.LastIndex(ref, "@"); idx >= 0 {
		return ref[:idx], ref[idx+1:]
	}
	return ref, ""
}

// checkPluginRefMarketplace refuses a marketplace-qualified ref whose qualifier
// does not match the marketplace recorded for the installed plugin (the
// "id@marketplace" written to plugins/<id>.toml at install). The lifecycle
// subcommands operate on the bare id, so silently discarding the qualifier let
// `plugin disable demo@wrong-mp` act on the demo installed from another
// marketplace. An unqualified ref (typedMP == "") is unchanged. The recorded
// value comes from a hand-editable file; %q renders any control bytes inert.
func checkPluginRefMarketplace(existing pluginTOML, id, typedMP string) error {
	if typedMP == "" {
		return nil
	}
	_, recorded := splitPluginRef(existing.Plugin.ID.Unverified())
	if recorded == typedMP {
		return nil
	}
	// A bare-id install records the "default" SENTINEL (resolveMarketplaceName:
	// the marketplace was searched across all caches, not named). "default"
	// identifies no real marketplace, so a typed qualifier can be neither
	// confirmed nor contradicted — refuse like the no-record branch below with
	// an honest message, rather than claiming the plugin was "installed from
	// marketplace \"default\"" (a marketplace that doesn't exist; the update
	// flow depends on the sentinel staying recorded as-is, so re-recording the
	// resolved name at install is a bigger change than this guard should make).
	if recorded == "" || recorded == "default" {
		return fmt.Errorf("plugin %q was installed by bare id (its marketplace is not recorded), so the ref %q cannot be verified; use the bare id %q",
			id, id+"@"+typedMP, id)
	}
	return fmt.Errorf("plugin %q is installed from marketplace %q, not %q; use %q or the bare id %q",
		id, recorded, typedMP, id+"@"+recorded, id)
}

// resolveMarketplaceName returns the marketplace name, defaulting to "default"
// if empty. NOTE: "default" is a SENTINEL, not a marketplace — but nothing
// reserves the name, so a user-registered marketplace literally named
// "default" is ambiguous with it (its qualified refs get the "installed by
// bare id" refusal and its upgrades re-search all caches). Accepted residual:
// reserving the name would break any existing marketplace so named, and the
// all-caches search still resolves the right entry.
func resolveMarketplaceName(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

// pluginCacheDir returns the cache directory for a plugin. The id is
// sanitized so a hostile marketplace publishing a plugin named
// "../../etc/foo" cannot cause writes outside .state/cache/plugins/.
func pluginCacheDir(home, id string) string {
	return filepath.Join(home, ".state", "cache", "plugins", sanitizeCacheKey(id))
}

// marketplaceCacheDir returns the cache directory for a marketplace.
// Same sanitization applies — the marketplace name is user-supplied at
// `marketplace add` time and must not escape the cache root.
func marketplaceCacheDir(home, mpName string) string {
	return filepath.Join(home, ".state", "cache", "marketplaces", sanitizeCacheKey(mpName))
}

// sanitizeCacheKey strips path separators and ".." components so the
// returned string is safe to use as a single path segment. Callers can
// also validate up-front via ValidateCacheKey before any filesystem
// operation; this helper is the last line of defense for read-only
// lookups where validation might be too aggressive.
func sanitizeCacheKey(s string) string {
	// Replace separators and walk-up components with an underscore.
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "..", "_")
	if s == "" || s == "." {
		s = "_"
	}
	return s
}

// validateCacheKey is the up-front guard for write paths: it rejects
// plugin / marketplace ids that contain path components which could
// escape the cache root. Callers should prefer this to sanitizeCacheKey
// when the id flows from user input or marketplace metadata so the user
// sees the real problem name in the error, not a silently-mangled one.
func validateCacheKey(kind, s string) error {
	if s == "" {
		return fmt.Errorf("%s id is empty", kind)
	}
	if strings.ContainsAny(s, "/\\") {
		return fmt.Errorf("%s id %q contains path separators", kind, s)
	}
	if s == "." || s == ".." || strings.Contains(s, "..") {
		return fmt.Errorf("%s id %q contains a path-traversal component", kind, s)
	}
	return nil
}

// resolveMarketplaceEntry loads the marketplace's marketplace.json from cache
// and finds the named plugin entry. Returns the raw bytes (for SHA
// computation), the plugin's
// entry, and the RESOLVED marketplace cache name — mpName when one was named,
// or the cache dir the search actually found the plugin in for a bare-id
// lookup (callers must fetch relative sources against the resolved name; the
// raw "" would sanitize to a nonexistent cache dir).
func resolveMarketplaceEntry(home, mpName, pluginID string) ([]byte, marketplace.PluginEntry, string, error) {
	if mpName == "" {
		// No marketplace specified — look in all marketplace caches.
		return searchAllMarketplaces(home, pluginID)
	}

	mpCacheDir := marketplaceCacheDir(home, mpName)
	mpJSONPath := filepath.Join(mpCacheDir, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(mpJSONPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, marketplace.PluginEntry{}, "", fmt.Errorf(
				"marketplace %q not found in cache; run: agentsync marketplace add <url>", mpName,
			)
		}
		return nil, marketplace.PluginEntry{}, "", fmt.Errorf("read %s: %w", mpJSONPath, err)
	}

	var mp marketplace.Marketplace
	if err := json.Unmarshal(data, &mp); err != nil {
		return nil, marketplace.PluginEntry{}, "", fmt.Errorf("parse marketplace.json: %w", err)
	}

	for _, entry := range mp.Plugins {
		if entry.Name.Unverified() == pluginID {
			return data, entry, mpName, nil
		}
	}
	return nil, marketplace.PluginEntry{}, "", fmt.Errorf("plugin %q not found in marketplace %q", pluginID, mpName)
}

// searchAllMarketplaces scans every marketplace cache for pluginID, returning
// the marketplace.json bytes, the entry, and the cache-dir NAME it was found
// in (the resolved marketplace a relative source must fetch against).
func searchAllMarketplaces(home, pluginID string) ([]byte, marketplace.PluginEntry, string, error) {
	cacheRoot := filepath.Join(home, ".state", "cache", "marketplaces")
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, marketplace.PluginEntry{}, "", fmt.Errorf(
				"no marketplaces cached; run: agentsync marketplace add <url>",
			)
		}
		return nil, marketplace.PluginEntry{}, "", err
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mpJSONPath := filepath.Join(cacheRoot, e.Name(), ".claude-plugin", "marketplace.json")
		data, err := os.ReadFile(mpJSONPath)
		if err != nil {
			continue
		}
		var mp marketplace.Marketplace
		if err := json.Unmarshal(data, &mp); err != nil {
			continue
		}
		for _, entry := range mp.Plugins {
			if entry.Name.Unverified() == pluginID {
				return data, entry, e.Name(), nil
			}
		}
	}
	return nil, marketplace.PluginEntry{}, "", fmt.Errorf("plugin %q not found in any cached marketplace", pluginID)
}

// computeManifestSHA records the SHA that the loader's verifyPluginManifestSHA
// will later re-check. The loader ALWAYS recomputes sha256(plugin.json) and has
// no strict flag to branch on, so the recorded SHA MUST use the same formula
// whenever a plugin.json exists — keying on the strict flag instead recorded
// sha256(entry) for non-strict plugins and then hard-failed the very next apply
// with a bogus "manifest SHA mismatch". So: hash plugin.json when present (both
// strict and non-strict), and only fall back to hashing the marketplace entry
// for an entry-only plugin with no plugin.json — a case the loader skips
// verifying anyway (missing plugin.json → nil).
func computeManifestSHA(home, id string, entry marketplace.PluginEntry, mpData []byte, cacheDir string) string {
	pluginJSONPath := filepath.Join(cacheDir, ".claude-plugin", "plugin.json")
	if _, err := os.Stat(pluginJSONPath); err == nil {
		// Hash the whole cache tree (every projected component body, not just
		// plugin.json) so the pin certifies what projection actually ships.
		h, herr := marketplace.PluginTreeHash(afero.NewOsFs(), cacheDir)
		if herr != nil {
			return ""
		}
		return h
	}
	// No plugin.json (entry-only plugin): pin the marketplace entry instead.
	h, err := marketplace.PluginEntryHash(entry)
	if err != nil {
		return ""
	}
	return h
}

// readPluginTOML reads and parses a plugins/<id>.toml file.
func readPluginTOML(path string) (pluginTOML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return pluginTOML{}, fmt.Errorf("read %s: %w", path, err)
	}
	var p pluginTOML
	if err := toml.Unmarshal(data, &p); err != nil {
		return pluginTOML{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return p, nil
}

// truncate shortens a string to n chars.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
