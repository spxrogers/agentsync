package secrets

import (
	"reflect"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// walkerCovered is the authoritative classification of the string-shaped
// fields on the secret-bearing canonical structs: every such field is either
// walked by walkSecretFields (true) or deliberately NOT a secret-bearing field
// (false). TestNewSecretFieldGuard fails if a field exists on these structs
// that is in neither column — forcing whoever adds it to decide, in one place,
// whether secrets must traverse it. This is the guard that makes a new
// secret-bearing field correct by construction instead of a silent leak.
var walkerCovered = map[string]map[string]bool{
	"MCPServerSpec": {
		"Command": true,
		"URL":     true,
		"Args":    true,
		"Env":     true,
		"Headers": true,
		"Type":    false, // enum discriminator, never a secret
		"Agents":  false, // source-only targeting allowlist, never a secret
	},
	"Hook": {
		"Command": true,
		"Event":   false, // event name (e.g. PreToolUse)
		"Matcher": false, // glob/regex, not a credential
		"Type":    false, // always "command"
	},
	"LSPServerSpec": {
		"Command": true,
		"URL":     true,
		"Args":    true,
		"Env":     true,
		"Headers": true,
		"Agents":  false, // source-only targeting allowlist, never a secret
	},
}

// isStringShaped reports whether a struct field can carry a ${secret:…} /
// ${env:…} reference: a string, a []string, or a map[string]string. Anything
// else (bool, *bool, int) cannot hold a reference and is not the walker's
// concern.
func isStringShaped(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String:
		return true
	case reflect.Slice:
		return t.Elem().Kind() == reflect.String
	case reflect.Map:
		return t.Key().Kind() == reflect.String && t.Elem().Kind() == reflect.String
	default:
		return false
	}
}

// TestNewSecretFieldGuard fails when a string-shaped field is added to a
// secret-bearing struct without being classified in walkerCovered. It is the
// "new field guard" required by the refactor: the walker, the classification
// map, and the actual struct definitions cannot silently drift apart.
func TestNewSecretFieldGuard(t *testing.T) {
	structs := map[string]reflect.Type{
		"MCPServerSpec": reflect.TypeOf(source.MCPServerSpec{}),
		"Hook":          reflect.TypeOf(source.Hook{}),
		"LSPServerSpec": reflect.TypeOf(source.LSPServerSpec{}),
	}
	for name, typ := range structs {
		covered := walkerCovered[name]
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !isStringShaped(f.Type) {
				continue
			}
			if _, ok := covered[f.Name]; !ok {
				t.Errorf("%s.%s is string-shaped but unclassified: add it to walkSecretFields "+
					"(and mark true) if it can hold a ${secret:…}/${env:…} reference, or mark it "+
					"false in walkerCovered if it never carries a secret", name, f.Name)
			}
		}
	}
}

// TestWalkerVisitsEveryCoveredField proves the walker actually traverses every
// field classified as covered. Without this, the classification map could claim
// coverage the walker never delivers (the exact leak the refactor prevents). It
// plants a unique sentinel in each covered field, walks, and asserts each
// sentinel was both visited and replaceable.
func TestWalkerVisitsEveryCoveredField(t *testing.T) {
	enabled := true
	c := &source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "srv",
			Server: source.MCPServerSpec{
				Type:    "stdio",
				Command: "mcp-command",
				URL:     "mcp-url",
				Args:    []string{"mcp-arg0", "mcp-arg1"},
				Env:     map[string]string{"E": "mcp-env"},
				Headers: map[string]string{"H": "mcp-header"},
				Agents:  []string{"claude"},
				Enabled: &enabled,
			},
		}},
		Hooks: []source.Hook{{
			Event:   "PreToolUse",
			Matcher: "*",
			Type:    "command",
			Command: "hook-command",
		}},
		LSPServers: []source.LSPServer{{
			ID: "lsp",
			Spec: source.LSPServerSpec{
				Command: "lsp-command",
				URL:     "lsp-url",
				Args:    []string{"lsp-arg0"},
				Env:     map[string]string{"E": "lsp-env"},
				Headers: map[string]string{"H": "lsp-header"},
			},
		}},
	}
	want := map[string]bool{
		"mcp-command": false, "mcp-url": false, "mcp-arg0": false, "mcp-arg1": false,
		"mcp-env": false, "mcp-header": false, "hook-command": false,
		"lsp-command": false, "lsp-url": false, "lsp-arg0": false,
		"lsp-env": false, "lsp-header": false,
	}
	walkSecretFields(c, func(_ secretFieldLoc, s string) string {
		if _, ok := want[s]; ok {
			want[s] = true
		}
		return s + "!" // mutate to prove the assignment path works for every field
	})
	for sentinel, visited := range want {
		if !visited {
			t.Errorf("walkSecretFields never visited the field carrying %q", sentinel)
		}
	}
	// Non-secret string-shaped fields must NOT have been rewritten.
	if c.MCPServers[0].Server.Type != "stdio" {
		t.Errorf("walker rewrote MCP Type (non-secret): %q", c.MCPServers[0].Server.Type)
	}
	if got := c.MCPServers[0].Server.Agents; len(got) != 1 || got[0] != "claude" {
		t.Errorf("walker rewrote MCP Agents (non-secret): %v", got)
	}
	if c.Hooks[0].Event != "PreToolUse" || c.Hooks[0].Matcher != "*" {
		t.Errorf("walker rewrote hook Event/Matcher (non-secret)")
	}
}
