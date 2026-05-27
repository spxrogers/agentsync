package claude

// ExtraNativeKeys returns the entries of a decoded native config object whose
// keys are NOT in modeled — the fields agentsync does not represent in its
// canonical model. Adapters capture these verbatim into source.*Spec.Extra so
// the dest→source round-trip (import/reconcile) is not lossy for native fields
// agentsync doesn't understand (e.g. an MCP server's timeout/disabled/cwd).
// Returns nil when there are none, so an empty Extra omits the canonical
// [server.extra] table entirely.
func ExtraNativeKeys(raw map[string]any, modeled ...string) map[string]any {
	skip := make(map[string]bool, len(modeled))
	for _, k := range modeled {
		skip[k] = true
	}
	var extra map[string]any
	for k, v := range raw {
		if skip[k] {
			continue
		}
		if extra == nil {
			extra = map[string]any{}
		}
		extra[k] = v
	}
	return extra
}

// MergeExtra projects passthrough native fields back into a rendered destination
// object. A modeled key the renderer already set always wins — Extra never
// clobbers a field agentsync owns — so a value that was promoted into the model
// can't be shadowed by a stale Extra copy.
func MergeExtra(spec, extra map[string]any) {
	for k, v := range extra {
		if _, exists := spec[k]; exists {
			continue
		}
		spec[k] = v
	}
}
