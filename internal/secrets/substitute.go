package secrets

import (
	"fmt"
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// SubstituteCanonical walks all string-valued fields of the canonical model
// that may contain ${secret:...} or ${env:...} references and resolves them
// in-place. It uses sec as the secrets backend and env as the environment
// resolver.
//
// If any reference cannot be resolved, it returns an error listing all
// unresolved markers so the user knows exactly which secret is missing.
// Apply is blocked — never silent about missing secrets.
func SubstituteCanonical(c *source.Canonical, sec Resolver, env Resolver) error {
	var allUnresolved []string

	// MCP servers: Command, Args, URL, Headers, Env
	for i := range c.MCPServers {
		srv := &c.MCPServers[i].Server
		if v, u, err := SubstituteRefs(srv.Command, sec, env); err == nil {
			srv.Command = v
			allUnresolved = append(allUnresolved, u...)
		}
		if v, u, err := SubstituteRefs(srv.URL, sec, env); err == nil {
			srv.URL = v
			allUnresolved = append(allUnresolved, u...)
		}
		for j, a := range srv.Args {
			if v, u, err := SubstituteRefs(a, sec, env); err == nil {
				srv.Args[j] = v
				allUnresolved = append(allUnresolved, u...)
			}
		}
		for k, val := range srv.Env {
			if v, u, err := SubstituteRefs(val, sec, env); err == nil {
				srv.Env[k] = v
				allUnresolved = append(allUnresolved, u...)
			}
		}
		for k, val := range srv.Headers {
			if v, u, err := SubstituteRefs(val, sec, env); err == nil {
				srv.Headers[k] = v
				allUnresolved = append(allUnresolved, u...)
			}
		}
	}

	// Hooks: Command
	for i := range c.Hooks {
		if v, u, err := SubstituteRefs(c.Hooks[i].Command, sec, env); err == nil {
			c.Hooks[i].Command = v
			allUnresolved = append(allUnresolved, u...)
		}
	}

	// LSP servers: Command, URL, Args, Env, Headers
	for i := range c.LSPServers {
		sp := &c.LSPServers[i].Spec
		if v, u, err := SubstituteRefs(sp.Command, sec, env); err == nil {
			sp.Command = v
			allUnresolved = append(allUnresolved, u...)
		}
		if v, u, err := SubstituteRefs(sp.URL, sec, env); err == nil {
			sp.URL = v
			allUnresolved = append(allUnresolved, u...)
		}
		for j, a := range sp.Args {
			if v, u, err := SubstituteRefs(a, sec, env); err == nil {
				sp.Args[j] = v
				allUnresolved = append(allUnresolved, u...)
			}
		}
		for k, val := range sp.Env {
			if v, u, err := SubstituteRefs(val, sec, env); err == nil {
				sp.Env[k] = v
				allUnresolved = append(allUnresolved, u...)
			}
		}
		for k, val := range sp.Headers {
			if v, u, err := SubstituteRefs(val, sec, env); err == nil {
				sp.Headers[k] = v
				allUnresolved = append(allUnresolved, u...)
			}
		}
	}

	// Also substitute in project overlay if present.
	if c.Project != nil {
		if err := SubstituteCanonical(c.Project, sec, env); err != nil {
			return err
		}
	}

	if len(allUnresolved) > 0 {
		return fmt.Errorf("unresolved secret references: %s", strings.Join(allUnresolved, ", "))
	}
	return nil
}
