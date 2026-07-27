package cli

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// canary is a value no adapter could produce on its own, so any occurrence in a
// Skip reason came from the canonical model we planted it in.
const canary = "AGENTSYNC-SECRET-CANARY-b7f3e1"

// TestSkipReasonsNeverCarrySecretValues is the guard behind the doc contract on
// adapter.Skip.Reason.
//
// Adapters Render from the secret-RESOLVED canonical, so every secret-bearing
// field (secrets.walkSecretFields: MCP/LSP Command,URL,Args,Env,Headers and Hook
// Command) holds live cleartext during Render. Skip reasons are then emitted as
// METADATA — by `agentsync explain` in both text and --json, and by the apply
// translation report — and `explain` documents itself as printing metadata only,
// which is what lets it answer against a LOCKED vault. An adapter that
// interpolates one of those field VALUES into a reason therefore prints a vault
// secret through a command the user believes is safe.
//
// That is not hypothetical: the continue adapter shipped exactly this bug,
// putting %q of m.Server.URL into a reduced-Skip reason, so `agentsync explain
// ~/.continue/mcpServers/<id>.yaml` printed the resolved URL two lines above the
// literal text "(reference only; never the value)".
//
// This test plants a canary in every secret-bearing field of every component
// shape, renders through EVERY registered adapter (deep + breadth tier), and
// fails if the canary reaches a reason. Naming the field is fine; interpolating
// its value is not.
func TestSkipReasonsNeverCarrySecretValues(t *testing.T) {
	testenv.RequireContainer(t)
	root := t.TempDir()
	t.Setenv("AGENTSYNC_TARGET_ROOT", root)

	c := canonicalWithSecretCanaries()

	reg := registryFactory()
	names := reg.Names()
	if len(names) == 0 {
		t.Fatal("registry is empty; this guard would vacuously pass")
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			a := reg.Lookup(name)
			if a == nil {
				t.Fatalf("registry lists %q but Lookup returned nil", name)
			}
			// Render is the egress that receives resolved secrets. ForRender
			// wraps the model unchanged, which is what we want: the canaries are
			// already literal values, exactly as a resolved ${secret:…} would be.
			_, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
			if err != nil {
				// An adapter that cannot render this shape at all still tells us
				// nothing about reasons; skip rather than fail the guard.
				t.Skipf("render: %v", err)
			}
			for _, s := range skips {
				if strings.Contains(s.Reason, canary) {
					t.Errorf("adapter %q leaked a secret-bearing field VALUE into a Skip reason.\n"+
						"  component: %s %s\n  reason:    %s\n\n"+
						"Skip reasons are printed as metadata by `agentsync explain` (text and --json) and the "+
						"apply translation report, both of which promise never to print destination content — and "+
						"Render runs on the secret-RESOLVED canonical, so this value is live cleartext.\n"+
						"Name the FIELD instead of interpolating its value; Component+Name already identify the item. "+
						"See the doc comment on adapter.Skip.Reason.",
						name, s.Component, s.Name, s.Reason)
				}
			}
		})
	}
}

// canonicalWithSecretCanaries builds a canonical whose every secret-bearing
// field holds the canary, in the shapes most likely to provoke a reduced Skip:
// a server carrying BOTH a command and a url (the single-transport conflict that
// produced the original bug), one per transport spelling, plus an LSP server and
// a hook.
//
// Keep this in sync with walkSecretFields — a new secret-bearing field that is
// not planted here is a field this guard does not cover.
func canonicalWithSecretCanaries() source.Canonical {
	spec := func(typ string) source.MCPServerSpec {
		return source.MCPServerSpec{
			Type:    typ,
			Command: canary,
			Args:    []string{canary},
			URL:     "https://example.invalid/" + canary,
			Headers: map[string]string{"Authorization": canary},
			Env:     map[string]string{"TOKEN": canary},
			Agents:  []string{"*"},
		}
	}
	return source.Canonical{
		MCPServers: []source.MCPServer{
			// No explicit type: command+url is the documented ambiguity that
			// makes single-transport adapters emit a reduced Skip.
			{ID: "canary-ambiguous", Server: spec("")},
			{ID: "canary-stdio", Server: spec("stdio")},
			{ID: "canary-http", Server: spec("http")},
			{ID: "canary-sse", Server: spec("sse")},
		},
		LSPServers: []source.LSPServer{
			{ID: "canary-lsp", Spec: source.LSPServerSpec{
				Command: canary,
				Args:    []string{canary},
				URL:     "https://example.invalid/" + canary,
				Headers: map[string]string{"Authorization": canary},
				Env:     map[string]string{"TOKEN": canary},
				Agents:  []string{"*"},
			}},
		},
		Hooks: []source.Hook{
			{Event: "PreToolUse", Type: "command", Command: canary},
		},
	}
}
