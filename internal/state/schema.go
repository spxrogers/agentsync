// Package state persists agentsync's last-applied hashes and marketplace/plugin
// pinning to ~/.agentsync/.state/targets.json.
package state

import "time"

const SchemaVersion = 1

// Targets is the root state document.
type Targets struct {
	SchemaVersion int                    `json:"schema_version"`
	Files         map[string]FileEntry   `json:"files,omitempty"`
	Keys          map[string]KeyEntry    `json:"keys,omitempty"`
	Marketplaces  map[string]Marketplace `json:"marketplaces,omitempty"`
	Plugins       map[string]PluginEntry `json:"plugins,omitempty"`
}

// FileEntry tracks one fully-managed destination file.
// Key format: "<agent>:<scope>:<project>:<dest_path>"
type FileEntry struct {
	SHA256    string    `json:"sha256"`
	Mode      uint32    `json:"mode"`
	AppliedAt time.Time `json:"applied_at"`
	SourceID  string    `json:"source_id"` // canonical file that produced this dest
}

// KeyEntry tracks one managed JSON-pointer-addressable key inside a shared
// destination file.
// Key format: "<agent>:<scope>:<project>:<file>:<json_pointer>"
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
		Files:         map[string]FileEntry{},
		Keys:          map[string]KeyEntry{},
		Marketplaces:  map[string]Marketplace{},
		Plugins:       map[string]PluginEntry{},
	}
}
