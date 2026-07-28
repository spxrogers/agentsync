package continuedev

import (
	"fmt"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

const (
	// defaultBlockVersion / defaultBlockSchema are the block-header values
	// Continue's documented MCP block schema requires (a `name`/`version`/`schema`
	// header wrapping the `mcpServers` list — https://docs.continue.dev/). renderMCP
	// supplies them unless a hand-authored block carried a NON-default header that
	// IngestMCPSpec preserved into Extra (see blockHeader / applyBlockHeader).
	defaultBlockVersion = "0.0.1"
	defaultBlockSchema  = "v1"

	// blockVersionExtraKey / blockSchemaExtraKey are the reserved Extra keys under
	// which the block-level `version`/`schema` header is round-tripped. They live at
	// the BLOCK level, not on the inner server, so continueMCPServerMap strips them
	// before merging Extra into the server map (renderMCP lifts them to the header).
	blockVersionExtraKey = "__block_version"
	blockSchemaExtraKey  = "__block_schema"
)

// blockHeaderExtraKeys is the set of reserved block-header keys, used to keep them
// out of the inner server map on render.
var blockHeaderExtraKeys = map[string]bool{
	blockVersionExtraKey: true,
	blockSchemaExtraKey:  true,
}

// renderMCP projects each canonical MCP server into its own YAML block file at
// `.continue/mcpServers/<id>.yaml` (Continue's documented per-server block: a
// `name`/`version`/`schema` header wrapping a single-element `mcpServers` list).
// stdio keeps command/args/env; a remote server uses Continue's transport name
// (`streamable-http` for HTTP, `sse` for SSE) + `url`, with auth headers under
// `requestOptions.headers`. Each block file is wholly owned by agentsync (a
// whole-file replace), so there is no key-merge — a user's other server blocks in
// the same directory are untouched.
//
// The block `version`/`schema` header is ROUND-TRIPPED, not regenerated: a
// hand-authored block's non-default header (captured into Extra by IngestMCPSpec)
// is preferred over the `0.0.1`/`v1` defaults so the next apply never silently
// rewrites it. A server that carries BOTH a command and a url with no explicit
// `type` is ambiguous — Continue's block is single-transport — so agentsync
// renders it as stdio (command wins) and reports the dropped url via a reduced
// Skip rather than silently discarding it; an explicitly-typed stdio server that
// also carries a url drops the url just the same and gets the same report. The
// returned skips are threaded out to Render.
func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted("continue", m.Server.Agents) {
			continue
		}
		// A Continue block is single-transport, so a url on a server that
		// renders as stdio is dropped — and the drop is NEVER silent. Two ways
		// to get there: command+url with no type (the documented ambiguity
		// rule: command wins) and an EXPLICIT `type: "stdio"` that also
		// carries a url (the user chose stdio; the url is unused). Both report
		// the dropped url via the same reduced Skip, with wording matching how
		// the transport was chosen.
		//
		// The reason names the FIELD, never its value: Render runs on the
		// secret-RESOLVED canonical, and `url` is secret-bearing
		// (secrets.walkSecretFields), so interpolating it here would print a
		// vault secret in cleartext through `explain` (which emits skip reasons
		// as metadata) and the apply translation report. See the doc comment on
		// adapter.Skip.Reason and TestSkipReasonsNeverCarrySecretValues.
		switch {
		case m.Server.Type == "" && m.Server.Command != "" && m.Server.URL != "":
			skips = append(skips, adapter.Skip{
				Component: "mcp",
				Name:      m.ID,
				Reason:    "server declares both a command and a url with no explicit type; a Continue block is single-transport, so it renders as stdio (command wins) and the url is dropped — set type to \"http\" or \"sse\" to render it as a remote server instead",
				Kind:      adapter.SkipReduced,
			})
		case m.Server.Type == "stdio" && m.Server.URL != "":
			skips = append(skips, adapter.Skip{
				Component: "mcp",
				Name:      m.ID,
				Reason:    "server is explicitly typed stdio but also carries a url; a Continue block is single-transport, so the url is unused and dropped — set type to \"http\" or \"sse\" to render it as a remote server instead",
				Kind:      adapter.SkipReduced,
			})
		}
		version, schema := blockHeader(m.Server.Extra)
		block := map[string]any{
			"name":       m.ID,
			"version":    version,
			"schema":     schema,
			"mcpServers": []any{continueMCPServerMap(m.ID, m.Server)},
		}
		body, err := yaml.Marshal(block)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal continue mcp %s: %w", m.ID, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.MCPDir, m.ID+".yaml"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("mcp", m.ID+".toml"),
			MergeStrategy: "replace",
		})
	}
	return ops, skips, nil
}

// blockHeader returns the version/schema for a server's rendered block, preferring
// a header captured from a hand-authored block (Extra["__block_version"] /
// Extra["__block_schema"]) over the required defaults so a user's non-default
// header round-trips instead of being regenerated to 0.0.1/v1.
func blockHeader(extra map[string]any) (version, schema string) {
	version, schema = defaultBlockVersion, defaultBlockSchema
	if v, ok := extra[blockVersionExtraKey].(string); ok && v != "" {
		version = v
	}
	if s, ok := extra[blockSchemaExtraKey].(string); ok && s != "" {
		schema = s
	}
	return version, schema
}

// applyBlockHeader preserves a Continue block's version/schema header into the
// server's Extra (under the reserved keys) so renderMCP round-trips it. Default
// (or absent) values are left uncaptured to keep the canonical model clean —
// renderMCP re-supplies the required defaults. Inverse of blockHeader.
func applyBlockHeader(spec *source.MCPServerSpec, version, schema string) {
	set := func(key, val, def string) {
		if val == "" || val == def {
			return
		}
		if spec.Extra == nil {
			spec.Extra = map[string]any{}
		}
		spec.Extra[key] = val
	}
	set(blockVersionExtraKey, version, defaultBlockVersion)
	set(blockSchemaExtraKey, schema, defaultBlockSchema)
}

// continueMCPServerMap builds the inner mcpServers entry for one server.
// requestOptions is rebuilt by deep-merging the canonical Headers into any
// non-headers requestOptions subkeys preserved in Extra (timeout, verifySsl,
// proxy, …) — see IngestMCPSpec — so a captured native block's request options
// survive the round trip instead of being shadowed by the headers map.
// MergeExtra skips keys already present, so the explicit requestOptions here
// wins over the Extra copy. The reserved block-header keys are stripped first —
// they belong to the block, not the inner server (see blockHeader).
func continueMCPServerMap(id string, s source.MCPServerSpec) map[string]any {
	srv := map[string]any{"name": id}
	if isRemote(s) {
		srv["type"] = continueTransport(s)
		if s.URL != "" {
			srv["url"] = s.URL
		}
		ro := map[string]any{}
		if rest, ok := s.Extra["requestOptions"].(map[string]any); ok {
			for k, v := range rest {
				ro[k] = v
			}
		}
		if len(s.Headers) > 0 {
			ro["headers"] = s.Headers
		}
		if len(ro) > 0 {
			srv["requestOptions"] = ro
		}
	} else {
		srv["type"] = "stdio"
		if s.Command != "" {
			srv["command"] = s.Command
		}
		if len(s.Args) > 0 {
			srv["args"] = s.Args
		}
		if len(s.Env) > 0 {
			srv["env"] = s.Env
		}
	}
	claude.MergeExtra(srv, withoutBlockHeader(s.Extra))
	return srv
}

// withoutBlockHeader returns a copy of extra with the reserved block-header keys
// removed, so continueMCPServerMap never leaks them into the inner server map.
func withoutBlockHeader(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	out := make(map[string]any, len(extra))
	for k, v := range extra {
		if blockHeaderExtraKeys[k] {
			continue
		}
		out[k] = v
	}
	return out
}

// isRemote reports whether a canonical server maps to one of Continue's remote
// transports. An untyped server carrying a URL but no command is treated as
// remote; an untyped server carrying BOTH a command and a url is NOT remote (it
// renders as stdio) — renderMCP reports the dropped url via a Skip so that
// single-transport choice is never silent.
func isRemote(s source.MCPServerSpec) bool {
	switch s.Type {
	case "http", "sse":
		return true
	case "stdio":
		return false
	default:
		return s.URL != "" && s.Command == ""
	}
}

// continueTransport names Continue's transport for a remote server: SSE keeps
// `sse`; everything else (http, or untyped-with-url) uses streamable HTTP.
func continueTransport(s source.MCPServerSpec) string {
	if s.Type == "sse" {
		return "sse"
	}
	return "streamable-http"
}

// IngestMCPSpec translates one Continue-native server entry (a map under a block's
// `mcpServers` list) into the canonical MCPServerSpec. Inverse of
// continueMCPServerMap: `streamable-http` → http, `sse` → sse, otherwise stdio;
// `requestOptions.headers` → canonical headers, and any OTHER requestOptions
// subkey (timeout, verifySsl, proxy, …) is preserved verbatim in
// Extra["requestOptions"] so the round trip can rebuild the full object —
// dropping it silently would let the next apply destroy it on disk. Native keys
// agentsync doesn't model (e.g. cwd) are preserved in Extra.
func IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	canonType := "stdio"
	switch asStr(raw["type"]) {
	case "streamable-http", "http":
		canonType = "http"
	case "sse":
		canonType = "sse"
	}
	var headers map[string]string
	extra := claude.ExtraNativeKeys(raw, "name", "type", "command", "args", "env", "url", "requestOptions")
	if ro, ok := raw["requestOptions"].(map[string]any); ok {
		headers = asStrMap(ro["headers"])
		residual := map[string]any{}
		for k, v := range ro {
			if k != "headers" {
				residual[k] = v
			}
		}
		if len(residual) > 0 {
			if extra == nil {
				extra = map[string]any{}
			}
			extra["requestOptions"] = residual
		}
	}
	return source.MCPServerSpec{
		Type:    canonType,
		Command: asStr(raw["command"]),
		Args:    asStrSlice(raw["args"]),
		Env:     asStrMap(raw["env"]),
		URL:     asStr(raw["url"]),
		Headers: headers,
		Extra:   extra,
	}
}

// agentTargeted reports whether the agents allowlist includes continue. An
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
