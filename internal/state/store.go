package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spxrogers/agentsync/internal/iox"
)

// Load reads targets.json from path. If the file is missing, returns a fresh
// state at the current SchemaVersion.
func Load(path string) (*Targets, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return New(), nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var t Targets
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if t.SchemaVersion == 0 {
		t.SchemaVersion = SchemaVersion
	}
	if t.Files == nil {
		t.Files = map[string]FileEntry{}
	}
	if t.Keys == nil {
		t.Keys = map[string]KeyEntry{}
	}
	if t.Marketplaces == nil {
		t.Marketplaces = map[string]Marketplace{}
	}
	if t.Plugins == nil {
		t.Plugins = map[string]PluginEntry{}
	}
	return &t, nil
}

// Save serializes t to path atomically (iox.AtomicWrite).
func Save(path string, t *Targets) error {
	if t == nil {
		return fmt.Errorf("save nil targets")
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal targets: %w", err)
	}
	return iox.AtomicWrite(path, append(data, '\n'), 0o644)
}
