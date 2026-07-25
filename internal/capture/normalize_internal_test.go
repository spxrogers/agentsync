package capture

import (
	"encoding/json"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestNormalizeExtraNumbers_ProjectOverlayRecursion pins the Project-overlay
// recursion of normalizeExtraNumbers directly. Capture itself never writes
// project-scope files from ingested.Project — its write loops read only the
// top-level MCPServers/LSPServers/Hooks slices — so the recursion is
// defensive-only today: it exists so that if a future write-back ever persists
// the overlay, its Extra values arrive already normalized instead of silently
// reintroducing the json.Number → TOML-string type flip. Because that branch is
// unreachable through Capture's public surface (no on-disk artifact to anchor
// to), this test lives in-package and calls the unexported helper on a
// Canonical carrying a two-level Project overlay, proving the walk descends
// through nested overlays and converts MCP and LSP Extra values alike.
func TestNormalizeExtraNumbers_ProjectOverlayRecursion(t *testing.T) {
	inner := source.Canonical{
		LSPServers: []source.LSPServer{{
			ID: "lsp",
			Spec: source.LSPServerSpec{
				Command: "gopls",
				Extra:   map[string]any{"port": json.Number("9000")},
			},
		}},
	}
	overlay := source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "proj-srv",
			Server: source.MCPServerSpec{
				Type:    "stdio",
				Command: "npx",
				Extra: map[string]any{
					// 2^53+1: the value UseNumber exists to keep exact.
					"snowflake": json.Number("9007199254740993"),
					"ratio":     json.Number("0.5"),
					"nested":    map[string]any{"retries": json.Number("4")},
				},
			},
		}},
		Project: &inner,
	}
	c := &source.Canonical{Project: &overlay}

	normalizeExtraNumbers(c)

	extra := c.Project.MCPServers[0].Server.Extra
	if v, ok := extra["snowflake"].(int64); !ok || v != 9007199254740993 {
		t.Errorf("overlay Extra snowflake = %[1]v (%[1]T), want int64(9007199254740993)", extra["snowflake"])
	}
	if v, ok := extra["ratio"].(float64); !ok || v != 0.5 {
		t.Errorf("overlay Extra ratio = %[1]v (%[1]T), want float64(0.5)", extra["ratio"])
	}
	nested, _ := extra["nested"].(map[string]any)
	if v, ok := nested["retries"].(int64); !ok || v != 4 {
		t.Errorf("overlay nested Extra retries = %[1]v (%[1]T), want int64(4)", nested["retries"])
	}
	lspExtra := c.Project.Project.LSPServers[0].Spec.Extra
	if v, ok := lspExtra["port"].(int64); !ok || v != 9000 {
		t.Errorf("doubly nested overlay LSP Extra port = %[1]v (%[1]T), want int64(9000)", lspExtra["port"])
	}
}
