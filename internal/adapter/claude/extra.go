package claude

import "strings"

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
		// Reserved agentsync namespace, symmetric with MergeExtra. A "__"-prefixed
		// key is agentsync-INTERNAL round-trip metadata (continuedev's
		// __block_version/__block_schema), never a verbatim native field. MergeExtra
		// refuses to RENDER such a key; capture refuses to INGEST one, so the "__"
		// namespace stays fully agentsync-owned on both sides. Without this, a stray
		// native "__"-prefixed key would be captured into the shared Extra yet dropped
		// on the next render — a silent lossy round-trip. continuedev stores its block
		// header via applyBlockHeader (block level), not through this helper (the inner
		// server map, which never carries a "__" key), so it is unaffected. No known
		// harness uses a "__"-prefixed native config key; this carve-out is deliberate.
		if strings.HasPrefix(k, "__") {
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
		// Skip agentsync-reserved keys. A "__"-prefixed Extra key is INTERNAL
		// round-trip metadata (e.g. continuedev's __block_version/__block_schema
		// block header), not a verbatim native field — it must never be rendered
		// into a destination. Because Extra is a SHARED canonical field, an
		// unfiltered copy here would leak one adapter's synthetic keys into every
		// OTHER agent's native config (codex config.toml, .claude.json, …) and
		// re-ingest would then recapture them as if native. The owning adapter reads
		// its own reserved keys directly from Extra, never via MergeExtra.
		if strings.HasPrefix(k, "__") {
			continue
		}
		if _, exists := spec[k]; exists {
			continue
		}
		spec[k] = v
	}
}
