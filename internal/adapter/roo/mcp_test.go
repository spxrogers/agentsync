package roo_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/roo"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestRoo_MCPTransportRoundTrip is artifact-anchored: for each transport it writes
// a spec-complete .roo/mcp.json on disk, ingests it, renders back, applies, and
// asserts the on-disk artifact is a FIXED POINT (byte-stable across a second
// ingest→render→apply). This pins the transport normalization documented in
// docs/capability-matrix.md → "Roo Code / MCP remote": stdio stays keyless, an
// explicit `sse` stays `sse`, and an UNTYPED url-bearing server canonicalizes to
// `http` (rendered as `streamable-http`) and thereafter round-trips stably.
func TestRoo_MCPTransportRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		native   map[string]any // the server entry as first written to disk
		wantType string         // canonical Type after the first ingest
		wantURL  string
		wantCmd  string
	}{
		{
			name:     "stdio",
			native:   map[string]any{"command": "npx", "args": []any{"-y", "pkg"}, "env": map[string]any{"K": "v"}},
			wantType: "stdio",
			wantCmd:  "npx",
		},
		{
			name:     "http",
			native:   map[string]any{"type": "streamable-http", "url": "https://x/mcp", "headers": map[string]any{"A": "b"}},
			wantType: "http",
			wantURL:  "https://x/mcp",
		},
		{
			name:     "sse",
			native:   map[string]any{"type": "sse", "url": "https://x/sse"},
			wantType: "sse",
			wantURL:  "https://x/sse",
		},
		{
			// Type=="" with a URL and no command: the acknowledged normalization —
			// canonicalizes to http, rendered as streamable-http, stable thereafter.
			name:     "untyped-url",
			native:   map[string]any{"url": "https://x/mcp"},
			wantType: "http",
			wantURL:  "https://x/mcp",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proj := t.TempDir()
			mcpPath := filepath.Join(proj, ".roo", "mcp.json")
			writeRooServer(t, mcpPath, "srv", tc.native)
			a := roo.New(roo.Options{TargetRoot: t.TempDir()})

			// 1) Ingest the on-disk fixture and assert the canonical transport.
			s := ingestOneRooServer(t, a, proj)
			if s.Type != tc.wantType || s.URL != tc.wantURL || s.Command != tc.wantCmd {
				t.Fatalf("ingest: got Type=%q URL=%q Command=%q, want Type=%q URL=%q Command=%q",
					s.Type, s.URL, s.Command, tc.wantType, tc.wantURL, tc.wantCmd)
			}

			// 2) Render+apply back to disk (first capture), then again — the
			//    artifact must be a fixed point (byte-stable).
			first := renderApplyRooMCP(t, a, proj, s)
			s2 := ingestOneRooServer(t, a, proj)
			second := renderApplyRooMCP(t, a, proj, s2)
			if !bytes.Equal(first, second) {
				t.Fatalf("on-disk .roo/mcp.json not stable across round-trip:\nfirst:\n%s\nsecond:\n%s", first, second)
			}

			// 3) The stable on-disk shape keeps url and carries the right transport.
			srv := readRooServer(t, mcpPath, "srv")
			if tc.wantURL != "" && srv["url"] != tc.wantURL {
				t.Fatalf("url not preserved on disk: %v", srv)
			}
			switch tc.wantType {
			case "sse":
				if srv["type"] != "sse" {
					t.Fatalf("sse must stay sse on disk, got %v", srv["type"])
				}
			case "http":
				if srv["type"] != "streamable-http" {
					t.Fatalf("remote must render type=streamable-http on disk, got %v", srv["type"])
				}
			case "stdio":
				if _, has := srv["type"]; has {
					t.Fatalf("stdio must not carry a type key: %v", srv)
				}
			}
		})
	}
}

// TestRoo_CapabilitySkipCorrespondence pins the capability/skip contract: every
// component Roo SUPPORTS (MCP, memory, command — its documented set) produces a
// FileOp and is never reported as a dropped Skip, and every component it does NOT
// support (skill, subagent, hook, LSP) produces a SkipDropped and never a FileOp.
// No component may be both silently unrendered and unskipped. (Roo exposes no
// Capabilities() bitmask; the supported set here is Roo's documented one.)
func TestRoo_CapabilitySkipCorrespondence(t *testing.T) {
	proj := t.TempDir()
	full := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "srv", Server: source.MCPServerSpec{Type: "stdio", Command: "x"}}},
		Memory:     source.Memory{Body: "rules\n"},
		Commands:   []source.Command{{Name: "deploy", Body: "do it\n"}},
		Skills:     []source.Skill{{Name: "s1", Frontmatter: map[string]any{"name": "s1"}, Body: "x\n"}},
		Subagents:  []source.Subagent{{Name: "a1", Body: "y\n"}},
		Hooks:      []source.Hook{{Event: "PreToolUse", Type: "command", Command: "echo"}},
		LSPServers: []source.LSPServer{{ID: "gopls", Spec: source.LSPServerSpec{Command: "gopls"}}},
	}
	c := full
	c.Project = &full // project scope renders the overlay; MCP renders at project scope
	a := roo.New(roo.Options{TargetRoot: t.TempDir()})
	ops, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}

	// Supported: a FileOp exists and there is no dropped Skip for the component.
	for _, sup := range []struct{ component, suffix string }{
		{"mcp", ".roo/mcp.json"},
		{"memory", ".roo/rules/agentsync.md"},
		{"command", ".roo/commands/deploy.md"},
	} {
		if findOp(ops, sup.suffix) == nil {
			t.Errorf("supported component %q produced no FileOp (%q)", sup.component, sup.suffix)
		}
		for _, s := range skips {
			if s.Component == sup.component && s.Kind == adapter.SkipDropped {
				t.Errorf("supported component %q must not be a dropped skip: %+v", sup.component, s)
			}
		}
	}

	// Unsupported: a SkipDropped exists and no FileOp targets the component.
	for _, uns := range []struct{ component, name string }{
		{"skill", "s1"},
		{"subagent", "a1"},
		{"hook", "PreToolUse"},
		{"lsp", "gopls"},
	} {
		if !hasSkip(skips, uns.component, uns.name, adapter.SkipDropped) {
			t.Errorf("unsupported component %q %q must report a dropped skip; skips=%+v", uns.component, uns.name, skips)
		}
	}
	// Skills/subagents/hooks/LSP have no Roo render path at all.
	for _, suffix := range []string{"skills", "agents", "hooks", "lsp"} {
		if op := findOp(ops, suffix); op != nil {
			t.Errorf("unsupported component leaked a FileOp: %+v", op)
		}
	}
}

// --- helpers (artifact I/O) ---

func writeRooServer(t *testing.T, mcpPath, id string, native map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{id: native}}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ingestOneRooServer(t *testing.T, a *roo.Adapter, proj string) source.MCPServerSpec {
	t.Helper()
	c, err := a.Ingest(adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.MCPServers) != 1 {
		t.Fatalf("want 1 ingested server, got %d", len(c.MCPServers))
	}
	return c.MCPServers[0].Server
}

func renderApplyRooMCP(t *testing.T, a *roo.Adapter, proj string, s source.MCPServerSpec) []byte {
	t.Helper()
	c := projOf(source.Canonical{MCPServers: []source.MCPServer{{ID: "srv", Server: s}}})
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(proj, ".roo", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readRooServer(t *testing.T, mcpPath, id string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	servers, _ := top["mcpServers"].(map[string]any)
	srv, _ := servers[id].(map[string]any)
	if srv == nil {
		t.Fatalf("server %q missing on disk: %s", id, data)
	}
	return srv
}
