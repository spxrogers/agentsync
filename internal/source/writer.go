// Package-level write-back helpers for the canonical source (~/.agentsync/).
// These are called by the reconcile command when the user selects [w]rite-back.
//
// v1 trade-off: TOML comments in the original file are not preserved on
// write-back. Comment-preserving mutation is deferred to v1.x.
package source

import (
	"fmt"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/spxrogers/agentsync/internal/iox"
)

// WriteMCP writes mcp/<id>.toml from m into home. Overwrites atomically.
func WriteMCP(home, id string, m MCPServer) error {
	body, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal mcp %s: %w", id, err)
	}
	return iox.AtomicWrite(filepath.Join(home, "mcp", id+".toml"), body, 0o644)
}

// WritePlugin writes plugins/<id>.toml from p into home. Overwrites atomically.
func WritePlugin(home, id string, p Plugin) error {
	body, err := toml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal plugin %s: %w", id, err)
	}
	return iox.AtomicWrite(filepath.Join(home, "plugins", id+".toml"), body, 0o644)
}

// WriteMarketplace writes marketplaces/<name>.toml from m into home. Overwrites atomically.
func WriteMarketplace(home, name string, m Marketplace) error {
	body, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal marketplace %s: %w", name, err)
	}
	return iox.AtomicWrite(filepath.Join(home, "marketplaces", name+".toml"), body, 0o644)
}
