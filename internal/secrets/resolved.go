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
// the render-only egress: the adapter Render entry points consume it to project
// the resolved model into destination FileOps.
//
// It returns a writable source.Canonical, so this accessor is the one seam that
// could otherwise launder resolved cleartext back toward source. The type wall
// makes passing a Resolved DIRECTLY to source.Write* / capture.Capture a compile
// error; this accessor is additionally fenced by a forbidigo rule
// (.golangci.yml) that forbids secrets.Resolved.Canonical outside the adapter
// Render files, so non-render code can't unwrap-then-write. The dest->source
// direction goes through ReReferenceCanonical + capture.Capture on a templated
// source.Canonical, never through here.
//
// ACCEPTED RESIDUAL (documented, intentional): the forbidigo fence is a static
// matcher, so interface dispatch, struct embedding, and reflection can defeat
// it; and source.Write* is itself not fenced. So a DELIBERATE two-step
// laundering (defeat the fence to get a writable source.Canonical, then call a
// source writer that skips re-referencing) remains possible. No innocent
// mistake produces this, and capture.Capture always re-references, so every real
// import/reconcile flow is safe. Fencing the whole source.Write* API was
// declined (it fights the legitimate write path and is bypassable one level
// down). If you find yourself unwrapping a Resolved outside an adapter Render,
// stop — you almost certainly want capture.Capture instead.
func (r Resolved) Canonical() source.Canonical { return r.c }

// ForAgent returns a Resolved narrowed to what the named agent should render:
// every component provided by a plugin whose `agents` allowlist excludes this
// agent, or whose `native_agents` deferral list names it, is dropped.
// Hand-authored components (no Plugin stamp) always survive.
//
// It is the render-side entry to source.FilterForAgent, which holds the actual
// rule (import's capture-refusal filter is the other caller — the two must agree
// exactly). This wrapper exists for a fence reason and a design reason:
//
//   - Fence: render.Plan cannot reach the underlying source.Canonical (the
//     secrets.Resolved.Canonical forbidigo rule confines that to adapter Render),
//     so the narrowing has to be a method on the wrapper. It reads the private
//     canonical directly and returns another Resolved — no writable model is
//     ever exposed, so this adds no laundering seam.
//   - Design: doing it once at the dispatch waist means no adapter can forget.
//     An allowlist honoured in ten adapters × six component kinds is sixty
//     chances to silently render the duplicate the allowlist exists to prevent.
//
// It never touches secret material — targeting is a pure include/exclude over
// already-resolved components — so a narrowed Resolved carries exactly the
// substitutions its parent did.
//
// The result SHARES component values with the receiver (the filter rebuilds
// slices, it does not deep-copy). That matches what Render already gets today:
// adapters read the model and build their own output, they never mutate it.
func (r Resolved) ForAgent(agent string) Resolved {
	return Resolved{c: source.FilterForAgent(r.c, agent)}
}

// ComponentID is a (kind, name) pair for a component id an adapter joins into a
// destination path. For a TEXT component (subagent/command/skill) the Name is the
// canonical Name that becomes a destination filename stem at Render. For an MCP or
// LSP server the Name is the server id: a filename stem only for adapters that
// write one file per server (continuedev — filepath.Join(MCPDir, id+".yaml")), and
// a JSON key in a shared file for the rest — but a traversal id is never a
// legitimate server id in either shape, so all kinds are validated the same way.
// Kind is the label source.ValidateComponentID expects ("subagent", "command",
// "skill", "mcp", "lsp").
type ComponentID struct {
	Kind string
	Name string
}

// ComponentIDs returns every component id reachable in the resolved model that an
// adapter joins into a destination FileOp path — subagent, command, and skill
// names plus MCP and LSP server ids, INCLUDING the project overlay — so a caller
// (render.Plan) can validate each against source.ValidateComponentID before any
// adapter joins it into a path. A Name/id like "../../../tmp/x" would otherwise
// render a path that escapes the agent's config dir.
//
// It is deliberately a STRING-only accessor: it reads the id strings off the
// private canonical directly (never through the fenced Canonical() egress) and
// never returns the writable source.Canonical. So it neither trips the
// secrets.Resolved.Canonical forbidigo fence nor gives the caller a seam to
// launder resolved cleartext back toward source — the dispatch-layer guard gets
// exactly the names it must validate and nothing more.
func (r Resolved) ComponentIDs() []ComponentID {
	return componentIDs(r.c)
}

func componentIDs(c source.Canonical) []ComponentID {
	var out []ComponentID
	for _, s := range c.Subagents {
		out = append(out, ComponentID{Kind: "subagent", Name: s.Name})
	}
	for _, cmd := range c.Commands {
		out = append(out, ComponentID{Kind: "command", Name: cmd.Name})
	}
	for _, sk := range c.Skills {
		out = append(out, ComponentID{Kind: "skill", Name: sk.Name})
	}
	// MCP/LSP server ids are joined into a destination FILENAME by adapters that
	// write one file per server (continuedev: filepath.Join(MCPDir, id+".yaml")).
	// A marketplace-projected id is a raw manifest map key with no traversal check
	// of its own, so a plugin declaring an mcpServers key like "../../../etc/x"
	// would otherwise render a write-anywhere FileOp — validate it here like every
	// other id. (For adapters where the id is a JSON key in a shared file rather
	// than a filename, rejecting a traversal id is still correct — it is never a
	// legitimate server id.)
	for _, m := range c.MCPServers {
		out = append(out, ComponentID{Kind: "mcp", Name: m.ID})
	}
	for _, l := range c.LSPServers {
		out = append(out, ComponentID{Kind: "lsp", Name: l.ID})
	}
	// Recurse into the project overlay as forward-looking defense-in-depth — NOT
	// the primary guard. In the production flow this is belt-and-suspenders:
	// project.Merge (internal/project, via overlayByKey) already mirrors every
	// project component id into the TOP-LEVEL merged slices above, so render.Plan —
	// the sole caller — validates each project id through the top-level loop
	// regardless, and dropping this recursion would not open a real gap. It costs
	// only duplicate (already-validated) ids today; its value is guarding a would-be
	// caller that ever passed an UNMERGED project-only model. User-scope models have
	// a nil Project (no overlay loaded), so this is a no-op there — it never rejects
	// a user-scope apply for a name only the project overlay would render.
	if c.Project != nil {
		out = append(out, componentIDs(*c.Project)...)
	}
	return out
}

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
		m.Server.Extra = cloneAnyMap(m.Server.Extra)
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
		l.Spec.Extra = cloneAnyMap(l.Spec.Extra)
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

// cloneAnyMap detaches the top level of a passthrough Extra map so the resolved
// copy does not alias the caller's templated source. Resolve never mutates Extra
// (it is not secret-walked), so a shallow copy is sufficient to keep the two
// models independent.
func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
