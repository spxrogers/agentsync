package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/state"
)

func newMarketplaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "marketplace",
		Short: "manage plugin marketplaces",
	}
	cmd.AddCommand(
		newMarketplaceAddCmd(),
		newMarketplaceRemoveCmd(),
		newMarketplaceListCmd(),
	)
	return cmd
}

// marketplaceTOML is the shape of marketplaces/<name>.toml.
type marketplaceTOML struct {
	Marketplace marketplaceTOMLSpec `toml:"marketplace"`
}

type marketplaceTOMLSpec struct {
	URL               string `toml:"url"`
	Ref               string `toml:"ref,omitempty"`
	DefaultUpdateMode string `toml:"default_update_mode,omitempty"`
	HeadSHA           string `toml:"head_sha,omitempty"`
	Name              string `toml:"name,omitempty"`
}

// ---- add --------------------------------------------------------------------

func newMarketplaceAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <url-or-path>",
		Short: "fetch a marketplace and register it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error { return marketplaceAddRun(cmd, args) })
		},
	}
}

func marketplaceAddRun(cmd *cobra.Command, args []string) error {
	rawURL := args[0]
	home := paths.AgentsyncHome(paths.OSEnv{})

	// Build the Source from the raw URL/path argument.
	src, err := parseMarketplaceSource(rawURL)
	if err != nil {
		return err
	}

	// Derive a slug for the marketplace.
	slug := deriveMarketplaceSlug(rawURL)
	cacheDir := marketplaceCacheDir(home, slug)

	// Fetch into cache.
	fetcher := marketplace.Dispatch(src)
	result, err := fetcher.Fetch(src, cacheDir)
	if err != nil {
		return fmt.Errorf("fetch marketplace %s: %w", rawURL, err)
	}

	// Read marketplace.json to extract the declared name.
	mpName := slug
	mpJSONPath := filepath.Join(cacheDir, ".claude-plugin", "marketplace.json")
	if data, err := os.ReadFile(mpJSONPath); err == nil {
		var mp marketplace.Marketplace
		if json.Unmarshal(data, &mp) == nil && mp.Name != "" {
			// Check reserved names.
			for _, reserved := range marketplace.ReservedMarketplaceNames {
				if mp.Name == reserved {
					fmt.Fprintf(cmd.OutOrStdout(), "warning: marketplace name %q is reserved\n", mp.Name)
				}
			}
			mpName = sanitizeSlug(mp.Name)
		}
	}

	// If slug derived from URL differs from declared name, re-cache under declared name.
	if mpName != slug {
		newCacheDir := marketplaceCacheDir(home, mpName)
		if newCacheDir != cacheDir {
			if err := os.MkdirAll(filepath.Dir(newCacheDir), 0o755); err == nil {
				_ = os.Rename(cacheDir, newCacheDir)
			}
		}
	}

	// Write marketplaces/<name>.toml.
	mp := marketplaceTOML{
		Marketplace: marketplaceTOMLSpec{
			URL:               rawURL,
			DefaultUpdateMode: "track",
			HeadSHA:           result.HeadSHA,
			Name:              mpName,
		},
	}
	data, err := toml.Marshal(mp)
	if err != nil {
		return err
	}
	mpPath := filepath.Join(home, "marketplaces", mpName+".toml")
	if err := iox.AtomicWrite(mpPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", mpPath, err)
	}

	// Update state.json so the update command can track fetch timestamps and SHAs.
	statePath := filepath.Join(home, ".state", "targets.json")
	st, _ := state.Load(statePath) // best-effort; ignore read errors on fresh home
	if st == nil {
		st = state.New()
	}
	st.Marketplaces[mpName] = state.Marketplace{
		URL:       rawURL,
		HeadSHA:   result.HeadSHA,
		FetchedAt: time.Now().UTC(),
	}
	_ = state.Save(statePath, st) // best-effort; don't fail add on state write errors

	fmt.Fprintf(cmd.OutOrStdout(), "added marketplace %s (sha=%s)\n",
		mpName, truncate(result.HeadSHA, 12))
	return nil
}

// ---- remove -----------------------------------------------------------------

func newMarketplaceRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "remove a marketplace and its cached files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error { return marketplaceRemoveRun(cmd, args) })
		},
	}
}

func marketplaceRemoveRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := validateCacheKey("marketplace", name); err != nil {
		return err
	}
	home := paths.AgentsyncHome(paths.OSEnv{})

	tomlPath := filepath.Join(home, "marketplaces", name+".toml")
	if err := os.Remove(tomlPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", tomlPath, err)
	}

	cacheDir := marketplaceCacheDir(home, name)
	if err := os.RemoveAll(cacheDir); err != nil {
		return fmt.Errorf("remove cache %s: %w", cacheDir, err)
	}

	// Remove from state.json (best-effort).
	statePath := filepath.Join(home, ".state", "targets.json")
	if st, err := state.Load(statePath); err == nil {
		delete(st.Marketplaces, name)
		_ = state.Save(statePath, st)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "removed marketplace %s\n", name)
	return nil
}

// ---- list -------------------------------------------------------------------

func newMarketplaceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list registered marketplaces",
		Args:  cobra.NoArgs,
		RunE:  marketplaceListRun,
	}
}

func marketplaceListRun(cmd *cobra.Command, _ []string) error {
	home := paths.AgentsyncHome(paths.OSEnv{})
	dir := filepath.Join(home, "marketplaces")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", dir, err)
	}

	var names []string
	mps := map[string]marketplaceTOMLSpec{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var mp marketplaceTOML
		if err := toml.Unmarshal(data, &mp); err != nil {
			continue
		}
		names = append(names, name)
		mps[name] = mp.Marketplace
	}
	sort.Strings(names)

	if len(names) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "(no marketplaces registered; try: agentsync marketplace add <url>)")
		return nil
	}
	for _, name := range names {
		mp := mps[name]
		fmt.Fprintf(cmd.OutOrStdout(), "%-20s url=%-40s sha=%s\n",
			name, mp.URL, truncate(mp.HeadSHA, 12))
	}
	return nil
}

// ---- helpers ----------------------------------------------------------------

// parseMarketplaceSource converts a user-provided URL/path string into a Source.
// Handles:
//   - github:owner/repo[@ref]     → github source
//   - https://...                 → url source
//   - file://... or /abs/path     → relative source
//   - ./rel/path                  → relative source
//
// An input that does not match any known form returns an error rather
// than silently degrading to a relative-path copy. The original behaviour
// turned a typo like `gh:obra/superpowers` (should be `github:`) into
// `cp -r ./gh:obra/superpowers ...`, creating an empty marketplace
// cache with no diagnostic. We now refuse and tell the user the
// supported forms.
func parseMarketplaceSource(rawURL string) (marketplace.Source, error) {
	// github: shorthand
	if strings.HasPrefix(rawURL, "github:") {
		rest := strings.TrimPrefix(rawURL, "github:")
		ref := ""
		if idx := strings.Index(rest, "@"); idx >= 0 {
			ref = rest[idx+1:]
			rest = rest[:idx]
		}
		if rest == "" {
			return marketplace.Source{}, fmt.Errorf("github source missing owner/repo: %q", rawURL)
		}
		return marketplace.Source{Kind: "github", Repo: rest, Ref: ref}, nil
	}

	// file:// URL → treat as relative (strip file:// prefix)
	if strings.HasPrefix(rawURL, "file://") {
		return marketplace.Source{Relative: strings.TrimPrefix(rawURL, "file://")}, nil
	}

	// Absolute path or ./relative
	if strings.HasPrefix(rawURL, "/") || strings.HasPrefix(rawURL, "./") || strings.HasPrefix(rawURL, "../") {
		return marketplace.Source{Relative: rawURL}, nil
	}

	// https:// or http:// → url source
	if strings.HasPrefix(rawURL, "https://") || strings.HasPrefix(rawURL, "http://") {
		return marketplace.Source{Kind: "url", URL: rawURL}, nil
	}

	// Existing local path? (covers bare directory names typed without ./)
	if info, err := os.Stat(rawURL); err == nil && info.IsDir() {
		return marketplace.Source{Relative: rawURL}, nil
	}

	return marketplace.Source{}, fmt.Errorf("unrecognised marketplace source %q; supported forms: github:owner/repo[@ref], https://…, file://…, /abs/path, ./rel/path", rawURL)
}

// deriveMarketplaceSlug derives a filesystem-safe slug from a URL or path.
func deriveMarketplaceSlug(rawURL string) string {
	// Strip common prefixes.
	s := rawURL
	for _, pfx := range []string{"https://", "http://", "file://", "github:"} {
		s = strings.TrimPrefix(s, pfx)
	}
	// Strip trailing .git
	s = strings.TrimSuffix(s, ".git")
	// Replace path separators with dashes.
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	// Strip leading ./ or ../
	s = strings.TrimPrefix(s, ".-")
	s = strings.TrimPrefix(s, "..-")
	// Collapse multiple dashes.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if s == "" {
		s = "marketplace"
	}
	return sanitizeSlug(s)
}

// sanitizeSlug makes a string safe for use as a filename.
func sanitizeSlug(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if r == '/' || r == '.' || r == ' ' {
			b.WriteRune('-')
		}
	}
	result := b.String()
	// Collapse runs of dashes.
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return strings.Trim(result, "-")
}
