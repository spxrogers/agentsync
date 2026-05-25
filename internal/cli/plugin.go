package cli

import (
	"encoding/json"
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
)

func newPluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "manage plugins from marketplaces",
	}
	cmd.AddCommand(
		newPluginInstallCmd(),
		newPluginUpgradeCmd(),
		newPluginEnableCmd(),
		newPluginDisableCmd(),
		newPluginRemoveCmd(),
		newPluginListCmd(),
	)
	return cmd
}

// pluginTOML is the shape of plugins/<id>.toml.
type pluginTOML struct {
	Plugin pluginTOMLSpec `toml:"plugin"`
}

type pluginTOMLSpec struct {
	ID          string   `toml:"id"`
	Version     string   `toml:"version,omitempty"`
	ManifestSHA string   `toml:"manifest_sha,omitempty"`
	Update      string   `toml:"update,omitempty"`
	Agents      []string `toml:"agents,omitempty"`
	Disabled    bool     `toml:"disabled,omitempty"`
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

	// Resolve marketplace.json from the marketplace cache.
	mpData, mpEntry, err := resolveMarketplaceEntry(home, mpName, id)
	if err != nil {
		return err
	}

	// Compute cache path.
	cacheDir := pluginCacheDir(home, id)

	// Fetch plugin source into cache.
	src := mpEntry.Source
	// For relative sources, resolve relative to the marketplace cache root
	// AND constrain the fetcher so a hostile entry cannot escape that root.
	if src.Relative != "" {
		mpCacheRoot := marketplaceCacheDir(home, mpName)
		src.Relative = filepath.Join(mpCacheRoot, src.Relative)
		src.RootDir = mpCacheRoot
	}
	fetcher := marketplace.Dispatch(src)
	result, err := fetcher.Fetch(src, cacheDir)
	if err != nil {
		return fmt.Errorf("fetch plugin %s: %w", id, err)
	}

	// Compute manifest SHA.
	manifestSHA := computeManifestSHA(home, id, mpEntry, mpData, cacheDir)
	if result.HeadSHA != "" && manifestSHA == "" {
		manifestSHA = result.HeadSHA
	}

	// Write plugins/<id>.toml.
	pluginPath := filepath.Join(home, "plugins", id+".toml")
	spec := pluginTOMLSpec{
		ID:          id + "@" + resolveMarketplaceName(mpName),
		Version:     mpEntry.Version,
		ManifestSHA: manifestSHA,
		Update:      "track",
		Agents:      []string{"*"},
	}
	if result.Version != "" {
		spec.Version = result.Version
	}
	data, err := toml.Marshal(pluginTOML{Plugin: spec})
	if err != nil {
		return fmt.Errorf("marshal plugin toml: %w", err)
	}
	if err := iox.AtomicWrite(pluginPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", pluginPath, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "installed plugin %s (version=%s sha=%s)\n",
		id, spec.Version, truncate(spec.ManifestSHA, 12))
	return nil
}

// ---- upgrade ----------------------------------------------------------------

func newPluginUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade <id>",
		Short: "re-fetch a plugin and update its manifest sha",
		Args:  cobra.ExactArgs(1),
		RunE:  lockedRun(pluginUpgradeRun),
	}
}

func pluginUpgradeRun(cmd *cobra.Command, args []string) error {
	id := args[0]
	if err := validateCacheKey("plugin", id); err != nil {
		return err
	}
	home := paths.AgentsyncHome(paths.OSEnv{})

	// Read existing plugin toml.
	pluginPath := filepath.Join(home, "plugins", id+".toml")
	existing, err := readPluginTOML(pluginPath)
	if err != nil {
		return err
	}

	// Parse marketplace name from the stored id "name@marketplace".
	_, mpName := splitPluginRef(existing.Plugin.ID)

	// Re-resolve marketplace entry.
	mpData, mpEntry, err := resolveMarketplaceEntry(home, mpName, id)
	if err != nil {
		return err
	}

	cacheDir := pluginCacheDir(home, id)
	// Remove old cache so we get a fresh fetch.
	_ = os.RemoveAll(cacheDir)

	src := mpEntry.Source
	if src.Relative != "" {
		mpCacheRoot := marketplaceCacheDir(home, mpName)
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
	if mpEntry.Version != "" {
		existing.Plugin.Version = mpEntry.Version
	}
	if result.Version != "" {
		existing.Plugin.Version = result.Version
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
	return nil
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
	id := args[0]
	if err := validateCacheKey("plugin", id); err != nil {
		return err
	}
	home := paths.AgentsyncHome(paths.OSEnv{})
	pluginPath := filepath.Join(home, "plugins", id+".toml")

	existing, err := readPluginTOML(pluginPath)
	if err != nil {
		return err
	}
	existing.Plugin.Disabled = false
	if len(existing.Plugin.Agents) == 0 {
		existing.Plugin.Agents = []string{"*"}
	}
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
	id := args[0]
	if err := validateCacheKey("plugin", id); err != nil {
		return err
	}
	home := paths.AgentsyncHome(paths.OSEnv{})
	pluginPath := filepath.Join(home, "plugins", id+".toml")

	existing, err := readPluginTOML(pluginPath)
	if err != nil {
		return err
	}
	existing.Plugin.Disabled = true
	existing.Plugin.Agents = []string{}
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
	id := args[0]
	if err := validateCacheKey("plugin", id); err != nil {
		return err
	}
	home := paths.AgentsyncHome(paths.OSEnv{})

	pluginPath := filepath.Join(home, "plugins", id+".toml")
	if err := os.Remove(pluginPath); err != nil {
		// Match upgrade/enable/disable: a typo'd or already-removed id is an
		// error, not a cheerful "removed plugin X" no-op.
		if os.IsNotExist(err) {
			return fmt.Errorf("plugin %q is not installed", id)
		}
		return fmt.Errorf("remove %s: %w", pluginPath, err)
	}

	cacheDir := pluginCacheDir(home, id)
	if err := os.RemoveAll(cacheDir); err != nil {
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
			name, p.Version, truncate(p.ManifestSHA, 12), status)
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

// resolveMarketplaceName returns the marketplace name, defaulting to "default"
// if empty.
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
// and finds the named plugin entry. Returns the raw bytes (for SHA computation)
// and the entry.
func resolveMarketplaceEntry(home, mpName, pluginID string) ([]byte, marketplace.PluginEntry, error) {
	if mpName == "" {
		// No marketplace specified — look in all marketplace caches.
		return searchAllMarketplaces(home, pluginID)
	}

	mpCacheDir := marketplaceCacheDir(home, mpName)
	mpJSONPath := filepath.Join(mpCacheDir, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(mpJSONPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, marketplace.PluginEntry{}, fmt.Errorf(
				"marketplace %q not found in cache; run: agentsync marketplace add <url>", mpName,
			)
		}
		return nil, marketplace.PluginEntry{}, fmt.Errorf("read %s: %w", mpJSONPath, err)
	}

	var mp marketplace.Marketplace
	if err := json.Unmarshal(data, &mp); err != nil {
		return nil, marketplace.PluginEntry{}, fmt.Errorf("parse marketplace.json: %w", err)
	}

	for _, entry := range mp.Plugins {
		if entry.Name == pluginID {
			return data, entry, nil
		}
	}
	return nil, marketplace.PluginEntry{}, fmt.Errorf("plugin %q not found in marketplace %q", pluginID, mpName)
}

// searchAllMarketplaces scans all cached marketplace.json files for a plugin.
func searchAllMarketplaces(home, pluginID string) ([]byte, marketplace.PluginEntry, error) {
	cacheRoot := filepath.Join(home, ".state", "cache", "marketplaces")
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, marketplace.PluginEntry{}, fmt.Errorf(
				"no marketplaces cached; run: agentsync marketplace add <url>",
			)
		}
		return nil, marketplace.PluginEntry{}, err
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
			if entry.Name == pluginID {
				return data, entry, nil
			}
		}
	}
	return nil, marketplace.PluginEntry{}, fmt.Errorf("plugin %q not found in any cached marketplace", pluginID)
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
