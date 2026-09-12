package cli

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// mcpDialectFixtures are the two canonical MCP servers whose ROUND TRIP through
// an adapter's own native bytes is what this guard checks: one stdio server
// (command + args + env) and one remote server (url + headers). Between them
// they populate every modeled MCPServerSpec field any dialect has a slot for,
// so a dialect that silently demotes a modeled field into the `Extra`
// passthrough is caught whichever side of the transport split it lives on.
func mcpDialectFixtures() []source.MCPServer {
	return []source.MCPServer{
		{ID: "stdiosrv", Server: source.MCPServerSpec{
			Type:    "stdio",
			Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-github"},
			Env:     map[string]string{"GITHUB_TOKEN": "xyz"},
		}},
		{ID: "remotesrv", Server: source.MCPServerSpec{
			Type:    "http",
			URL:     "https://api.example.com/mcp",
			Headers: map[string]string{"Authorization": "Bearer tok"},
		}},
	}
}

// TestMCPSpecIngester_CoversEveryKeyMergeMCPRenderer is the registry-wide guard
// for the adapter.MCPSpecIngester contract, and the reason reconcile's key-level
// write-back no longer carries a hand-maintained pointer-root allowlist.
//
// Two assertions, both anchored to the ADAPTER'S OWN ON-DISK BYTES rather than
// to a model (CLAUDE.md, "models must stay faithful to their on-disk
// artifacts"):
//
//  1. COVERAGE — every registered adapter that emits an MCP key-merge FileOp
//     MUST implement adapter.MCPSpecIngester. Without this, adding an
//     MCP-capable agent silently reintroduces the old failure mode: its key
//     items are refused (a root key nothing recognises) or, worse, translated
//     through whichever adapter happens to share its root key.
//  2. FIDELITY — feeding the adapter's OWN rendered native entry back through
//     its OWN IngestMCPSpec must return the modeled fields as MODELED fields,
//     never demoted into the `Extra` passthrough. This is what makes "route the
//     write-back through the rendering adapter" a guarantee rather than a hope:
//     the routing is correct only if each implementor's inverse is faithful to
//     its own render. (The ROUTING itself is pinned at the CLI level, by the
//     per-dialect reconcile write-back tests — a guard at this layer cannot see
//     which translator reconcile picks.)
//
// It runs over the whole registry, so it covers all 22 generic specs as well as
// the nine deep adapters.
func TestMCPSpecIngester_CoversEveryKeyMergeMCPRenderer(t *testing.T) {
	testenv.RequireContainer(t)
	fixture := source.Canonical{MCPServers: mcpDialectFixtures()}
	resolved := secrets.ForRender(fixture)
	want := map[string]source.MCPServerSpec{}
	for _, m := range fixture.MCPServers {
		want[m.ID] = m.Server
	}

	reg := registryFactory()
	checked := 0
	for _, name := range reg.Names() {
		a := reg.Lookup(name)
		if a == nil {
			t.Fatalf("registry has name %q but Lookup returned nil", name)
		}
		t.Run(name, func(t *testing.T) {
			for _, sc := range []struct {
				name    string
				scope   adapter.Scope
				project string
			}{
				{"user", adapter.ScopeUser, ""},
				{"project", adapter.ScopeProject, t.TempDir()},
			} {
				ops, _, err := a.Render(resolved, sc.scope, sc.project)
				if err != nil {
					t.Fatalf("[%s/%s] Render: %v", name, sc.name, err)
				}
				for _, op := range ops {
					if !render.IsKeyMerge(op.MergeStrategy) || !isMCPSourceID(op.SourceID) {
						continue
					}
					ing, ok := a.(adapter.MCPSpecIngester)
					if !ok {
						t.Fatalf("[%s/%s] emits an MCP key-merge op (%s) but does NOT implement "+
							"adapter.MCPSpecIngester — reconcile's key-level write-back has no "+
							"dialect for it and must refuse or mistranslate it",
							name, sc.name, op.Path)
					}
					var top map[string]any
					if err := json.Unmarshal(op.Content, &top); err != nil {
						t.Fatalf("[%s/%s] decode rendered MCP op content: %v", name, sc.name, err)
					}
					servers := mcpRootServers(top, want)
					if servers == nil {
						t.Fatalf("[%s/%s] rendered MCP op %s carries no root object holding the "+
							"fixture servers; content=%s", name, sc.name, op.Path, op.Content)
					}
					for id, raw := range servers {
						entry, ok := raw.(map[string]any)
						if !ok {
							t.Fatalf("[%s/%s] rendered server %q is not an object", name, sc.name, id)
						}
						got := ing.IngestMCPSpec(entry)
						assertMCPRoundTrip(t, name, sc.name, id, want[id], got)
						checked++
					}
				}
			}
		})
	}
	// Guard against a vacuously-green run.
	if checked == 0 {
		t.Fatal("no adapter rendered an MCP key-merge op for the fixture; the guard is vacuous")
	}
}

// isMCPSourceID reports whether a FileOp's SourceID names the MCP component —
// the same SourceID-derived kind test reconcile's write-back and plugin-owner
// lookup use, rather than a pointer-root allowlist.
func isMCPSourceID(id string) bool { return len(id) >= 4 && id[:4] == "mcp/" }

// mcpRootServers finds the rendered op's server map without knowing the
// adapter's root key: it is the top-level value that is an object keyed by the
// fixture server ids. Deriving it beats a second copy of the root-key table.
func mcpRootServers(top map[string]any, want map[string]source.MCPServerSpec) map[string]any {
	for _, v := range top {
		m, ok := v.(map[string]any)
		if !ok || len(m) == 0 {
			continue
		}
		all := true
		for id := range m {
			if _, known := want[id]; !known {
				all = false
				break
			}
		}
		if all {
			return m
		}
	}
	return nil
}

// assertMCPRoundTrip pins the modeled fields a dialect DOES have a slot for. It
// deliberately does not assert Type: the transport-keyless dialects have nowhere
// to record it and canonicalize sse→http by documented design
// (docs/capability-matrix.md). What it does assert is that a field the dialect
// wrote is read back as the MODELED field and not as Extra passthrough.
func assertMCPRoundTrip(t *testing.T, agent, scope, id string, want, got source.MCPServerSpec) {
	t.Helper()
	if want.Command != "" {
		if got.Command != want.Command {
			t.Errorf("[%s/%s] %s: Command = %q, want %q", agent, scope, id, got.Command, want.Command)
		}
		if !reflect.DeepEqual(got.Args, want.Args) {
			t.Errorf("[%s/%s] %s: Args = %v, want %v (a modeled field demoted to Extra?)", agent, scope, id, got.Args, want.Args)
		}
		if !reflect.DeepEqual(got.Env, want.Env) {
			t.Errorf("[%s/%s] %s: Env = %v, want %v (a modeled field demoted to Extra?)", agent, scope, id, got.Env, want.Env)
		}
	}
	if want.URL != "" {
		if got.URL != want.URL {
			t.Errorf("[%s/%s] %s: URL = %q, want %q (the dialect's url key not inverted?)", agent, scope, id, got.URL, want.URL)
		}
		if !reflect.DeepEqual(got.Headers, want.Headers) {
			t.Errorf("[%s/%s] %s: Headers = %v, want %v", agent, scope, id, got.Headers, want.Headers)
		}
	}
	for _, modeled := range []string{"type", "command", "args", "env", "url", "headers"} {
		if _, bad := got.Extra[modeled]; bad {
			t.Errorf("[%s/%s] %s: Extra carries the modeled key %q — the translator demoted a "+
				"canonical field into passthrough", agent, scope, id, modeled)
		}
	}
}
