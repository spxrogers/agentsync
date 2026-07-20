package codex

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMCP projects canonical MCP servers into a single op targeting
// ~/.codex/config.toml under the `[mcp_servers]` table. op.Content is JSON
// (`{"mcp_servers": {...}}`) — the pipeline's pointer-merge machinery is
// format-agnostic; the merge-toml-keys strategy makes Apply read/write the
// on-disk config.toml as TOML (see settings.go).
func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	servers := map[string]any{}
	var skips []adapter.Skip
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			skips = append(skips, adapter.Skip{
				Component: "mcp",
				Name:      m.ID,
				Reason:    "MCP server is disabled in canonical config",
				Kind:      adapter.SkipDropped,
			})
			continue
		}
		if !agentTargeted("codex", m.Server.Agents) {
			skips = append(skips, adapter.Skip{
				Component: "mcp",
				Name:      m.ID,
				Reason:    "Codex is not in this MCP server's agents allowlist",
				Kind:      adapter.SkipDropped,
			})
			continue
		}
		servers[m.ID] = codexMCPSpec(m.Server)
	}
	// Return the accumulated skips even when nothing lands: a config whose only
	// MCP servers are all disabled/non-targeted must still report each drop, not
	// swallow them behind a nil return.
	if len(servers) == 0 {
		return nil, skips, nil
	}
	ours := map[string]any{"mcp_servers": servers}
	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, skips, fmt.Errorf("marshal codex mcp: %w", err)
	}
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.Config,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "mcp/* (multiple)",
		MergeStrategy: "merge-toml-keys",
	}}, skips, nil
}

// codexMCPSpec projects a canonical MCP server into Codex's native config.toml
// shape. A stdio server keeps command/args split (Codex, unlike OpenCode, uses
// a string `command` + `args` array) and env under `env`. A streamable-HTTP
// server carries `url` and `http_headers` (canonical `headers`). Codex has no
// separate SSE transport, so a canonical `sse` server is represented as a URL
// server too. IngestMCPSpec inverts this.
func codexMCPSpec(s source.MCPServerSpec) map[string]any {
	spec := map[string]any{}
	if isRemoteMCP(s) {
		if s.URL != "" {
			spec["url"] = s.URL
		}
		if len(s.Headers) > 0 {
			spec["http_headers"] = s.Headers
		}
		claude.MergeExtra(spec, s.Extra)
		return spec
	}
	if s.Command != "" {
		spec["command"] = s.Command
	}
	if len(s.Args) > 0 {
		spec["args"] = s.Args
	}
	if len(s.Env) > 0 {
		spec["env"] = s.Env
	}
	claude.MergeExtra(spec, s.Extra)
	return spec
}

// isRemoteMCP decides whether a canonical server maps to Codex's URL (HTTP)
// transport. Codex collapses http and sse into a single streamable-HTTP server;
// an untyped server is classified by whether it carries a URL but no command.
func isRemoteMCP(s source.MCPServerSpec) bool {
	switch s.Type {
	case "http", "sse":
		return true
	case "stdio":
		return false
	default:
		return s.URL != "" && s.Command == ""
	}
}

// IngestMCPSpec translates one Codex-native MCP server table — the value under
// config.toml `[mcp_servers.<id>]` — into the canonical MCPServerSpec. It is the
// inverse of codexMCPSpec (Render). A server carrying a URL is canonicalised to
// the "http" transport; otherwise "stdio". Codex's `http_headers` map back onto
// canonical `headers`. Codex's per-server `enabled` boolean (default-on when the
// key is absent; `enabled = false` disables the server) maps back onto canonical
// `Enabled` — captured only when the key is PRESENT so an absent key stays nil
// (default-on) — and is therefore a modeled key excluded from Extra.
func IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	url := asStr(raw["url"])
	typ := "stdio"
	if url != "" {
		typ = "http"
	}
	return source.MCPServerSpec{
		Type:    typ,
		Command: asStr(raw["command"]),
		Args:    asStrSlice(raw["args"]),
		Env:     asStrMap(raw["env"]),
		URL:     url,
		Headers: asStrMap(raw["http_headers"]),
		Enabled: asBoolPtr(raw["enabled"]),
		Extra:   claude.ExtraNativeKeys(raw, "command", "args", "env", "url", "http_headers", "enabled"),
	}
}

func asStr(v any) string { s, _ := v.(string); return s }

// asBoolPtr returns a pointer to v when it is a bool, else nil. Used so an
// absent Codex `enabled` key stays nil (default-on) while a present one — true
// or false — is captured into the tri-state *bool.
func asBoolPtr(v any) *bool {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}

func asStrSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asStrMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}
