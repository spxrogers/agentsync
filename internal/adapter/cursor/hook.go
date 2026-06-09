package cursor

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// cursorHooksVersion is the integer `version` Cursor requires at the top level of
// `.cursor/hooks.json`. Cursor 3.x refuses to load ANY hooks from a file missing
// it (see cursor.com/docs/hooks). It is (re)asserted post-merge in applyWrite,
// never rendered into op.Content — see the note there.
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
			})
			continue
		}
		entry := map[string]any{"command": h.Command}
		if h.Matcher != "" {
			entry["matcher"] = h.Matcher
		}
		// Cursor's hook type defaults to "command" (the only kind agentsync
		// models). Emit `type` only when it diverges from that default so the
		// rendered file stays idiomatic and the round-trip is clean (Ingest
		// defaults a missing type back to "command").
		if h.Type != "" && h.Type != "command" {
			entry["type"] = h.Type
		}
		byEvent[ce] = append(byEvent[ce], entry)
	}
	if len(byEvent) == 0 {
		return nil, skips, nil
	}
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

// ingestHooks decodes `.cursor/hooks.json`'s `hooks` object (the value of the
// top-level "hooks" key) into canonical hooks. Inverse of renderHooks: each
// Cursor camelCase event is mapped back to its canonical PascalCase name (events
// with no canonical equivalent are skipped), and each flat `{command, matcher,
// type}` entry becomes a source.Hook with a missing type defaulting to "command".
func ingestHooks(raw any) []source.Hook {
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	var out []source.Hook
	for cursorEvent, rawEntries := range hooks {
		canonEvent, ok := cursorToCanonicalHookEvent[cursorEvent]
		if !ok {
			continue // foreign Cursor event agentsync doesn't model
		}
		entries, ok := rawEntries.([]any)
		if !ok {
			continue
		}
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				continue
			}
			typ := asStr(entry["type"])
			if typ == "" {
				typ = "command"
			}
			out = append(out, source.Hook{
				Event:   canonEvent,
				Matcher: asStr(entry["matcher"]),
				Type:    typ,
				Command: asStr(entry["command"]),
			})
		}
	}
	return out
}
