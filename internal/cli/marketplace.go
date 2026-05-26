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

	mpName, headSHA, err := addMarketplaceSource(home, src, rawURL)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "added marketplace %s (sha=%s)\n",
		mpName, truncate(headSHA, 12))
	return nil
}

// addMarketplaceSource fetches src into the marketplace cache, writes
// marketplaces/<name>.toml, and records the fetch in state. It returns the
// resolved marketplace name (the declared name from marketplace.json, falling
// back to a URL-derived slug) and the fetched head SHA. rawURL is the original
// source string stored in the TOML.
//
// It does not print a success line or acquire the global lock — callers do.
// Both `marketplace add` and `import` use it so the two produce byte-identical
// canonical artifacts.
func addMarketplaceSource(home string, src marketplace.Source, rawURL string) (mpName, headSHA string, err error) {
	// Derive a slug for the marketplace.
	slug := deriveMarketplaceSlug(rawURL)
	cacheDir := marketplaceCacheDir(home, slug)

	// Fetch into cache.
	fetcher := marketplace.Dispatch(src)
	result, err := fetcher.Fetch(src, cacheDir)
	if err != nil {
		return "", "", fmt.Errorf("fetch marketplace %s: %w", rawURL, err)
	}

	// Read marketplace.json to extract the declared name.
	mpName = slug
	mpJSONPath := filepath.Join(cacheDir, ".claude-plugin", "marketplace.json")
	if data, err := os.ReadFile(mpJSONPath); err == nil {
		var mp marketplace.Marketplace
		if json.Unmarshal(data, &mp) == nil && mp.Name != "" {
			// Only adopt the declared name if it sanitises to something usable;
			// a name like "..." sanitises to "" and would author marketplaces/.toml.
			if s := sanitizeSlug(mp.Name); s != "" {
				mpName = s
			}
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
		return "", "", err
	}
	mpPath := filepath.Join(home, "marketplaces", mpName+".toml")
	if err := iox.AtomicWrite(mpPath, data, 0o644); err != nil {
		return "", "", fmt.Errorf("write %s: %w", mpPath, err)
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

	return mpName, result.HeadSHA, nil
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

// marketplaceListHint points users at the command that lists registered
// marketplace names; used in `marketplace remove` error messages.
const marketplaceListHint = "run: agentsync marketplace list to see registered names"

func marketplaceRemoveRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := validateCacheKey("marketplace", name); err != nil {
		return fmt.Errorf("%w; %s", err, marketplaceListHint)
	}
	home := paths.AgentsyncHome(paths.OSEnv{})

	tomlPath := filepath.Join(home, "marketplaces", name+".toml")
	// Removing an unregistered name is a user mistake, not a silent no-op: report
	// it and point at `marketplace list` rather than printing "removed".
	if _, err := os.Stat(tomlPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("marketplace %q is not registered; %s", name, marketplaceListHint)
		}
		return fmt.Errorf("stat %s: %w", tomlPath, err)
	}
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

// registeredMarketplaceNames returns the set of marketplace names registered in
// ~/.agentsync/marketplaces/. Both each TOML's declared `name` field and its
// filename stem map to true, so a native marketplace id matches whichever the
// store recorded. It mirrors how `marketplace list` enumerates the store and
// lets import resolve a plugin's marketplace from agentsync's own store before
// the agent's native config. A missing or unreadable store yields an empty set.
func registeredMarketplaceNames(home string) map[string]bool {
	entries, err := os.ReadDir(filepath.Join(home, "marketplaces"))
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		out[strings.TrimSuffix(e.Name(), ".toml")] = true
		data, err := os.ReadFile(filepath.Join(home, "marketplaces", e.Name()))
		if err != nil {
			continue
		}
		var mp marketplaceTOML
		if toml.Unmarshal(data, &mp) == nil && mp.Marketplace.Name != "" {
			out[mp.Marketplace.Name] = true
		}
	}
	return out
}

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
	// Sanitise FIRST, then fall back: a punctuation-only source (e.g. "...")
	// survives the trim above but sanitises to "", which would otherwise write
	// marketplaces/.toml and a marketplaces/_ cache dir.
	s = sanitizeSlug(s)
	if s == "" {
		s = "marketplace"
	}
	return s
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
