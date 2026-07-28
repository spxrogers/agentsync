package source

import "fmt"

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
// id) and `BaseName` (the pre-namespace name) so a report, `plugin explain`, or
// a collision error can say where it came from and what it was called upstream.
// Both are empty for a hand-authored component loaded from ~/.agentsync/, which
// is NEVER renamed — a plugin can therefore never take a name the user chose.
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

// ValidateNamespacedComponentName checks that a derived name is writable and
// safe to display, reusing the SAME rules ValidateComponentID enforces at the
// canonical write boundary (no path separator, no "..", no ':', no control or
// deceptive formatting rune). A plugin id originates from a marketplace, so it
// is outside agentsync's trust boundary: without this, a hostile id could derive
// a component name that escapes its destination directory or smuggles a
// terminal escape into a diagnostic. The error names both halves so the user can
// tell which one is at fault.
func ValidateNamespacedComponentName(kind, plugin, base string) error {
	name := NamespacedComponentName(plugin, base)
	if err := ValidateComponentID(kind, name); err != nil {
		return fmt.Errorf("plugin %q provides %s %q: %w", plugin, kind, base, err)
	}
	return nil
}
