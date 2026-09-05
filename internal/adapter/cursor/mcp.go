package cursor

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMCP converts canonical MCP servers into a single op targeting
// `.cursor/mcp.json` under the `mcpServers` object. Cursor's stdio schema matches
// Claude's (type/command/args/env, `${env:…}` references), but its documented
// REMOTE schema is url+headers with NO `type` key (per cursor.com/docs, MCP) —
// so `type` is emitted only for a stdio-shaped server and a remote server round-
// trips its transport via url presence (Cursor infers remote), not a label.
// Passthrough native keys carry through `Extra`. op.Content is JSON and the
// merge-json-keys strategy preserves a hand-authored mcp.json's foreign servers.
func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	targeted := map[string]any{}
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted("cursor", m.Server.Agents) {
			continue
		}
		spec := map[string]any{}
		// Emit `type` only for a stdio-shaped server (a command, no url). Cursor's
		// documented remote schema is url+headers with no `type`, so writing it on
		// a remote server would be an off-spec key.
		// TODO(#164): whether real Cursor *rejects* vs silently *ignores* an
		// unknown remote `type` is unconfirmed upstream (its docs are silent on
		// unknown-field tolerance); revisit the drop-vs-keep verdict when #164's
		// real-Cursor test lands.
		if m.Server.Type != "" && m.Server.Command != "" && m.Server.URL == "" {
			spec["type"] = m.Server.Type
		}
		if m.Server.Command != "" {
			spec["command"] = m.Server.Command
		}
		if len(m.Server.Args) > 0 {
			spec["args"] = m.Server.Args
		}
		if len(m.Server.Env) > 0 {
			spec["env"] = m.Server.Env
		}
		if m.Server.URL != "" {
			spec["url"] = m.Server.URL
		}
		if len(m.Server.Headers) > 0 {
			spec["headers"] = m.Server.Headers
		}
		claude.MergeExtra(spec, m.Server.Extra)
		targeted[m.ID] = spec
	}
	if len(targeted) == 0 {
		return nil, nil
	}
	ours := map[string]any{"mcpServers": targeted}
	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal cursor mcp: %w", err)
	}
	return []adapter.FileOp{{
		Action:        adapter.ActionWrite,
		Path:          p.MCP,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "mcp/* (multiple)",
		MergeStrategy: "merge-json-keys",
	}}, nil
}

// IngestMCPSpec translates one Cursor-native MCP server table — the value under
// `.cursor/mcp.json` `mcpServers.<id>` — into the canonical MCPServerSpec. It is
// the inverse of renderMCP: type/command/args/env/url/headers carry over and any
// other native key (timeout, envFile, auth, …) is preserved verbatim in Extra.
// It stays tolerant of a `type` key on read (forward-compat) even though renderMCP
// no longer writes one on remote servers, so a hand-authored remote server that
// still carries `type` captures it rather than dropping it.
func IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	return source.MCPServerSpec{
		Type:    asStr(raw["type"]),
		Command: asStr(raw["command"]),
		Args:    asStrSlice(raw["args"]),
		Env:     asStrMap(raw["env"]),
		URL:     asStr(raw["url"]),
		Headers: asStrMap(raw["headers"]),
		Extra:   claude.ExtraNativeKeys(raw, "type", "command", "args", "env", "url", "headers"),
	}
}

// agentTargeted reports whether the agents allowlist includes cursor. An
// empty/nil list or a "*" entry means all agents are targeted.
func agentTargeted(name string, agents []string) bool {
	if len(agents) == 0 {
		return true
	}
	for _, a := range agents {
		if a == "*" || a == name {
			return true
		}
	}
	return false
}
