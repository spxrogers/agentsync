package secrets

import (
	"strconv"

	"github.com/spxrogers/agentsync/internal/source"
)

// secretFieldLoc identifies where a secret-bearing string lives in the
// canonical model. A single-canonical walk (substitute / redact / unresolved
// scan) ignores it; a paired walk (ReReferenceCanonical) uses it to find the
// same field in a second canonical so a templated source value can be restored
// field-positionally rather than by value.
type secretFieldLoc struct {
	scope string // "" (user scope) | "project" (overlay)
	kind  string // "mcp" | "lsp" | "hook"
	id    string // MCP/LSP server ID, or hook event
	role  string // "command" | "url" | "arg" | "env" | "header"
	sub   string // arg index, env/header map key, or hook index; "" otherwise
}

// walkSecretFields visits EVERY secret-bearing string field of c and replaces
// each with fn's return value. This is the single authoritative enumeration of
// the secret-bearing field set in the codebase:
//
//   - MCPServers[].Server: Command, URL, Args[], Env{}, Headers{}
//   - Hooks[]:             Command
//   - LSPServers[].Spec:   Command, URL, Args[], Env{}, Headers{}
//   - Project (recursive overlay)
//
// Every secret operation — SubstituteCanonical, CollectResolved,
// UnresolvedSecretRefs, ReReferenceCanonical — is a thin caller of this walker,
// so a field added to source.MCPServerSpec / Hook / LSPServerSpec is picked up
// by all of them automatically. Add new secret-bearing fields HERE and nowhere
// else. The reflect-based guard in walk_test.go fails if a new string-shaped
// field on those structs is introduced without being classified.
//
// fn must be pure with respect to the location: callers that only read (redact,
// scan) return the value unchanged, making the assignment a no-op.
func walkSecretFields(c *source.Canonical, fn func(loc secretFieldLoc, s string) string) {
	walkSecretFieldsScoped(c, "", fn)
}

func walkSecretFieldsScoped(c *source.Canonical, scope string, fn func(loc secretFieldLoc, s string) string) {
	if c == nil {
		return
	}
	for i := range c.MCPServers {
		id := c.MCPServers[i].ID
		srv := &c.MCPServers[i].Server
		srv.Command = fn(secretFieldLoc{scope, "mcp", id, "command", ""}, srv.Command)
		srv.URL = fn(secretFieldLoc{scope, "mcp", id, "url", ""}, srv.URL)
		for j := range srv.Args {
			srv.Args[j] = fn(secretFieldLoc{scope, "mcp", id, "arg", strconv.Itoa(j)}, srv.Args[j])
		}
		for k, v := range srv.Env {
			srv.Env[k] = fn(secretFieldLoc{scope, "mcp", id, "env", k}, v)
		}
		for k, v := range srv.Headers {
			srv.Headers[k] = fn(secretFieldLoc{scope, "mcp", id, "header", k}, v)
		}
	}
	for i := range c.Hooks {
		h := &c.Hooks[i]
		h.Command = fn(secretFieldLoc{scope, "hook", h.Event, "command", strconv.Itoa(i)}, h.Command)
	}
	for i := range c.LSPServers {
		id := c.LSPServers[i].ID
		sp := &c.LSPServers[i].Spec
		sp.Command = fn(secretFieldLoc{scope, "lsp", id, "command", ""}, sp.Command)
		sp.URL = fn(secretFieldLoc{scope, "lsp", id, "url", ""}, sp.URL)
		for j := range sp.Args {
			sp.Args[j] = fn(secretFieldLoc{scope, "lsp", id, "arg", strconv.Itoa(j)}, sp.Args[j])
		}
		for k, v := range sp.Env {
			sp.Env[k] = fn(secretFieldLoc{scope, "lsp", id, "env", k}, v)
		}
		for k, v := range sp.Headers {
			sp.Headers[k] = fn(secretFieldLoc{scope, "lsp", id, "header", k}, v)
		}
	}
	if c.Project != nil {
		walkSecretFieldsScoped(c.Project, "project", fn)
	}
}
