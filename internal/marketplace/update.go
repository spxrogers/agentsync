package marketplace

import (
	"strconv"
	"strings"

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
	// Only bump FORWARD. With raw string inequality a marketplace rollback to
	// an older version was auto-applied as a silent "downgrade bump". When both
	// versions parse as dotted-numeric semver, suppress the bump if the fetched
	// version is strictly OLDER. If the cores are equal (e.g. a prerelease
	// suffix differs) or either version is non-semver, fall back to
	// track-latest (any change bumps) so an exotic scheme isn't stranded.
	if cmp, ok := compareSemver(spec.Version, latestVersion); ok && cmp > 0 {
		return nil
	}
	return &Bump{
		ID:         pl.ID,
		From:       spec.Version,
		To:         latestVersion,
		UpdateMode: mode,
	}
}

// compareSemver compares two dotted-numeric versions, returning (-1|0|1, true)
// when BOTH parse as a numeric release core, or (0, false) when either does
// not. A leading "v" and any -prerelease/+build suffix are ignored; only the
// release core (MAJOR.MINOR.PATCH…) is compared. It is intentionally minimal —
// a non-comparable result lets the caller fall back to track-latest.
func compareSemver(a, b string) (int, bool) {
	pa, oka := parseNumericVersion(a)
	pb, okb := parseNumericVersion(b)
	if !oka || !okb {
		return 0, false
	}
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseNumericVersion parses "v1.2.3", "1.2.3-rc1", or "1.2" into numeric
// release-core components ([]int). It returns false if any core component is
// non-numeric, which signals the caller to fall back to string semantics.
func parseNumericVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
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
