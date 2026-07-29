package cli

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestPluginOwnerForKeyItem is the direct unit test for the key-item provenance
// lookup. The e2e import tests exercise a different code path (import matches by
// id, never by pointer), so without this the function's whole contract — kind
// from SourceID, id from the pointer, RFC 6901 decoding — was unwitnessed.
func TestPluginOwnerForKeyItem(t *testing.T) {
	owners := map[string]string{
		"mcp/github":     "gh-plugin",
		"mcp/with/slash": "slash-plugin",
		"lsp/gopls":      "lsp-plugin",
		// A plugin LSP server whose id collides with a common MCP name. This is
		// the entry that must NOT be reachable from an /mcpServers/ pointer.
		"lsp/postgres": "lsp-plugin",
	}

	tests := []struct {
		name     string
		sourceID string
		ptr      string
		want     string
	}{
		// Every MCP root key in the tree resolves the same way, because the kind
		// comes from the SourceID rather than the root key. The generic tier's
		// root keys are DATA that grows with every agent added, so a hand-listed
		// allowlist would silently drop the refusal for each new one.
		{"claude/cursor/gemini shape", "mcp/* (multiple)", "/mcpServers/github", "gh-plugin"},
		{"opencode shape", "mcp/* (multiple)", "/mcp/github", "gh-plugin"},
		{"codex shape", "mcp/* (multiple)", "/mcp_servers/github", "gh-plugin"},
		{"generic tier: zed", "mcp/* (multiple)", "/context_servers/github", "gh-plugin"},
		{"generic tier: vscode", "mcp/* (multiple)", "/servers/github", "gh-plugin"},
		{"generic tier: amp (dotted root)", "mcp/* (multiple)", "/amp.mcpServers/github", "gh-plugin"},

		// NOTE: Continue's per-server SourceID ("mcp/<id>.toml") is deliberately
		// NOT exercised here. Its op is MergeStrategy "replace", so it is a
		// WHOLE-FILE item that never reaches this function — collectItems looks
		// it up by SourceID instead. Asserting it here would test a shape that
		// cannot occur; the real path is covered by
		// TestPluginProvidedSourceIDs_RegistersBothServerKeyForms and the
		// Continue e2e in plugin_namespace_test.go.

		{"lsp kind", "lsp/* (multiple)", "/lspServers/gopls", "lsp-plugin"},

		// THE REGRESSION: a plugin LSP server named like a common MCP server must
		// never be reported as the owner of an MCP pointer. Probing the id
		// against both kinds would refuse write-back of the user's own MCP server
		// and blame a plugin that does not own it.
		{"lsp id must not leak into an mcp pointer", "mcp/* (multiple)", "/mcpServers/postgres", ""},

		// Hooks carry no per-entry provenance for this lookup (hook write-back is
		// not implemented), so they resolve to "" explicitly rather than falling
		// through to a server probe.
		{"hooks resolve to no owner", "hooks/* (multiple)", "/hooks/PreToolUse", ""},
		{"hook event named like a server", "hooks/* (multiple)", "/hooks/github", ""},

		// RFC 6901: ~1 is '/', ~0 is '~'. An id containing '/' would otherwise
		// never match its map key.
		{"escaped slash in id", "mcp/* (multiple)", "/mcpServers/with~1slash", "slash-plugin"},

		{"hand-declared server", "mcp/* (multiple)", "/mcpServers/mine", ""},
		{"pointer with no id segment", "mcp/* (multiple)", "/mcpServers", ""},
		{"empty pointer", "mcp/* (multiple)", "", ""},
		{"unknown kind", "memory/AGENTS.md", "/anything/github", ""},
		{"deeper pointer still keys on the id", "mcp/* (multiple)", "/mcpServers/github/env/TOKEN", "gh-plugin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pluginOwnerForKeyItem(tc.sourceID, tc.ptr, owners); got != tc.want {
				t.Errorf("pluginOwnerForKeyItem(%q, %q) = %q, want %q", tc.sourceID, tc.ptr, got, tc.want)
			}
		})
	}
}

// TestUnescapeJSONPointer pins the decode order: ~0 must be decoded LAST, or
// "~01" would wrongly become "/" instead of the literal "~1".
func TestUnescapeJSONPointer(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", "plain"},
		{"with~1slash", "with/slash"},
		{"with~0tilde", "with~tilde"},
		{"~01", "~1"},
		{"~1~0", "/~"},
		{"", ""},
	} {
		if got := unescapeJSONPointer(tc.in); got != tc.want {
			t.Errorf("unescapeJSONPointer(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPluginProvidedSourceIDs_RegistersBothServerKeyForms pins the dual keying
// that the key-item table above deliberately does not cover.
//
// Most adapters fold every MCP server into one config file, so their op carries
// the section-wide SourceID "mcp/* (multiple)" and only the JSON pointer names
// the server — that is the "<kind>/<id>" key. Continue instead renders ONE FILE
// PER SERVER with the per-server SourceID "mcp/<id>.toml", making it a whole-file
// item that collectItems looks up by SourceID directly. Keying only the bare form
// left that path unguarded: a plugin-provided server on Continue could still be
// captured.
func TestPluginProvidedSourceIDs_RegistersBothServerKeyForms(t *testing.T) {
	c := source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "pluginapi", Plugin: "srv"},
			{ID: "mine"}, // hand-declared: must not appear under either form
		},
		LSPServers: []source.LSPServer{{ID: "gopls", Plugin: "srv"}},
	}
	got := pluginProvidedSourceIDs(c)

	for _, key := range []string{"mcp/pluginapi", "mcp/pluginapi.toml", "lsp/gopls", "lsp/gopls.toml"} {
		if got[key] != "srv" {
			t.Errorf("key %q = %q, want %q — both the key-merge and whole-file "+
				"lookup shapes must resolve a plugin-owned server", key, got[key], "srv")
		}
	}
	for _, key := range []string{"mcp/mine", "mcp/mine.toml"} {
		if _, present := got[key]; present {
			t.Errorf("a hand-declared server must not be registered as plugin-owned; got key %q", key)
		}
	}
}
