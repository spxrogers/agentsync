package cursor

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// cursorHooksVersion is the integer `version` Cursor's documented hooks.json
// schema requires at the top level of `.cursor/hooks.json` (see
// cursor.com/docs/agent/hooks; the documented value is 1). It is asserted
// post-merge in applyWrite when the merged file lacks one — never rendered into
// op.Content, and never overwriting a user-set value — see the note there.
const cursorHooksVersion = 1

// hooksFileName is the basename applyWrite uses to detect the hooks destination
// (vs mcp.json) so it knows to inject the required `version` field.
const hooksFileName = "hooks.json"

// canonicalToCursorHookEvent maps agentsync's canonical (Claude-shaped,
// PascalCase) lifecycle events to Cursor's camelCase hook event names (per
// cursor.com/docs/hooks). A canonical event with no entry here (e.g.
// Notification, PostCompact, PermissionRequest) has no Cursor target and is
// dropped with a reported Skip.
var canonicalToCursorHookEvent = map[string]string{
	"PreToolUse":       "preToolUse",
	"PostToolUse":      "postToolUse",
	"UserPromptSubmit": "beforeSubmitPrompt",
	"SessionStart":     "sessionStart",
	"SessionEnd":       "sessionEnd",
	"Stop":             "stop",
	"SubagentStart":    "subagentStart",
	"SubagentStop":     "subagentStop",
	"PreCompact":       "preCompact",
}

// cursorToCanonicalHookEvent is the inverse map, used by Ingest. A Cursor-native
// event with no canonical equivalent (afterFileEdit, beforeShellExecution, …) is
// not captured: agentsync only round-trips events it can also render.
var cursorToCanonicalHookEvent = invertHookEvents(canonicalToCursorHookEvent)

func invertHookEvents(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

// renderHooks writes a single merge-json-keys op for `.cursor/hooks.json`'s
// `hooks` object. Cursor's hook schema is FLAT — each event maps to an array of
// `{ command, matcher?, type? }` entries — unlike Claude's nested
// matcher-group/hooks-array shape, so the canonical Hook (Event, Matcher, Type,
// Command) is translated, not copied. op.Content carries `{"hooks": {…}}` only:
// the required top-level `version` is injected post-merge in applyWrite (never
// here) so it is never recorded as an owned key and stripped by orphan-cleanup.
// Per-event ownership mirrors Claude/Codex: agentsync owns the whole array under
// each event key it renders; foreign event keys the user authored are untouched.
func (a *Adapter) renderHooks(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if len(c.Hooks) == 0 {
		return nil, nil, nil
	}
	byEvent := map[string][]map[string]any{}
	var skips []adapter.Skip
	for _, h := range c.Hooks {
		ce, ok := canonicalToCursorHookEvent[h.Event]
		if !ok {
			skips = append(skips, adapter.Skip{
				Component: "hook",
				Name:      h.Event,
				Reason:    "Cursor has no equivalent hook event",
				Kind:      adapter.SkipDropped,
			})
			continue
		}
		// agentsync models only command hooks. A canonical hook with another
		// type (e.g. a Cursor prompt hook captured before this guard existed)
		// would render as a half-formed entry Cursor can't run — skip it with a
		// report instead of emitting it.
		if h.Type != "" && h.Type != "command" {
			skips = append(skips, adapter.Skip{
				Component: "hook",
				Name:      h.Event,
				Reason:    fmt.Sprintf("agentsync models only command hooks; type %q is not projected", h.Type),
				Kind:      adapter.SkipDropped,
			})
			continue
		}
		entry := map[string]any{"command": h.Command}
		if h.Matcher != "" {
			entry["matcher"] = h.Matcher
		}
		byEvent[ce] = append(byEvent[ce], entry)
	}
	if len(byEvent) == 0 {
		return nil, skips, nil
	}
	// OwnedKeys here is effective only when Apply is driven without the render
	// pipeline (e.g. the adapter's own tests); render.Plan overwrites it from
	// state, scoped to this op's sections. Same parity as claude/hook.go.
	var ownedKeys []string
	for event := range byEvent {
		ownedKeys = append(ownedKeys, "/hooks/"+event)
	}
	obj := map[string]any{"hooks": byEvent}
	body, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal hooks: %w", err)
	}
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.Hooks,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "hooks/* (multiple)",
		MergeStrategy: "merge-json-keys",
		OwnedKeys:     ownedKeys,
	}}, skips, nil
}

// cursorHookEntryModeledKeys are the per-entry hooks.json fields the canonical
// source.Hook can represent. Cursor's documented entry schema is wider (timeout,
// failClosed, loop_limit, and prompt/model for prompt-type hooks); an entry
// carrying any unmodeled field cannot round-trip, and capturing its modeled
// subset would let the next apply — which owns the whole per-event array —
// rewrite the user's native entry without those fields. ingestHooks therefore
// refuses to capture the ENTIRE event when any of its entries is unrepresentable,
// so apply never takes ownership of an array it would lossily rewrite.
var cursorHookEntryModeledKeys = map[string]bool{"command": true, "matcher": true, "type": true}

// ingestHooks decodes `.cursor/hooks.json`'s `hooks` object (the value of the
// top-level "hooks" key) into canonical hooks, warning on anything it cannot
// capture. Inverse of renderHooks: each Cursor camelCase event is mapped back to
// its canonical PascalCase name, and each flat `{command, matcher, type}` entry
// becomes a source.Hook with a missing type defaulting to "command". Two fail-safe
// skips, both warned: a Cursor-native event with no canonical equivalent
// (afterFileEdit, beforeShellExecution, …), and an event containing an entry the
// canonical model cannot represent (non-command type or an unmodeled field) — see
// cursorHookEntryModeledKeys.
func ingestHooks(raw any, warn io.Writer) []source.Hook {
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	var out []source.Hook
	for cursorEvent, rawEntries := range hooks {
		canonEvent, ok := cursorToCanonicalHookEvent[cursorEvent]
		if !ok {
			fmt.Fprintf(warn, "warning: hook event %q has no canonical equivalent; not captured\n", cursorEvent)
			continue
		}
		entries, ok := rawEntries.([]any)
		if !ok {
			continue
		}
		var captured []source.Hook
		representable := true
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				representable = false
				break
			}
			typ := asStr(entry["type"])
			if typ == "" {
				typ = "command"
			}
			if typ != "command" {
				fmt.Fprintf(warn, "warning: hook event %q has a %q-type entry agentsync cannot represent; event not captured\n", cursorEvent, typ)
				representable = false
				break
			}
			if extra := unmodeledKeys(entry, cursorHookEntryModeledKeys); len(extra) > 0 {
				fmt.Fprintf(warn, "warning: hook event %q has an entry with unmodeled fields (%s); event not captured\n", cursorEvent, strings.Join(extra, ", "))
				representable = false
				break
			}
			captured = append(captured, source.Hook{
				Event:   canonEvent,
				Matcher: asStr(entry["matcher"]),
				Type:    typ,
				Command: asStr(entry["command"]),
			})
		}
		if representable {
			out = append(out, captured...)
		}
	}
	return out
}

// unmodeledKeys returns the sorted keys of entry that are not in modeled.
func unmodeledKeys(entry map[string]any, modeled map[string]bool) []string {
	var out []string
	for k := range entry {
		if !modeled[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
