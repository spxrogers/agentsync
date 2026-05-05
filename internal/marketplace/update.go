package marketplace

import (
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// Bump describes a pending plugin version change discovered by the update scan.
type Bump struct {
	// ID is the plugin's filesystem ID (e.g. "demo").
	ID string
	// From is the currently pinned version (from plugins/<id>.toml).
	From string
	// To is the latest version available in the fetched marketplace.
	To string
	// ManifestSHA is the SHA of the freshly-fetched plugin.json (if available).
	ManifestSHA string
	// UpdateMode is the plugin's configured update mode ("pinned", "track", "manual").
	UpdateMode string
}

// SHAWarning is emitted when a plugin's recorded manifest_sha differs from
// the freshly-fetched SHA for the same version — a signal that the marketplace
// publisher re-uploaded the same version with different content.
type SHAWarning struct {
	// ID is the plugin's filesystem ID.
	ID string
	// Version is the version that was re-uploaded.
	Version string
	// RecordedSHA is what was stored in plugins/<id>.toml at install time.
	RecordedSHA string
	// FetchedSHA is the SHA of the freshly-fetched plugin.json.
	FetchedSHA string
}

// DetectSHADrift checks installed plugins against freshly-fetched manifest SHAs
// (provided by the caller after re-fetching each plugin's cache).  For each
// plugin whose version hasn't changed but whose SHA has, a SHAWarning is returned.
//
// freshSHAs maps plugin ID → sha256 hex string of the freshly-fetched plugin.json.
func DetectSHADrift(plugins []source.Plugin, freshSHAs map[string]string) []SHAWarning {
	var out []SHAWarning
	for _, pl := range plugins {
		freshSHA, ok := freshSHAs[pl.ID]
		if !ok || freshSHA == "" {
			continue
		}
		recorded := pl.Plugin.ManifestSHA
		if recorded == "" || recorded == freshSHA {
			continue
		}
		out = append(out, SHAWarning{
			ID:          pl.ID,
			Version:     pl.Plugin.Version,
			RecordedSHA: recorded,
			FetchedSHA:  freshSHA,
		})
	}
	return out
}

// ComputePendingBumps compares each installed plugin's current version against
// the freshly-fetched marketplace data and returns the list of plugins that have
// a newer version available and whose update mode allows automatic upgrade.
//
// Only plugins whose Update mode is "" (default → "track") or "track" are
// included as pending bumps. Plugins with Update = "pinned" or "manual" are
// skipped (they need explicit user action).
//
// fetched maps marketplace name → fetched Marketplace index (keyed by plugin name).
func ComputePendingBumps(
	_ *state.Targets,
	_ []source.Marketplace,
	plugins []source.Plugin,
	fetched map[string]map[string]PluginEntry,
) []Bump {
	var out []Bump

	for _, pl := range plugins {
		spec := pl.Plugin

		// Resolve effective update mode.
		mode := spec.Update
		if mode == "" {
			mode = "track"
		}
		// Only "track" triggers automatic bumps.
		if mode != "track" {
			continue
		}

		// Find this plugin in one of the fetched marketplaces.
		// The spec.ID format is "<name>@<marketplace>".
		_, mpName := splitPluginRefPkg(spec.ID)
		if mpName == "" {
			// No marketplace hint — search all fetched.
			for _, entries := range fetched {
				if entry, ok := entries[pl.ID]; ok {
					if newer := computeBump(pl, spec, entry, mode); newer != nil {
						out = append(out, *newer)
					}
					break
				}
			}
			continue
		}

		entries, ok := fetched[mpName]
		if !ok {
			continue
		}
		entry, ok := entries[pl.ID]
		if !ok {
			continue
		}
		if newer := computeBump(pl, spec, entry, mode); newer != nil {
			out = append(out, *newer)
		}
	}
	return out
}

// computeBump returns a Bump if the fetched entry has a newer version than what
// is currently recorded for the plugin, or nil if no bump is needed.
func computeBump(pl source.Plugin, spec source.PluginSpec, entry PluginEntry, mode string) *Bump {
	latestVersion := entry.Version
	if latestVersion == "" || latestVersion == spec.Version {
		return nil
	}
	return &Bump{
		ID:         pl.ID,
		From:       spec.Version,
		To:         latestVersion,
		UpdateMode: mode,
	}
}

// splitPluginRefPkg splits "name@marketplace" into (name, marketplace).
// This mirrors the CLI's splitPluginRef but lives in the marketplace package
// to avoid a cli→marketplace→cli import cycle.
func splitPluginRefPkg(ref string) (name, mp string) {
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == '@' {
			return ref[:i], ref[i+1:]
		}
	}
	return ref, ""
}
