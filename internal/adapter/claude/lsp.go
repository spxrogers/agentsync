package claude

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderLSP writes LSP server configurations into settings.json at
// /lspServers/<id>. Uses merge-json-keys so foreign LSP servers are preserved.
func (a *Adapter) renderLSP(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	if len(c.LSPServers) == 0 {
		return nil, nil
	}
	lspMap := map[string]any{}
	var ownedKeys []string
	for _, lsp := range c.LSPServers {
		if lsp.Spec.Enabled != nil && !*lsp.Spec.Enabled {
			continue
		}
		if !agentTargeted("claude", lsp.Spec.Agents) {
			continue
		}
		spec := map[string]any{}
		if lsp.Spec.Command != "" {
			spec["command"] = lsp.Spec.Command
		}
		if len(lsp.Spec.Args) > 0 {
			spec["args"] = lsp.Spec.Args
		}
		if len(lsp.Spec.Env) > 0 {
			spec["env"] = lsp.Spec.Env
		}
		if lsp.Spec.URL != "" {
			spec["url"] = lsp.Spec.URL
		}
		if len(lsp.Spec.Headers) > 0 {
			spec["headers"] = lsp.Spec.Headers
		}
		MergeExtra(spec, lsp.Spec.Extra)
		lspMap[lsp.ID] = spec
		ownedKeys = append(ownedKeys, "/lspServers/"+lsp.ID)
	}
	if len(lspMap) == 0 {
		return nil, nil
	}
	obj := map[string]any{"lspServers": lspMap}
	body, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal lsp: %w", err)
	}
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.Settings,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "lsp/* (multiple)",
		MergeStrategy: "merge-json-keys",
		OwnedKeys:     ownedKeys,
	}}, nil
}
