// Package project handles project-scope source trees: a <root>/.agentsync/
// directory (same on-disk layout as the user-scope ~/.agentsync/), walk-up
// discovery from cwd, and overlay merge against the user-scope canonical model.
//
// A project tree is a full canonical home rooted at a repository instead of
// $HOME, so every existing loader/writer/capture path works unchanged by simply
// pointing `home` at project.Home(root). The retired M5 single-file
// .agentsync.toml marker is no longer a live schema; Discover surfaces a
// migration error if it finds one (see Discover).
package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// DirName is the project-scope source directory at a repository root.
const DirName = ".agentsync"

// LegacyMarkerFile is the retired M5 single-file project marker. It is no longer
// loaded; Discover reports it so a user can migrate to a DirName tree.
const LegacyMarkerFile = ".agentsync.toml"

// Home returns the project canonical home (<root>/.agentsync) for a project root.
func Home(root string) string { return filepath.Join(root, DirName) }

// Discover walks up from start looking for a project source tree: a DirName
// directory. Returns the project root (the directory CONTAINING DirName) and
// found=true on the nearest match. Returns ("", false, nil) when no project
// tree exists at or above start.
//
// If it encounters the retired single-file LegacyMarkerFile at a directory that
// has no DirName tree, it returns a migration error rather than silently
// ignoring it — a project that worked under M5 must not quietly lose its config.
// A DirName tree always wins over a legacy file at the same directory.
func Discover(start string) (root string, found bool, err error) {
	dir := start
	for {
		treeInfo, statErr := os.Stat(filepath.Join(dir, DirName))
		switch {
		case statErr == nil && treeInfo.IsDir():
			return dir, true, nil
		case statErr != nil && !os.IsNotExist(statErr):
			return "", false, fmt.Errorf("inspect %s: %w", filepath.Join(dir, DirName), statErr)
		}
		// No DirName tree here — a stray legacy marker is a migration prompt.
		if fi, ferr := os.Stat(filepath.Join(dir, LegacyMarkerFile)); ferr == nil && !fi.IsDir() {
			return "", false, fmt.Errorf(
				"%s holds a legacy single-file %s marker, which is no longer read; "+
					"recreate your project config as a %s/ tree (run `agentsync init --scope project` in that repo) "+
					"and move the marker's settings into it",
				dir, LegacyMarkerFile, DirName,
			)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, nil
		}
		dir = parent
	}
}

// Merge overlays the project-scope canonical `proj` onto the user-scope `base`,
// returning a new Canonical. It never mutates base.
//
// Semantics (documented in docs/architecture.md §project overlay):
//   - Agents: if proj declares any agents, its agent map REPLACES base's (the
//     project picks its own agent set); an empty project agent map inherits base.
//   - MCP / LSP / Skills / Subagents / Commands: overlaid by identity (ID or
//     Name) — a project entry replaces a base entry with the same key, and a new
//     key is appended. Order is base-first, then appended project entries.
//   - Hooks: overlaid by Event — if the project declares any hook for an event,
//     its hooks for that event REPLACE all base hooks for that event (hooks are a
//     per-event set, not individually addressable).
//   - Memory: base body then project body, concatenated (project appended).
//   - Plugins / Marketplaces: project entries are not modeled at project scope in
//     v1 and are inherited from base unchanged (see the migration note in
//     docs/architecture.md). Config (Updates/Secrets) is inherited from base
//     unless the project declares its own non-zero values.
func Merge(base, proj source.Canonical) source.Canonical {
	out := base // shallow copy; every slice/map we touch is replaced, not mutated

	if len(proj.Config.Agents) > 0 {
		agents := make(map[string]source.Agent, len(proj.Config.Agents))
		for k, v := range proj.Config.Agents {
			agents[k] = v
		}
		out.Config.Agents = agents
	}
	if proj.Config.Updates != (source.UpdateDefaults{}) {
		out.Config.Updates = proj.Config.Updates
	}
	if proj.Config.Secrets != (source.SecretsConfig{}) {
		out.Config.Secrets = proj.Config.Secrets
	}

	out.MCPServers = overlayByKey(base.MCPServers, proj.MCPServers, func(m source.MCPServer) string { return m.ID })
	out.LSPServers = overlayByKey(base.LSPServers, proj.LSPServers, func(l source.LSPServer) string { return l.ID })
	out.Skills = overlayByKey(base.Skills, proj.Skills, func(s source.Skill) string { return s.Name })
	out.Subagents = overlayByKey(base.Subagents, proj.Subagents, func(s source.Subagent) string { return s.Name })
	out.Commands = overlayByKey(base.Commands, proj.Commands, func(c source.Command) string { return c.Name })
	out.Hooks = overlayHooks(base.Hooks, proj.Hooks)
	out.Memory = overlayMemory(base.Memory, proj.Memory)

	// Store the project-only canonical so scope-aware render paths (apply
	// --scope project) can render proj items to the project directory without
	// also writing user-scope items there. secretFields/cloneForResolve already
	// handle c.Project; this was simply never set.
	projCopy := proj
	projCopy.Project = nil // prevent accidental nesting if proj itself came from a Merge
	out.Project = &projCopy

	return out
}

// overlayByKey returns base with proj entries overlaid: a proj entry whose key
// matches a base entry replaces it in place; a new key is appended. The result
// is a fresh slice (base is never mutated).
func overlayByKey[T any](base, proj []T, key func(T) string) []T {
	if len(proj) == 0 {
		return base
	}
	out := make([]T, len(base))
	copy(out, base)
	idx := make(map[string]int, len(out))
	for i, it := range out {
		idx[key(it)] = i
	}
	for _, it := range proj {
		if i, ok := idx[key(it)]; ok {
			out[i] = it
		} else {
			idx[key(it)] = len(out)
			out = append(out, it)
		}
	}
	return out
}

// overlayHooks replaces every base hook for an event the project also declares,
// then appends project hooks for events base did not have. Hooks are a per-event
// set, so a project that customises an event owns that event's whole set.
func overlayHooks(base, proj []source.Hook) []source.Hook {
	if len(proj) == 0 {
		return base
	}
	projEvents := map[string]bool{}
	for _, h := range proj {
		projEvents[h.Event] = true
	}
	out := make([]source.Hook, 0, len(base)+len(proj))
	for _, h := range base {
		if !projEvents[h.Event] {
			out = append(out, h)
		}
	}
	out = append(out, proj...)
	return out
}

// overlayMemory concatenates base then project memory bodies (project appended),
// separated by a blank line. Fragments are inherited from base; the project's
// memory is already a resolved body by the time it reaches here.
func overlayMemory(base, proj source.Memory) source.Memory {
	out := base
	pb := strings.TrimRight(proj.Body, "\n")
	if pb == "" {
		return out
	}
	if strings.TrimSpace(out.Body) == "" {
		out.Body = proj.Body
		return out
	}
	bb := strings.TrimRight(out.Body, "\n")
	out.Body = bb + "\n\n" + pb
	return out
}
