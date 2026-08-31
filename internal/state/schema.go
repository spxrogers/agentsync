// Package state persists agentsync's own bookkeeping under ~/.agentsync/.state/:
// the last-applied hashes and marketplace/plugin pinning in targets.json, and
// the per-machine run record in last-run.json (see lastrun.go).
package state

import "time"

// SchemaVersion is the on-disk format version of targets.json.
//
//	1 — files/keys were string maps keyed "agent:scope:project:path[:pointer]",
//	    a ':'-joined encoding that is NOT injective: ':' is legal in a POSIX
//	    path, so a project root containing one produced keys that string-prefixed
//	    a sibling project's (issue #227).
//	2 — files/keys are keyed by the typed, length-prefixed state.Key (key.go).
//	    Load migrates 0/1 -> 2 in place; the next Save commits the upgrade.
const SchemaVersion = 2

// Targets is the root state document.
//
// Files and Keys are keyed by the typed Key, so a hand-rolled fmt.Sprintf key is
// a compile error rather than a convention. That is the guarantee — NOT that
// NewFileKey/NewPointerKey are the only way in: a Key{…} composite literal
// compiles anywhere (it just skips their paths.HomeRelative, which is how tests
// plant a key in a specific portable spelling), and ParseKey is exported.
// Marketplaces and Plugins keep string keys: those are marketplace names and
// "owner/plugin" ids, a different namespace with no ambiguity problem.
type Targets struct {
	SchemaVersion int                    `json:"schema_version"`
	Files         map[Key]FileEntry      `json:"files,omitempty"`
	Keys          map[Key]KeyEntry       `json:"keys,omitempty"`
	Marketplaces  map[string]Marketplace `json:"marketplaces,omitempty"`
	Plugins       map[string]PluginEntry `json:"plugins,omitempty"`
}

// FileEntry tracks one fully-managed destination file, under a Key whose
// Pointer is empty.
type FileEntry struct {
	SHA256    string    `json:"sha256"`
	Mode      uint32    `json:"mode"`
	AppliedAt time.Time `json:"applied_at"`
	SourceID  string    `json:"source_id"` // canonical file that produced this dest
}

// KeyEntry tracks one managed JSON-pointer-addressable key inside a shared
// destination file, under a Key whose Pointer is that JSON pointer.
type KeyEntry struct {
	SHA256    string    `json:"sha256"`
	AppliedAt time.Time `json:"applied_at"`
	SourceID  string    `json:"source_id"`
}

type Marketplace struct {
	URL       string    `json:"url"`
	Ref       string    `json:"ref"`
	HeadSHA   string    `json:"head_sha"`
	FetchedAt time.Time `json:"fetched_at"`
}

type PluginEntry struct {
	Version     string `json:"version"`
	ManifestSHA string `json:"manifest_sha"`
	Enabled     bool   `json:"enabled"`
}

// New returns a fresh empty Targets at SchemaVersion.
func New() *Targets {
	return &Targets{
		SchemaVersion: SchemaVersion,
		Files:         map[Key]FileEntry{},
		Keys:          map[Key]KeyEntry{},
		Marketplaces:  map[string]Marketplace{},
		Plugins:       map[string]PluginEntry{},
	}
}
