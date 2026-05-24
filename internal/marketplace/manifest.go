// Package marketplace models the Claude marketplace.json + plugin.json schemas,
// the Fetcher interface for resolving plugin sources, and the projection layer
// that decomposes plugin manifests into canonical source model entries.
package marketplace

import "encoding/json"

// Marketplace is the .claude-plugin/marketplace.json document.
type Marketplace struct {
	Schema                              string               `json:"$schema,omitempty"`
	Name                                string               `json:"name"`
	Owner                               Owner                `json:"owner"`
	Description                         string               `json:"description,omitempty"`
	Version                             string               `json:"version,omitempty"`
	Metadata                            *MarketplaceMetadata `json:"metadata,omitempty"`
	Plugins                             []PluginEntry        `json:"plugins"`
	AllowCrossMarketplaceDependenciesOn []string             `json:"allowCrossMarketplaceDependenciesOn,omitempty"`
}

// Owner holds the name and optional email of a marketplace or plugin owner.
type Owner struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

// MarketplaceMetadata holds optional extra metadata for a marketplace.
type MarketplaceMetadata struct {
	PluginRoot string `json:"pluginRoot,omitempty"`
}

// PluginEntry is one plugin listed in a marketplace.
type PluginEntry struct {
	Name        string   `json:"name"`
	Source      Source   `json:"source"`
	Description string   `json:"description,omitempty"`
	Version     string   `json:"version,omitempty"`
	Author      *Author  `json:"author,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	Repository  string   `json:"repository,omitempty"`
	License     string   `json:"license,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	Category    string   `json:"category,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Strict      *bool    `json:"strict,omitempty"` // conflict policy on the plugin.json+entry union (default true): strict errors on a same-name conflict, non-strict lets the entry override. See marketplace.resolveConflicts.

	// Component config can be inlined here (overlaid on plugin.json):
	Skills     any            `json:"skills,omitempty"`   // string | []string
	Commands   any            `json:"commands,omitempty"` // string | []string
	Agents     any            `json:"agents,omitempty"`   // string | []string
	Hooks      any            `json:"hooks,omitempty"`    // string | object
	MCPServers map[string]any `json:"mcpServers,omitempty"`
	LSPServers map[string]any `json:"lspServers,omitempty"`
}

// Author holds the name and optional email of a plugin author.
type Author struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

// Source is the polymorphic plugin source. Tag-based dispatch:
// - A JSON string → Relative path
// - A JSON object → Kind-based (github, url, git-subdir, npm)
type Source struct {
	Kind     string `json:"source,omitempty"` // "github" | "url" | "git-subdir" | "npm"
	Repo     string `json:"repo,omitempty"`
	URL      string `json:"url,omitempty"`
	Path     string `json:"path,omitempty"`
	Ref      string `json:"ref,omitempty"`
	SHA      string `json:"sha,omitempty"`
	Package  string `json:"package,omitempty"`
	Version  string `json:"version,omitempty"`
	Registry string `json:"registry,omitempty"`
	// Relative is the relative-path string when Source was a JSON string.
	Relative string `json:"-"`
	// RootDir, if non-empty, constrains where the RelativeFetcher will copy
	// from — Relative must resolve inside RootDir or the fetch is rejected.
	// Callers that resolve a marketplace-supplied relative path against the
	// marketplace cache directory should set this to the cache root so a
	// hostile marketplace.json entry like `"source": "../../../etc"` cannot
	// copy host files into the plugin cache.
	RootDir string `json:"-"`
}

// UnmarshalJSON handles the polymorphic shape: string → Relative; object → Kind etc.
func (s *Source) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var rel string
		if err := json.Unmarshal(data, &rel); err != nil {
			return err
		}
		s.Relative = rel
		return nil
	}
	type alias Source
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*s = Source(a)
	return nil
}

// MarshalJSON serialises a Source back to either a JSON string (relative) or
// a JSON object (all other kinds).
func (s Source) MarshalJSON() ([]byte, error) {
	if s.Relative != "" {
		return json.Marshal(s.Relative)
	}
	type alias Source
	return json.Marshal(alias(s))
}

// PluginManifest is .claude-plugin/plugin.json for a strict-mode plugin.
type PluginManifest struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Version     string         `json:"version,omitempty"`
	MCPServers  map[string]any `json:"mcpServers,omitempty"`
	Skills      any            `json:"skills,omitempty"`
	Commands    any            `json:"commands,omitempty"`
	Agents      any            `json:"agents,omitempty"`
	Hooks       any            `json:"hooks,omitempty"`
	LSPServers  map[string]any `json:"lspServers,omitempty"`
}

// ReservedMarketplaceNames are names that trigger a warning on `marketplace add`.
var ReservedMarketplaceNames = []string{
	"claude-code-marketplace",
	"claude-code-plugins",
	"claude-plugins-official",
	"anthropic-marketplace",
	"anthropic-plugins",
	"agent-skills",
	"knowledge-work-plugins",
	"life-sciences",
}
