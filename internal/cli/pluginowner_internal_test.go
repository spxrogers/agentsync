package cli

import "testing"

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

		// Continue renders one file per server, so its op is a whole-file replace
		// with a per-server SourceID rather than a section-wide one.
		{"continue per-server SourceID", "mcp/github.toml", "/mcpServers/github", "gh-plugin"},

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
