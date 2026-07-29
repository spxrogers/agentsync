package source

// ComponentNamespaceSeparator joins a plugin id to a component name.
//
// It is a HYPHEN, and that is forced rather than chosen. Claude Code documents a
// subagent's `name` as a "Unique identifier using lowercase letters and
// hyphens", so ':' is not available — the familiar `plugin:agent` form is a
// scoped identifier Claude Code DERIVES from the plugin directory for its own
// picker and hook matchers, never a value written into a `name` field. Codex
// states no charset rule and treats `name` as the agent's identity
// (learn.chatgpt.com/docs/agent-configuration/subagents), so a hyphenated name
// is equally valid there. A ':' would also be rejected by ValidateComponentID,
// which bans it because the id becomes a filename and a colon is illegal on
// Windows.
const ComponentNamespaceSeparator = "-"

// NamespacedComponentName derives the effective name of a plugin-provided
// component: "<plugin>-<base>".
//
// # Why components are namespaced at all
//
// agentsync flattens every enabled plugin's components into one canonical model
// and renders them to the agent's native paths. Two plugins shipping a
// same-named component (the reported case: feature-dev and pr-review-toolkit
// each ship agents/code-reviewer.md) therefore rendered two files at one
// destination path, which apply refused — correctly, since silently keeping one
// is data loss. But BOTH files live under the marketplace-managed plugin cache,
// so the remedy the error named ("rename one") was one the user structurally
// could not perform. Namespacing removes the collision instead of reporting it.
//
// Claude Code reaches the same end natively, addressing a plugin's agent as
// `plugin:agent`; agentsync uses a hyphen for the reasons on
// ComponentNamespaceSeparator.
//
// # The provenance fields
//
// A namespaced component records `Plugin` (the providing plugin's filesystem
// id) and `BaseName` (the pre-namespace name). `Plugin` drives real behaviour:
// the dest→source paths refuse to capture a plugin-owned component, and the
// collision guards name the providing plugin. `BaseName` exists ONLY so those
// diagnostics can also say what the component was called upstream — it is
// never matched on, and nothing derives a path from it. Both are empty for a
// hand-authored component loaded from ~/.agentsync/, which is never renamed.
//
// A hand-authored component is never renamed, but note the converse is NOT a
// guarantee that a plugin can never occupy a name the user chose: the derived
// name is not injective. A user's own `feature-dev-code-reviewer` collides with
// what plugin `feature-dev` derives for its `code-reviewer`, and plugin `a`
// shipping `b-c` collides with plugin `a-b` shipping `c`. Those residual cases
// are caught by marketplace.checkProjectedConflicts, which reports both origins
// rather than letting one silently win.
//
// INVARIANT: when Plugin is non-empty, Name == NamespacedComponentName(Plugin,
// BaseName). Everything downstream reads Name as the effective identity while
// diagnostics read the other two, so a component whose three fields disagree
// would report an origin that does not match what it rendered. Pinned by
// TestProvenanceInvariant (internal/marketplace).
//
// Neither field is ever serialized. Skills, subagents, and commands are
// FILE-backed components whose canonical form is a file on disk, and a projected
// component is never written back into the canonical source, so provenance is
// derived state that lives only for the duration of a load.
//
// Both are also outside the secret machinery by construction: these are text
// components walkSecretFields deliberately never visits (internal/secrets/walk.go),
// so neither can carry a ${secret:…} reference or a resolved value.
//
// An empty plugin returns base unchanged, so callers need no branch.
func NamespacedComponentName(plugin, base string) string {
	if plugin == "" {
		return base
	}
	return plugin + ComponentNamespaceSeparator + base
}

// Namespacing performs NO validation of the derived name, deliberately. An
// earlier revision did, and it was a regression: the check used a stricter rune
// set than the projection's own validateProjectedName, and loadProjected
// propagates a projection error regardless of `lenient`, so it took down the
// read-only commands whose whole design is to degrade and show state. The name
// safety lives downstream at the single dispatch waist, where render.Plan runs
// ValidateComponentID over every component id before any of them is joined into
// a destination path. See the "Amendments after review" section of
// docs/superpowers/specs/2026-07-28-plugin-component-namespacing-design.md.
