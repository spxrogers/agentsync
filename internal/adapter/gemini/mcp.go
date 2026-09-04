package gemini

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderMCP projects canonical MCP servers into a single op targeting
// `.gemini/settings.json` under the `mcpServers` object. A stdio server keeps
// command/args/env; a remote server uses Gemini's two-key transport split —
// `url` for SSE, `httpUrl` for HTTP streaming — with `headers`. op.Content is
// JSON and the merge-jsonc-keys strategy preserves a hand-authored settings.json's
// foreign keys (and the `hooks` section the adapter also owns).
func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	servers := map[string]any{}
	var skips []adapter.Skip
	for _, m := range c.MCPServers {
		if m.Server.Enabled != nil && !*m.Server.Enabled {
			continue
		}
		if !agentTargeted("gemini", m.Server.Agents) {
			continue
		}
		servers[m.ID] = geminiMCPSpec(m.Server)
		// Gemini expands $VAR/${VAR}/${VAR:-default} in EVERY settings.json string
		// with NO escape (upstream envVarResolver.ts). A resolved secret value whose
		// '$' the expander would act on is at the mercy of the user's runtime env —
		// agentsync cannot make it survive intact — so surface the risk rather than
		// silently ship a value Gemini may re-expand. See geminiDollarRiskFields.
		if fields := geminiDollarRiskFields(m.Server); len(fields) > 0 {
			skips = append(skips, adapter.Skip{
				Component: "mcp",
				Name:      m.ID,
				Reason:    "Gemini expands $VAR/${VAR} in every settings.json string with no escape (upstream envVarResolver.ts); resolved value(s) in " + strings.Join(fields, ", ") + " contain '$' and may be corrupted at Gemini read time",
				Kind:      adapter.SkipReduced,
			})
		}
	}
	if len(servers) == 0 {
		return nil, skips, nil
	}
	ours := map[string]any{"mcpServers": servers}
	body, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal gemini mcp: %w", err)
	}
	return []adapter.FileOp{{
		Action:        adapter.ActionWrite,
		Path:          p.Settings,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "mcp/* (multiple)",
		MergeStrategy: "merge-jsonc-keys",
	}}, skips, nil
}

// geminiVarExpansionRe mirrors Gemini CLI's settings.json variable matcher
// (packages/cli/src/utils/envVarResolver.ts, regex
// /\$(?:(\w+)|{([^}]+?)(?::-([^}]*))?})/g): it matches $VAR, ${VAR}, and
// ${VAR:-default}. Gemini applies this expansion to EVERY settings.json string
// value — env, headers, url/httpUrl — and provides NO escape for a literal '$'
// (verified upstream: envVarResolver.ts has no $$/backslash handling, and its
// tests cover no escape; an undefined var is left as-is, but a '$' sequence that
// happens to name a var defined in the user's runtime env is expanded and the
// value corrupted). Because there is no faithful escape, geminiMCPSpec writes the
// resolved value VERBATIM (a fabricated "$$"/"\$" would itself corrupt it — Gemini
// does not un-double "$$"), and renderMCP reports a SkipReduced for any value this
// pattern matches so the user is told the value may not survive Gemini read-back.
var geminiVarExpansionRe = regexp.MustCompile(`\$(?:\w+|\{[^}]+\})`)

// geminiDollarRiskFields returns the sorted field labels of a resolved MCP spec
// whose value Gemini's variable expansion would act on (see geminiVarExpansionRe).
// Scope matches the resolved-secret sites #166 covers and geminiMCPSpec writes:
// url/httpUrl + headers for a remote server, env for stdio. (command/args are also
// string values Gemini expands, but are not resolved-secret targets in practice
// and are out of #166's scope.)
func geminiDollarRiskFields(s source.MCPServerSpec) []string {
	var out []string
	switch {
	case isSSE(s), isHTTP(s):
		if s.URL != "" && geminiVarExpansionRe.MatchString(s.URL) {
			key := "url"
			if isHTTP(s) {
				key = "httpUrl"
			}
			out = append(out, key)
		}
		for k, v := range s.Headers {
			if geminiVarExpansionRe.MatchString(v) {
				out = append(out, "headers["+k+"]")
			}
		}
	default:
		for k, v := range s.Env {
			if geminiVarExpansionRe.MatchString(v) {
				out = append(out, "env["+k+"]")
			}
		}
	}
	sort.Strings(out)
	return out
}

// geminiMCPSpec projects a canonical MCP server into Gemini's settings.json
// shape. stdio → command/args/env; SSE → url; HTTP → httpUrl; both remote
// transports carry headers. Passthrough native keys (cwd, timeout, trust)
// re-merge via Extra. IngestMCPSpec inverts this.
func geminiMCPSpec(s source.MCPServerSpec) map[string]any {
	spec := map[string]any{}
	switch {
	case isSSE(s):
		if s.URL != "" {
			spec["url"] = s.URL
		}
		if len(s.Headers) > 0 {
			spec["headers"] = s.Headers
		}
	case isHTTP(s):
		if s.URL != "" {
			spec["httpUrl"] = s.URL
		}
		if len(s.Headers) > 0 {
			spec["headers"] = s.Headers
		}
	default:
		if s.Command != "" {
			spec["command"] = s.Command
		}
		if len(s.Args) > 0 {
			spec["args"] = s.Args
		}
		if len(s.Env) > 0 {
			spec["env"] = s.Env
		}
	}
	claude.MergeExtra(spec, s.Extra)
	return spec
}

// isSSE reports whether a canonical server maps to Gemini's `url` (SSE) key.
func isSSE(s source.MCPServerSpec) bool { return s.Type == "sse" }

// isHTTP reports whether a canonical server maps to Gemini's `httpUrl` (HTTP
// streaming) key. An untyped server carrying a URL but no command is treated as
// HTTP (the modern default).
func isHTTP(s source.MCPServerSpec) bool {
	if s.Type == "http" {
		return true
	}
	return s.Type == "" && s.URL != "" && s.Command == ""
}

// IngestMCPSpec translates one Gemini-native MCP server table — the value under
// settings.json `mcpServers.<id>` — into the canonical MCPServerSpec. It is the
// inverse of geminiMCPSpec: `httpUrl` → http transport, `url` → sse transport,
// otherwise stdio. Native keys agentsync doesn't model are kept in Extra.
//
// There is no `$`-escape to INVERT here: Gemini offers no escape syntax (see
// geminiVarExpansionRe), so geminiMCPSpec writes resolved values verbatim and this
// reads them verbatim — the value round-trips losslessly without any transform.
func IngestMCPSpec(raw map[string]any) source.MCPServerSpec {
	httpURL := asStr(raw["httpUrl"])
	sseURL := asStr(raw["url"])
	typ := "stdio"
	url := ""
	switch {
	case httpURL != "":
		typ, url = "http", httpURL
	case sseURL != "":
		typ, url = "sse", sseURL
	}
	return source.MCPServerSpec{
		Type:    typ,
		Command: asStr(raw["command"]),
		Args:    asStrSlice(raw["args"]),
		Env:     asStrMap(raw["env"]),
		URL:     url,
		Headers: asStrMap(raw["headers"]),
		Extra:   claude.ExtraNativeKeys(raw, "command", "args", "env", "url", "httpUrl", "headers"),
	}
}

// agentTargeted reports whether the agents allowlist includes gemini. An
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
