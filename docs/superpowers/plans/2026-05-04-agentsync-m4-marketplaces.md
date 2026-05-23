# agentsync M4 — Marketplaces + plugins

> Conventions in [`overview`](2026-05-04-agentsync-v1.0-overview.md). Builds on M0–M3.

**Goal:** Implement Claude marketplace ingestion (5 source types: relative, github, url, git-subdir, npm), the plugin install/upgrade/enable/disable/remove CLI surface, per-component projection from plugin manifests through registered adapters, translation reports, manifest sha pinning, and update modes (pinned/track/manual). The `update` command (the only network-touching verb) refreshes marketplace caches and pre-computes pending bumps.

**Architecture:** New `internal/marketplace` package. `Fetcher` interface with one impl per source type (relative, git, npm). Cache lives at `~/.agentsync/.state/cache/marketplaces/<slug>/` and `.../plugins/<id>/`. Plugin install copies the manifest sha into `plugins/<id>.toml` (canonical); per-component decomposition happens in `internal/marketplace/projection.go` and emits `source.Canonical` overlays that adapters consume normally.

**Tech stack:** `github.com/go-git/go-git/v5` (git fetch + sparse), stdlib `net/http` + `archive/tar` + `compress/gzip` (npm tarball), no `npm`/`Bun` required at runtime.

---

## Files

```
NEW:
internal/marketplace/
├── marketplace.go        # types, public API
├── fetcher.go            # Fetcher interface + dispatch
├── fetch_git.go          # github, url, git-subdir
├── fetch_npm.go          # npm tarball
├── fetch_relative.go     # local path
├── manifest.go           # parse marketplace.json + plugin.json schemas
├── projection.go         # plugin manifest -> []FileOp via per-component projection
├── update.go             # update modes (pinned/track/manual), pending-bump computation
└── *_test.go

internal/cli/
├── marketplace.go        # marketplace add/remove/list
├── plugin.go             # plugin install/upgrade/enable/disable/remove/list
├── update.go             # update [--apply [--auto|--auto-safe]]
└── *_test.go
```

---

## Task 1: Marketplace + plugin manifest schemas

**Files:** `internal/marketplace/manifest.go`, `internal/marketplace/manifest_test.go`

Mirror the published schema verbatim (`code.claude.com/docs/en/plugin-marketplaces`).

- [ ] **Implement**

```go
// Package marketplace models the Claude marketplace.json + plugin.json schemas.
package marketplace

// Marketplace is the .claude-plugin/marketplace.json document.
type Marketplace struct {
    Schema      string                  `json:"$schema,omitempty"`
    Name        string                  `json:"name"`
    Owner       Owner                   `json:"owner"`
    Description string                  `json:"description,omitempty"`
    Version     string                  `json:"version,omitempty"`
    Metadata    *MarketplaceMetadata    `json:"metadata,omitempty"`
    Plugins     []PluginEntry           `json:"plugins"`
    AllowCrossMarketplaceDependenciesOn []string `json:"allowCrossMarketplaceDependenciesOn,omitempty"`
}

type Owner struct {
    Name  string `json:"name"`
    Email string `json:"email,omitempty"`
}

type MarketplaceMetadata struct {
    PluginRoot string `json:"pluginRoot,omitempty"`
}

// PluginEntry is one plugin listed in a marketplace.
type PluginEntry struct {
    Name        string         `json:"name"`
    Source      Source         `json:"source"`
    Description string         `json:"description,omitempty"`
    Version     string         `json:"version,omitempty"`
    Author      *Author        `json:"author,omitempty"`
    Homepage    string         `json:"homepage,omitempty"`
    Repository  string         `json:"repository,omitempty"`
    License     string         `json:"license,omitempty"`
    Keywords    []string       `json:"keywords,omitempty"`
    Category    string         `json:"category,omitempty"`
    Tags        []string       `json:"tags,omitempty"`
    Strict      *bool          `json:"strict,omitempty"` // default true

    // Component config can be inlined here when strict=false:
    Skills      any            `json:"skills,omitempty"`     // string | []string
    Commands    any            `json:"commands,omitempty"`   // string | []string
    Agents      any            `json:"agents,omitempty"`     // string | []string
    Hooks       any            `json:"hooks,omitempty"`      // string | object
    MCPServers  map[string]any `json:"mcpServers,omitempty"`
    LSPServers  map[string]any `json:"lspServers,omitempty"`
}

type Author struct {
    Name  string `json:"name"`
    Email string `json:"email,omitempty"`
}

// Source is the polymorphic plugin source. Tag-based dispatch:
type Source struct {
    Kind     string `json:"source,omitempty"`     // "github" | "url" | "git-subdir" | "npm"
    // Relative-path is encoded as a JSON string at the parent level; see UnmarshalJSON.
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
}

// UnmarshalJSON handles the polymorphic shape: string -> Relative; object -> Kind etc.
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

// Reserved names trigger a warning on `marketplace add`.
var ReservedMarketplaceNames = []string{
    "claude-code-marketplace", "claude-code-plugins", "claude-plugins-official",
    "anthropic-marketplace", "anthropic-plugins", "agent-skills",
    "knowledge-work-plugins", "life-sciences",
}
```

(Need `import "encoding/json"` at top of file.)

- [ ] **Test parse**

```go
func TestParseMarketplace_StringSource(t *testing.T) {
    raw := []byte(`{
        "name": "x",
        "owner": {"name": "y"},
        "plugins": [{"name": "p", "source": "./plugins/p"}]
    }`)
    var m marketplace.Marketplace
    if err := json.Unmarshal(raw, &m); err != nil {
        t.Fatal(err)
    }
    if m.Plugins[0].Source.Relative != "./plugins/p" {
        t.Fatalf("source = %+v", m.Plugins[0].Source)
    }
}

func TestParseMarketplace_ObjectSource(t *testing.T) {
    raw := []byte(`{
        "name": "x", "owner": {"name": "y"},
        "plugins": [{"name": "p", "source": {"source":"github","repo":"o/r","ref":"v1"}}]
    }`)
    var m marketplace.Marketplace
    _ = json.Unmarshal(raw, &m)
    if m.Plugins[0].Source.Kind != "github" || m.Plugins[0].Source.Repo != "o/r" {
        t.Fatalf("source = %+v", m.Plugins[0].Source)
    }
}
```

Commit.

---

## Task 2: `Fetcher` interface + relative + git source impls

**Files:** `internal/marketplace/{fetcher.go, fetch_relative.go, fetch_git.go, fetch_test.go}`

```go
// Fetcher fetches one plugin source into a local directory.
type Fetcher interface {
    Fetch(src Source, into string) (FetchResult, error)
}

type FetchResult struct {
    HeadSHA string  // for git sources
    Version string  // for npm
}

func Dispatch(src Source) Fetcher {
    if src.Relative != "" {
        return &RelativeFetcher{}
    }
    switch src.Kind {
    case "github", "url", "git-subdir":
        return &GitFetcher{}
    case "npm":
        return &NPMFetcher{}
    }
    return &errFetcher{err: fmt.Errorf("unknown source kind %q", src.Kind)}
}
```

- [ ] **Implement RelativeFetcher**: simple `cp -r` via afero or `os.CopyFS` (Go 1.23+). For 1.22 use a manual walk. Test with a tmpdir source + dest.

- [ ] **Implement GitFetcher**: uses `go-git/v5` for shallow clone with optional sha checkout. For `git-subdir`, configure sparse-checkout via `core.sparseCheckout=true` and `info/sparse-checkout` file containing `Path/*`. If go-git's sparse support is incomplete on the installed version, fall back to `exec.Command("git", "clone", ...)` followed by `git sparse-checkout init/set`. Test against a `file://` bare repo (set up in TestMain).

- [ ] **Implement NPMFetcher**: HTTP GET `<registry>/<package>/<version>` → returns metadata with `dist.tarball` URL → GET tarball → gunzip+untar into dest. No `npm` install on user's machine. Test with a fake registry served via `httptest.Server`.

Commit each fetcher individually.

---

## Task 3: Plugin install / load / projection

**Files:** `internal/marketplace/projection.go`, `internal/cli/plugin.go`

`agentsync plugin install <id>@<marketplace>`:
1. Resolve marketplace from `~/.agentsync/marketplaces/<name>.toml` + cache.
2. Find `<id>` in `marketplace.json` plugins list.
3. Compute manifest sha (sha256 of plugin.json bytes if strict; sha of marketplace entry block if non-strict).
4. Fetch plugin into `~/.agentsync/.state/cache/plugins/<id>/`.
5. Write `~/.agentsync/plugins/<id>.toml` with `version`, `manifest_sha`, `update`, default `agents=["*"]`.

The next `agentsync apply` reads `plugins/*.toml`, expands each through `projection.go` into per-component canonical entries that adapters render normally.

- [ ] **Plugin projection** decomposes a `PluginManifest` (strict) or `PluginEntry` (non-strict) into:

```go
// ProjectionResult adds entries to a canonical model from a plugin's components.
type ProjectionResult struct {
    MCPServers []source.MCPServer
    Skills     []source.Skill
    Subagents  []source.Subagent
    Commands   []source.Command
    Hooks      []source.Hook
    LSPServers []source.LSPServer
}

// Project loads .claude-plugin/plugin.json from cacheDir (strict) or returns
// the inlined entry's components (non-strict). Resolves ${CLAUDE_PLUGIN_ROOT}
// in commands/MCP server configs to cacheDir for non-Claude agents.
func Project(entry PluginEntry, cacheDir string) (ProjectionResult, error) {
    var pr ProjectionResult
    strict := entry.Strict == nil || *entry.Strict
    if strict {
        // load plugin.json from cacheDir/.claude-plugin/plugin.json
        var manifest PluginManifest
        data, err := os.ReadFile(filepath.Join(cacheDir, ".claude-plugin", "plugin.json"))
        if err == nil {
            if err := json.Unmarshal(data, &manifest); err != nil {
                return pr, fmt.Errorf("parse plugin.json: %w", err)
            }
            applyManifest(manifest, &pr, cacheDir)
        }
        // strict + marketplace-entry component overrides also merge in
        applyEntryOverrides(entry, &pr, cacheDir)
    } else {
        applyEntryFull(entry, &pr, cacheDir)
    }
    return pr, nil
}

func resolvePluginRoot(s string, cacheDir string) string {
    return strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", cacheDir)
}
```

(Helper `applyManifest`, `applyEntryOverrides`, `applyEntryFull` walk MCPServers/Skills/etc, build canonical entries, run `resolvePluginRoot` on every command/arg/url field.)

- [ ] **Test projection**

```go
func TestProject_StrictPluginJSON(t *testing.T) {
    cache := t.TempDir()
    _ = os.MkdirAll(filepath.Join(cache, ".claude-plugin"), 0o755)
    _ = os.WriteFile(filepath.Join(cache, ".claude-plugin", "plugin.json"),
        []byte(`{"name":"x","mcpServers":{"foo":{"command":"${CLAUDE_PLUGIN_ROOT}/run.sh"}}}`),
        0o644)
    pr, err := marketplace.Project(marketplace.PluginEntry{Name: "x"}, cache)
    if err != nil {
        t.Fatal(err)
    }
    if len(pr.MCPServers) != 1 {
        t.Fatalf("mcp = %d", len(pr.MCPServers))
    }
    cmd := pr.MCPServers[0].Server.Command
    if !strings.HasPrefix(cmd, cache) {
        t.Fatalf("CLAUDE_PLUGIN_ROOT not resolved: %s", cmd)
    }
}
```

- [ ] **Implement** `internal/cli/plugin.go`:

```go
agentsync plugin install <id>[@<marketplace>]   # cache + write plugins/<id>.toml
agentsync plugin upgrade <id>                   # bump version in plugins/<id>.toml; re-fetch; update sha
agentsync plugin enable <id>                    # set enabled on plugins/<id>.toml (already implicit; flag noop unless we add one)
agentsync plugin disable <id>                   # set agents=[] effectively (or add a disabled bool)
agentsync plugin remove <id>                    # delete plugins/<id>.toml + cache
agentsync plugin list                           # walk plugins/*.toml, print
```

Commit per subcommand. Tests use file:// fake repo as marketplace fixture; npm via httptest.Server.

---

## Task 4: Wire plugin projection into source loader

**Files:** modify `internal/source/loader.go`

After loading `plugins/<id>.toml`, the loader resolves each plugin's cached manifest, runs `marketplace.Project()`, and merges the result into the canonical model so adapters see the plugin's components transparently.

Loader gains a `cacheDir` parameter. Pass `~/.agentsync/.state/cache/plugins`.

- [ ] **Test**

```go
func TestLoad_PluginExpandsToMCP(t *testing.T) {
    fs := afero.NewMemMapFs()
    home := "/h"
    cache := "/h/.state/cache/plugins"
    _ = afero.WriteFile(fs, filepath.Join(home, "plugins", "x.toml"), []byte(`
[plugin]
id = "x@m"
version = "1"
`), 0o644)
    _ = afero.WriteFile(fs, filepath.Join(cache, "x", ".claude-plugin", "plugin.json"),
        []byte(`{"name":"x","mcpServers":{"server-from-plugin":{"command":"x"}}}`),
        0o644)
    c, err := source.LoadWithCache(fs, home, cache)
    if err != nil {
        t.Fatal(err)
    }
    var found bool
    for _, m := range c.MCPServers {
        if m.ID == "server-from-plugin" {
            found = true
        }
    }
    if !found {
        t.Fatalf("plugin's MCP not surfaced via projection: %+v", c.MCPServers)
    }
}
```

Implement `source.LoadWithCache` (existing `Load` becomes a thin wrapper that passes `""` for cache, falling back to skip-projection behavior). Commit.

---

## Task 5: Marketplace add/remove/list

**Files:** `internal/cli/marketplace.go`, `internal/cli/marketplace_test.go`

```
agentsync marketplace add github:owner/repo[@ref]   # writes marketplaces/<owner-repo>.toml; fetches into cache
agentsync marketplace add https://example.com/r.git # url source
agentsync marketplace remove <name>                  # deletes marketplaces/<name>.toml + cache
agentsync marketplace list                           # walks marketplaces/*.toml, prints
```

`add` performs an initial fetch via `marketplace.Fetcher` and writes the head SHA into `state.Marketplaces[<name>]`. Commit.

---

## Task 6: `update` command

**Files:** `internal/cli/update.go`, `internal/marketplace/update.go`

For each marketplace: re-fetch (or use cached etag for HTTP URL sources). For each plugin: compute pending bump given its `update` mode + the fetched manifest. Print pending bumps. With `--apply`: chain into apply logic. With `--auto-safe`: only bump plugins whose translation report is non-lossy.

```go
// in marketplace/update.go:
func ComputePendingBumps(s *state.Targets, marketplaces []source.Marketplace, plugins []source.Plugin, fetched map[string]Marketplace) []Bump {
    var out []Bump
    for _, p := range plugins {
        // find p in fetched marketplaces
        // compute desired version per p.Update mode
        // if differs from current p.Version + manifest_sha, emit Bump{ID, From, To, ManifestSHA}
    }
    return out
}
```

Test with fake fetcher; commit.

---

## Task 7: Translation report

**Files:** modify `internal/render/pipeline.go` to emit a structured report and make it available to `apply`/`update --apply`.

After Plan, before Apply, render emits per-(plugin, agent) lines:

```
plugin: atlassian@anthropic
  claude    ✓ full   (1 mcp, 5 commands)
  opencode  ✓ full   (1 mcp, 5 commands)
```

Emitted as both human-readable text and structured JSON via `--json`.

- [ ] Implement `internal/render/report.go` building the report from `plan.PerAgent[i].Skips` + the canonical MCPServers/Skills/etc-from-plugin metadata. Test, commit.

---

## Task 8: Manifest sha pinning

**Files:** modify projection logic to compute sha256 over the canonical bytes of `plugin.json` (strict) or the marketplace entry (non-strict). Compare to `plugins/<id>.toml`'s `manifest_sha`. If different, emit a `manifest-sha-mismatch` warning + treat as drift in `status`.

- [ ] Implement, test, commit.

---

## Task 9: Integration test — full plugin fanout

```go
func TestIntegration_M4_PluginFanoutClaudeAndOpenCode(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

    // Set up a fake marketplace as a local relative-path fixture
    fixture := filepath.Join(tmp, "fixture-marketplace")
    _ = os.MkdirAll(filepath.Join(fixture, ".claude-plugin"), 0o755)
    _ = os.WriteFile(filepath.Join(fixture, ".claude-plugin", "marketplace.json"),
        []byte(`{
            "name": "test-mp", "owner": {"name": "x"},
            "plugins": [{"name": "demo", "source": "./plugins/demo"}]
        }`), 0o644)
    plugDir := filepath.Join(fixture, "plugins", "demo", ".claude-plugin")
    _ = os.MkdirAll(plugDir, 0o755)
    _ = os.WriteFile(filepath.Join(plugDir, "plugin.json"),
        []byte(`{
            "name": "demo",
            "version": "1.0.0",
            "mcpServers": {"demo-mcp": {"command":"echo","args":["hi"]}}
        }`), 0o644)

    _, _ = runCLI(t, env, "init")
    _, _ = runCLI(t, env, "agent", "add", "claude")
    _, _ = runCLI(t, env, "agent", "add", "opencode")

    // marketplace add via local path
    if _, err := runCLI(t, env, "marketplace", "add", fixture); err != nil {
        t.Fatal(err)
    }
    if _, err := runCLI(t, env, "plugin", "install", "demo@test-mp"); err != nil {
        t.Fatal(err)
    }
    if _, err := runCLI(t, env, "apply"); err != nil {
        t.Fatal(err)
    }

    // verify both agents have demo-mcp
    body, _ := os.ReadFile(filepath.Join(tmp, ".claude.json"))
    if !strings.Contains(string(body), "demo-mcp") {
        t.Fatal("claude missing demo-mcp")
    }
    body, _ = os.ReadFile(filepath.Join(tmp, ".config", "opencode", "opencode.json"))
    if !strings.Contains(string(body), "demo-mcp") {
        t.Fatal("opencode missing demo-mcp")
    }
}
```

Commit.

---

## Done When

- 5 plugin source types fetch + extract correctly (relative, github, url, git-subdir, npm).
- `marketplace add/remove/list` and `plugin install/upgrade/enable/disable/remove/list` work end-to-end.
- A plugin's MCP/skills/subagents/commands/hooks/LSP project through every registered adapter; translation report shows ✓/◐/✗ per cell.
- `${CLAUDE_PLUGIN_ROOT}` resolves to cache path for non-Claude agents and passes through verbatim for Claude.
- `update` polls marketplaces and prints pending bumps without touching agent configs; `update --apply` chains into apply.
- `update --apply --auto-safe` only auto-applies non-lossy bumps; lossy ones stop for confirmation.
- Manifest sha pin detects re-uploaded same-version content as drift.
- Integration test demonstrates a single plugin install fanning to Claude + OpenCode after one apply.
- CI green on linux/macos/windows.
