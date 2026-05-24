package secrets

import "github.com/spxrogers/agentsync/internal/source"

// Resolved is a canonical model prepared for rendering to an agent destination.
// It is a DISTINCT type — not assignable to source.Canonical and carrying no
// exported field of that type — so the compiler forbids handing it to the
// dest->source write path (capture.Capture and source.Write* accept only the
// templated source.Canonical / its sub-structs). That makes the recurring leak
// — resolved cleartext persisted back into ~/.agentsync — a compile error
// rather than a code-review catch.
//
// Obtain one of two ways, both honest about what they contain:
//
//   - SubstituteCanonical: the apply/update path. Secrets are resolved to
//     cleartext; the result MUST only flow forward (Render -> destination).
//   - ForRender: the diff/status/reconcile/explain/import path. The canonical
//     is wrapped as-is (still templated, or ingested-from-dest) because those
//     callers render only to hash, preview, or enumerate — never to write real
//     config — so resolution is irrelevant and must not be forced (it would
//     fail when the secrets backend is locked).
//
// The single resolved->templated converter is ReReferenceCanonical; there is no
// method that turns a Resolved back into a writable source.Canonical.
type Resolved struct {
	c source.Canonical
}

// ForRender wraps a templated (or ingested-from-destination) canonical as a
// Resolved for rendering, WITHOUT resolving any ${secret:…}. Use it from paths
// that render only to compute hashes / previews / owned pointers.
func ForRender(c source.Canonical) Resolved { return Resolved{c: c} }

// Canonical returns the underlying model for the render layer to read. It is
// the render-only egress: adapters consume it to produce destination FileOps.
// It deliberately does not help the reverse direction — callers that persist to
// source take a source.Canonical directly and so cannot be fed a Resolved.
func (r Resolved) Canonical() source.Canonical { return r.c }

// cloneForResolve copies c so SubstituteCanonical can resolve secrets into the
// copy while the caller's source.Canonical stays templated. Only the containers
// the secret walk mutates (Args/Env/Headers slices+maps, the MCP/LSP/Hook
// slices, and the Project overlay) are cloned; everything else is shared, since
// substitution never touches it.
func cloneForResolve(c source.Canonical) source.Canonical {
	out := c
	out.MCPServers = cloneMCPServers(c.MCPServers)
	out.LSPServers = cloneLSPServers(c.LSPServers)
	out.Hooks = append([]source.Hook(nil), c.Hooks...)
	if c.Project != nil {
		p := cloneForResolve(*c.Project)
		out.Project = &p
	}
	return out
}

func cloneMCPServers(in []source.MCPServer) []source.MCPServer {
	if in == nil {
		return nil
	}
	out := make([]source.MCPServer, len(in))
	for i, m := range in {
		m.Server.Args = append([]string(nil), m.Server.Args...)
		m.Server.Env = cloneStrMap(m.Server.Env)
		m.Server.Headers = cloneStrMap(m.Server.Headers)
		out[i] = m
	}
	return out
}

func cloneLSPServers(in []source.LSPServer) []source.LSPServer {
	if in == nil {
		return nil
	}
	out := make([]source.LSPServer, len(in))
	for i, l := range in {
		l.Spec.Args = append([]string(nil), l.Spec.Args...)
		l.Spec.Env = cloneStrMap(l.Spec.Env)
		l.Spec.Headers = cloneStrMap(l.Spec.Headers)
		out[i] = l
	}
	return out
}

func cloneStrMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
