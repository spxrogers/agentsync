// Package project handles project-scope overlays: .agentsync.toml at a repo
// root, walk-up discovery from cwd, and merge against base canonical model.
package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spxrogers/agentsync/internal/source"
)

const MarkerFile = ".agentsync.toml"

// Marker is the parsed contents of a project's .agentsync.toml.
type Marker struct {
	Path    string                // absolute path of the marker file
	Root    string                // dirname of Path
	Agents  []string              `toml:"agents,omitempty"`
	MCP     []ProjectMCP          `toml:"mcp,omitempty"` // [[mcp]] array-of-tables
	Plugins ProjectPluginsSection `toml:"plugins,omitempty"`
	Memory  ProjectMemorySection  `toml:"memory,omitempty"`
}

// ProjectMCP holds an MCP server entry from the marker file.
// The marker uses a flat [[mcp]] array-of-tables where id, type, command,
// args, url, headers, env and agents fields are all at the top level.
type ProjectMCP struct {
	ID      string            `toml:"id"`
	Type    string            `toml:"type,omitempty"`
	Command string            `toml:"command,omitempty"`
	Args    []string          `toml:"args,omitempty"`
	URL     string            `toml:"url,omitempty"`
	Headers map[string]string `toml:"headers,omitempty"`
	Env     map[string]string `toml:"env,omitempty"`
	Agents  []string          `toml:"agents,omitempty"`
	Enabled *bool             `toml:"enabled,omitempty"`
}

// toMCPServer converts a ProjectMCP to a source.MCPServer.
func (p ProjectMCP) toMCPServer() source.MCPServer {
	return source.MCPServer{
		ID: p.ID,
		Server: source.MCPServerSpec{
			Type:    p.Type,
			Command: p.Command,
			Args:    p.Args,
			URL:     p.URL,
			Headers: p.Headers,
			Env:     p.Env,
			Agents:  p.Agents,
			Enabled: p.Enabled,
		},
	}
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

	// Agents filter: intersect base agents with marker allowlist.
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

	// MCP overlay: project entries replace base entries with same ID, or append.
	if len(m.MCP) > 0 {
		// Copy the base slice so we don't mutate it.
		merged := make([]source.MCPServer, len(out.MCPServers))
		copy(merged, out.MCPServers)
		byID := map[string]int{}
		for i, srv := range merged {
			byID[srv.ID] = i
		}
		for _, entry := range m.MCP {
			srv := entry.toMCPServer()
			if idx, exists := byID[srv.ID]; exists {
				merged[idx] = srv
			} else {
				byID[srv.ID] = len(merged)
				merged = append(merged, srv)
			}
		}
		out.MCPServers = merged
	}

	// Plugins.Disabled: remove plugins from base by ID.
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

	// Memory imports: read project-relative files and append.
	if len(m.Memory.Import) > 0 {
		body := out.Memory.Body
		// Resolve the root through any symlinks once so a symlinked tmp root
		// (e.g. macOS /tmp -> /private/tmp) doesn't cause false rejections of
		// legitimate in-root imports below.
		resolvedRoot := m.Root
		if rr, err := filepath.EvalSymlinks(m.Root); err == nil {
			resolvedRoot = rr
		}
		for _, rel := range m.Memory.Import {
			// Containment: a committed marker's import path must not escape
			// the project root. Without this, `import = ["../../etc/passwd"]`
			// (or an absolute path) reads arbitrary host files into the
			// rendered memory. Skip anything that resolves outside m.Root.
			abs := filepath.Join(m.Root, rel)
			if !importWithinRoot(m.Root, abs) {
				continue
			}
			// Defense-in-depth: the lexical check above can't see a committed
			// symlink under the root (leak.md -> /etc/passwd) — os.ReadFile
			// would follow it off-root. Resolve symlinks and re-check against
			// the resolved root before reading. Project markers come from
			// cloned repos, so this path is attacker-influenced.
			resolved, err := filepath.EvalSymlinks(abs)
			if err != nil || !importWithinRoot(resolvedRoot, resolved) {
				continue
			}
			data, err := os.ReadFile(resolved)
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

// importWithinRoot reports whether abs is the same path as root or sits
// inside it (lexical, after Clean). Used to bound project memory imports.
func importWithinRoot(root, abs string) bool {
	root = filepath.Clean(root)
	abs = filepath.Clean(abs)
	if root == abs {
		return true
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}
