# agentsync M5 — Project-local

> Conventions in [`overview`](2026-05-04-agentsync-v1.0-overview.md). Builds on M0–M4.

**Goal:** `.agentsync.toml` walk-up resolution from cwd; project IR overlay merged onto base IR; `--project <slug>` flag for explicit project selection; project-scope state tracking (state keys include the project slug).

**Architecture:** New `internal/project` package. `Detect()` walks up from cwd to find `.agentsync.toml`. Schema mirrors `~/.agentsync/`'s top-level (agents allowlist, MCP, plugin enables/disables, memory imports). Overlay merge: project entries replace user entries with the same id; project entries with new ids are added. `state.Targets.Files` and `state.Targets.Keys` keys include the project root path (or empty string for user scope) so drift tracks per-(scope, project).

---

## Files

```
NEW:
internal/project/{project.go, project_test.go}

MODIFIED:
internal/source/loader.go      # accepts project overlay path; merges
internal/cli/apply.go          # walks-up by default; --project explicit override
internal/cli/status.go, diff.go, reconcile.go  # all gain --project
```

---

## Task 1: `.agentsync.toml` schema + walk-up

`internal/project/project.go`:

```go
// Package project handles project-scope overlays: .agentsync.toml at a repo
// root, walk-up discovery from cwd, and merge against base canonical model.
package project

import (
    "errors"
    "os"
    "path/filepath"

    "github.com/pelletier/go-toml/v2"
    "github.com/spxrogers/agentsync/internal/source"
)

const MarkerFile = ".agentsync.toml"

// Marker is the parsed contents of a project's .agentsync.toml.
type Marker struct {
    Path     string                            // absolute path of the marker file
    Root     string                            // dirname of Path
    Agents   []string                          `toml:"agents,omitempty"`
    MCP      []source.MCPServerSpec            `toml:"mcp,omitempty"`
    Plugins  ProjectPluginsSection             `toml:"plugins,omitempty"`
    Memory   ProjectMemorySection              `toml:"memory,omitempty"`
}

type ProjectPluginsSection struct {
    Disabled []string `toml:"disabled,omitempty"`
    Enabled  []string `toml:"enabled,omitempty"`
}

type ProjectMemorySection struct {
    Import []string `toml:"import,omitempty"` // project-relative paths
}

// Discover walks up from cwd looking for MarkerFile. Returns (nil, nil) if
// not found. Returns error on read or parse failure.
func Discover(cwd string) (*Marker, error) {
    dir := cwd
    for {
        candidate := filepath.Join(dir, MarkerFile)
        if data, err := os.ReadFile(candidate); err == nil {
            var m Marker
            if err := toml.Unmarshal(data, &m); err != nil {
                return nil, err
            }
            m.Path = candidate
            m.Root = dir
            return &m, nil
        } else if !errors.Is(err, os.ErrNotExist) {
            return nil, err
        }
        parent := filepath.Dir(dir)
        if parent == dir {
            return nil, nil
        }
        dir = parent
    }
}
```

- [ ] **Test**

```go
func TestDiscover_FoundAtRoot(t *testing.T) {
    tmp := t.TempDir()
    deep := filepath.Join(tmp, "a", "b", "c")
    _ = os.MkdirAll(deep, 0o755)
    _ = os.WriteFile(filepath.Join(tmp, ".agentsync.toml"), []byte(`agents = ["claude"]`), 0o644)
    m, err := project.Discover(deep)
    if err != nil {
        t.Fatal(err)
    }
    if m == nil {
        t.Fatal("expected discovery")
    }
    if len(m.Agents) != 1 || m.Agents[0] != "claude" {
        t.Fatalf("agents = %v", m.Agents)
    }
}

func TestDiscover_NotFound(t *testing.T) {
    m, err := project.Discover(t.TempDir())
    if err != nil {
        t.Fatal(err)
    }
    if m != nil {
        t.Fatalf("expected nil marker")
    }
}
```

Commit.

---

## Task 2: Overlay merge

In `internal/project/project.go`:

```go
// Merge applies the project marker on top of base. Returns a new Canonical.
//   - Agents allowlist on Marker filters base.Config.Agents to only those
//     listed (intersect with enabled). Empty list = use all enabled.
//   - MCP entries on Marker are appended; collisions on .ID replace base.
//   - Plugins.Disabled removes plugins from base by ID.
//   - Plugins.Enabled is reserved for v1.x (currently a no-op since plugins
//     are enabled-by-default).
//   - Memory.Import paths are read relative to Marker.Root and appended to
//     the base memory body (separated by a single blank line).
func Merge(base source.Canonical, m *Marker) source.Canonical {
    if m == nil {
        return base
    }
    out := base // shallow copy is OK; we replace slices below

    // Agents filter
    if len(m.Agents) > 0 {
        allow := map[string]bool{}
        for _, a := range m.Agents {
            allow[a] = true
        }
        filtered := map[string]source.Agent{}
        for name, ag := range base.Config.Agents {
            if allow[name] {
                filtered[name] = ag
            }
        }
        out.Config.Agents = filtered
    }

    // MCP overlay
    byID := map[string]int{}
    for i, srv := range out.MCPServers {
        byID[srv.ID] = i
    }
    for _, spec := range m.MCP {
        // Marker.MCP entries are MCPServerSpec; need an ID. v1: treat the
        // first entry's command as the implicit id basis. Actually, our
        // MCP block in marker is `[[mcp]]` with explicit id field — adjust
        // schema to wrap MCPServer not MCPServerSpec. Update Marker:
    }

    // Plugins.Disabled
    if len(m.Plugins.Disabled) > 0 {
        block := map[string]bool{}
        for _, id := range m.Plugins.Disabled {
            block[id] = true
        }
        var kept []source.Plugin
        for _, p := range out.Plugins {
            if !block[p.ID] {
                kept = append(kept, p)
            }
        }
        out.Plugins = kept
    }

    // Memory imports
    if len(m.Memory.Import) > 0 {
        body := out.Memory.Body
        for _, rel := range m.Memory.Import {
            data, err := os.ReadFile(filepath.Join(m.Root, rel))
            if err != nil {
                continue
            }
            if body != "" && !strings.HasSuffix(body, "\n") {
                body += "\n"
            }
            body += "\n" + string(data)
        }
        out.Memory.Body = body
    }
    return out
}
```

(The marker schema for MCP needs to carry an `id` field. Update `Marker`:)

```go
type ProjectMCP struct {
    ID     string                  `toml:"id"`
    Server source.MCPServerSpec    `toml:"server"`
}

type Marker struct {
    Path    string
    Root    string
    Agents  []string                  `toml:"agents,omitempty"`
    MCP     []ProjectMCP              `toml:"mcp,omitempty"` // [[mcp]] tables
    Plugins ProjectPluginsSection     `toml:"plugins,omitempty"`
    Memory  ProjectMemorySection      `toml:"memory,omitempty"`
}
```

The marker example in the design spec shows:

```toml
[[mcp]]
id      = "company-api"
type    = "stdio"
command = "npx"
args    = ["-y", "@company/mcp"]
[mcp.env]
COMPANY_TOKEN = "${secret:company.api_token}"
```

This is `[[mcp]]` array-of-tables with the spec fields inlined at the table's top level (not under `[server]`). Adjust loader: `ProjectMCP` parses the flat shape and maps to `MCPServerSpec`. Test the overlay with a real `.agentsync.toml`. Commit.

---

## Task 3: Wire project into CLI

- Modify `internal/cli/apply.go`, `status.go`, `diff.go`, `reconcile.go` to:
  - Add `--project <path>` flag (defaults to walk-up).
  - Discover project marker via `project.Discover(cwd)` (or use `--project` override).
  - Pass `adapter.ScopeProject` and the project root to Render/Plan/Apply when a marker is found.
  - Update `RecordOpsState` calls to include the project root in the state key.

- [ ] **Integration test**

```go
func TestIntegration_M5_ProjectOverlay(t *testing.T) {
    tmpHome := t.TempDir()
    project := t.TempDir()
    env := map[string]string{
        "AGENTSYNC_TARGET_ROOT": tmpHome,
        "PWD":                   project, // some shells; for us: chdir
    }

    _, _ = runCLI(t, env, "init")
    _, _ = runCLI(t, env, "agent", "add", "claude")

    _ = os.WriteFile(filepath.Join(project, ".agentsync.toml"), []byte(`
agents = ["claude"]

[[mcp]]
id      = "proj-mcp"
[mcp.server]
type    = "stdio"
command = "npx"
args    = ["-y", "@proj/mcp"]
`), 0o644)

    // chdir into project
    cwd, _ := os.Getwd()
    defer os.Chdir(cwd)
    _ = os.Chdir(project)

    if _, err := runCLI(t, env, "apply"); err != nil {
        t.Fatal(err)
    }
    body, _ := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
    if !strings.Contains(string(body), "proj-mcp") {
        t.Fatalf("project-scope MCP not landed: %s", body)
    }
}
```

Commit.

---

## Done When

- `cd ~/repo && agentsync apply` discovers `.agentsync.toml`, merges overlay, writes project-scope destinations under `<repo>/.claude/`, etc.
- `agentsync apply --project /abs/path` works without cwd dependence.
- State keys disambiguate user vs project (drift on `~/.claude/settings.json` vs `<repo>/.claude/settings.json` is independently tracked).
- Marker `agents = ["claude"]` filters: a project that only wants Claude doesn't accidentally write to OpenCode in that scope.
- `[[mcp]]` array-of-tables in marker overlays into base canonical correctly.
- Memory.Import resolves project-relative files and concatenates them into the rendered memory.
- CI green.
