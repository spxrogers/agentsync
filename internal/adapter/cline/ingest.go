package cline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// Ingest reads Cline's native config and returns a partial source.Canonical. It
// is the inverse of Render, honoring the same scope asymmetry: MCP from the CLI's
// ~/.cline/mcp.json (user scope), rules + workflows from the .clinerules/ tree.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical

	// MCP from ~/.cline/mcp.json (user scope only).
	if p.MCP != "" {
		data, present, err := adapter.ReadFileOptional(p.MCP)
		if err != nil {
			return c, fmt.Errorf("read %s: %w", p.MCP, err)
		}
		if present {
			var top map[string]any
			if err := json.Unmarshal(data, &top); err != nil {
				return c, fmt.Errorf("parse %s: %w", p.MCP, err)
			}
			if servers, ok := top["mcpServers"].(map[string]any); ok {
				for id, raw := range servers {
					spec, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					c.MCPServers = append(c.MCPServers, source.MCPServer{ID: id, Server: IngestMCPSpec(spec)})
				}
			}
		}
	}

	// Commands from .clinerules/workflows/<name>.md (project scope; plain markdown).
	// Ownership-scoped like memory ingest: only workflows agentsync itself wrote —
	// those carrying the leading managedWorkflowMarker — are captured (the marker is
	// stripped so the canonical body round-trips clean). A human-authored workflow in
	// .clinerules/workflows/ has no marker and is left untouched, so ingest never
	// pulls a foreign workflow into the canonical model (the over-capture asymmetry
	// this closes: memory reads only the agentsync-owned agentsync.md, but workflows
	// used to read every .md).
	//
	// Cline implements no diagnostics sink (WarnEmitter), so a per-workflow read
	// error is RETURNED rather than warned — surfaced loudly, never swallowed into a
	// silently-short Commands slice. An absent entry (rare race) stays a benign skip.
	if p.WorkflowsDir != "" {
		entries, present, err := adapter.ReadDirOptional(p.WorkflowsDir)
		if err != nil {
			return c, fmt.Errorf("read workflows dir %s: %w", p.WorkflowsDir, err)
		}
		if present {
			for _, e := range entries {
				if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
					continue
				}
				wfPath := filepath.Join(p.WorkflowsDir, e.Name())
				data, err := os.ReadFile(wfPath)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					return c, fmt.Errorf("read workflow %s: %w", wfPath, err)
				}
				body, owned := stripManagedWorkflow(string(data))
				if !owned {
					continue // human-authored workflow — not agentsync-owned
				}
				name := e.Name()[:len(e.Name())-len(".md")]
				c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: map[string]any{}, Body: body})
			}
		}
	}

	// Memory from .clinerules/agentsync.md (project scope; plain markdown).
	if p.RulesDir != "" {
		ruleFile := filepath.Join(p.RulesDir, memoryRuleFile)
		data, present, err := adapter.ReadFileOptional(ruleFile)
		if err != nil {
			return c, fmt.Errorf("read memory %s: %w", ruleFile, err)
		}
		if present {
			c.Memory.Body = source.StripManagedBanner(string(data)) // banner stripped — see claude/ingest.go
		}
	}

	return c, nil
}

func asStr(v any) string { s, _ := v.(string); return s }

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
