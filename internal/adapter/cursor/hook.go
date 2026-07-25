package cursor

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
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
		ce, ok := canonicalToCursorHookEvent[h.Event.Unverified()]
		if !ok {
			skips = append(skips, adapter.Skip{
				Component: "hook",
				Name:      h.Event.String(),
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
				Name:      h.Event.String(),
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
// becomes a source.Hook with a missing type defaulting to "command". A
// Cursor-native event with no canonical equivalent (afterFileEdit,
// beforeShellExecution, …) is warned and skipped — never refused: no canonical
// hooks/<event>.toml can exist for it, so there is nothing for import to
// retire. A mappable event containing an entry the canonical model cannot
// represent (non-command type or an unmodeled field — see
// cursorHookEntryModeledKeys) is left uncaptured whole, with a warning.
//
// refused reports the SEMANTICALLY refused events under their CANONICAL names
// (the spelling import retires — hooks/<canonical>.toml), sorted. The
// structural flag mirrors the claude/gemini twins: a malformed native shape
// (non-object entry, non-string matcher/type/command — likely a hooks.json
// typo) warns and skips capture but never joins refused, because import
// deletes canonical config for every refused event and a native typo must not
// be destructive. First failure wins — a structural typo earlier in the array
// masks a semantic entry later (deliberate; see the claude twin's comment).
func ingestHooks(raw any, warn io.Writer) (out []source.Hook, refused []string) {
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil, nil
	}
	for cursorEvent, rawEntries := range hooks {
		canonEvent, ok := cursorToCanonicalHookEvent[cursorEvent]
		if !ok {
			fmt.Fprintf(warn, "warning: hook event %q has no canonical equivalent; not captured\n", cursorEvent)
			continue
		}
		entries, ok := rawEntries.([]any)
		if !ok {
			fmt.Fprintf(warn, "warning: hook event %q value is not an array; event not captured\n", cursorEvent)
			continue // structural: warn + skip capture, but never a retire-triggering refusal
		}
		var captured []source.Hook
		representable := true
		structural := false
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				fmt.Fprintf(warn, "warning: hook event %q has a malformed entry (not an object); event not captured\n", cursorEvent)
				representable = false
				structural = true
				break
			}
			// A non-string matcher/type would be asStr-coerced ("" match-all, ""
			// type promoted to "command") and captured wrong; refuse the malformed
			// shape instead — structurally, so a typo never triggers retirement.
			bad := false
			for _, key := range []string{"matcher", "type"} {
				if rawVal, present := entry[key]; present {
					if _, isStr := rawVal.(string); !isStr {
						fmt.Fprintf(warn, "warning: hook event %q has an entry whose %q is not a string; event not captured\n", cursorEvent, key)
						bad = true
						break
					}
				}
			}
			if bad {
				representable = false
				structural = true
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
			// An absent or non-string command would be asStr-coerced to "" and
			// captured as an EMPTY-command entry the next apply would then write
			// over the user's native entry. Checked AFTER the type check so it
			// governs only command-type entries: a native prompt-type entry
			// legitimately has no command, and must keep its SEMANTIC
			// (retirement-triggering) refusal above.
			if rawCmd, present := entry["command"]; !present {
				fmt.Fprintf(warn, "warning: hook event %q has an entry without a \"command\"; event not captured\n", cursorEvent)
				representable = false
				structural = true
				break
			} else if _, isStr := rawCmd.(string); !isStr {
				fmt.Fprintf(warn, "warning: hook event %q has an entry whose \"command\" is not a string; event not captured\n", cursorEvent)
				representable = false
				structural = true
				break
			}
			if extra := unmodeledKeys(entry, cursorHookEntryModeledKeys); len(extra) > 0 {
				fmt.Fprintf(warn, "warning: hook event %q has an entry with unmodeled fields (%s); event not captured\n", cursorEvent, quotedKeys(extra))
				representable = false
				break
			}
			captured = append(captured, source.Hook{
				Event:   untrusted.Wrap(canonEvent), // remapped from native config
				Matcher: asStr(entry["matcher"]),
				Type:    typ,
				Command: asStr(entry["command"]),
			})
		}
		if representable {
			out = append(out, captured...)
		} else if !structural {
			refused = append(refused, canonEvent)
		}
	}
	sort.Strings(refused) // map iteration order — keep output deterministic
	return out, refused
}

// RefusedHookEvents implements adapter.HookIngestGuard — see the interface doc
// for the shared contract and ingestHooks for what cursor refuses. Cursor leg:
// hooks.json is re-read exactly as Ingest parses it (strict JSON, UseNumber);
// refused events surface under their CANONICAL names (preToolUse →
// PreToolUse), and Cursor-only events (afterFileEdit, …) never appear — no
// canonical file exists to retire. Warnings are discarded here; Ingest already
// emitted them on the same shapes.
func (a *Adapter) RefusedHookEvents(scope adapter.Scope, project string) ([]string, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	data, present, err := adapter.ReadFileOptional(p.Hooks)
	if err != nil || !present {
		return nil, err
	}
	top, err := jsonkeys.DecodeObject(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", p.Hooks, err)
	}
	_, refused := ingestHooks(top["hooks"], io.Discard)
	return refused, nil
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

// NativeHookEvent implements adapter.HookEventNamer: Cursor camelCases
// canonical hook events in hooks.json (PreToolUse -> preToolUse, …), so
// import's stale-hook retirement must disown cursor's "/hooks/<native>" state
// key under the NATIVE spelling — matching only the canonical name would leave
// the key owned with no canonical render, and the next apply's orphan cleanup
// would delete the event from the user's hooks.json.
func (a *Adapter) NativeHookEvent(canonical string) (string, bool) {
	native, ok := canonicalToCursorHookEvent[canonical]
	return native, ok
}

// quotedKeys renders untrusted native key names for a warning line: each key
// %q-quoted (control bytes escaped — a key containing a newline cannot forge
// a second warning line), comma-separated.
func quotedKeys(keys []string) string {
	qs := make([]string, len(keys))
	for i, k := range keys {
		qs[i] = strconv.Quote(k)
	}
	return strings.Join(qs, ", ")
}
