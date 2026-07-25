package claude

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

// renderHooks writes a single op for settings.json containing /hooks/<event>
// entries. Per-event ownership: agentsync owns the entire array under its
// event key. Foreign event keys (e.g. PreToolUse if user has authored
// directly) are NOT touched if they're not in canonical.
//
// Render is the last line of defense against corrupting the user's real
// settings.json: the canonical Hook models only command handlers, so any hook
// whose Type is a non-empty non-"command" value is reported as a dropped Skip
// and never emitted — emitting a {type:"...",command:""} handler would let the
// owned-array overwrite clobber the user's native handler. An empty Type is
// treated as command-compatible (mirrors Gemini's `h.Type != "" && h.Type !=
// "command"` guard).
func (a *Adapter) renderHooks(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if len(c.Hooks) == 0 {
		return nil, nil, nil
	}
	byEvent := map[string][]map[string]any{}
	var skips []adapter.Skip
	for _, h := range c.Hooks {
		// agentsync models only command hooks (the only kind the canonical Hook
		// represents). Skip any other type with a report rather than emitting an
		// entry with an empty command that would overwrite the user's handler.
		if h.Type != "" && h.Type != "command" {
			skips = append(skips, adapter.Skip{
				Component: "hook",
				Name:      h.Event.String(),
				Reason:    fmt.Sprintf("agentsync models only command hooks; type %q is not projected", h.Type),
				Kind:      adapter.SkipDropped,
			})
			continue
		}
		entry := map[string]any{
			"matcher": h.Matcher,
			"hooks": []map[string]any{{
				"type":    h.Type,
				"command": h.Command,
			}},
		}
		// event is a machine map key / owned-key stem — raw, not the sanitizing String().
		event := h.Event.Unverified()
		byEvent[event] = append(byEvent[event], entry)
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
		Path:          p.Settings,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "hooks/* (multiple)",
		MergeStrategy: "merge-json-keys",
		OwnedKeys:     ownedKeys,
	}}, skips, nil
}

// Claude's documented hook schema is wider than the canonical source.Hook: a
// per-event definition can carry keys beyond {matcher, hooks} and an individual
// handler keys beyond {type, command} (e.g. `timeout`). These enumerate the
// fields the canonical model CAN represent; anything else in an event makes that
// event unrepresentable — see ingestHooks.
var (
	claudeHookDefModeledKeys   = map[string]bool{"matcher": true, "hooks": true}
	claudeHookEntryModeledKeys = map[string]bool{"type": true, "command": true}
)

// ingestHooks decodes settings.json's `hooks` object into canonical hooks,
// warning on anything it cannot capture. Inverse of renderHooks: each
// {type, command} handler becomes a source.Hook sharing the group's matcher.
// Unlike Gemini there is NO event-name remapping — every Claude event name is
// canonical. Per event, if ANY definition carries an unmodeled key, or ANY
// handler is a non-empty non-"command" type, or ANY handler carries an unmodeled
// key (e.g. `timeout`), the WHOLE event is left uncaptured with a warning.
//
// APPROACH A (no schema change): mirror the Gemini adapter's guard-and-warn
// posture rather than widen source.Hook. Capturing a lossy subset would let the
// next apply — which owns the whole per-event array — rewrite the user's native
// entry without the dropped fields; Render is the last line of defense (it skips
// non-command handlers), and this ingest guard keeps unrepresentable events out
// of the canonical source in the first place.
//
// Gemini's twin carries the same structural diagnostics and refusal reporting
// (parity landed with the epic #178 residual close); its refused list maps
// native event names back to canonical, since Gemini renames events.
func ingestHooks(raw any, warn io.Writer) (out []source.Hook, refused []string) {
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil, nil
	}
	for event, rawEntries := range hooks {
		entries, ok := rawEntries.([]any)
		if !ok {
			fmt.Fprintf(warn, "warning: hook event %q value is not an array; event not captured\n", event)
			continue // structural: warn + skip capture, but never a retire-triggering refusal
		}
		var captured []source.Hook
		representable := true
		// structural distinguishes a MALFORMED native shape (non-object def/handler,
		// non-string matcher/type/command, missing hooks array — likely a
		// settings.json typo) from a well-formed entry agentsync cannot MODEL
		// (unmodeled fields, a non-command handler). Only the latter joins refused:
		// import retires the canonical hooks/<event>.toml for refused events, and
		// deleting canonical config because the user typo'd their native JSON would
		// be destructive. First failure wins — the scan stops at the first
		// unrepresentable def/handler, so a structural typo EARLIER in the array
		// masks a semantic def later (no retirement that round). Deliberate: an
		// event containing a malformed shape can't be trusted for retirement at
		// all, every skip is warned, and fixing the typo re-runs the scan.
		structural := false
	defs:
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				fmt.Fprintf(warn, "warning: hook event %q has a malformed definition (not an object); event not captured\n", event)
				representable = false
				structural = true
				break
			}
			if extra := unmodeledKeys(entry, claudeHookDefModeledKeys); len(extra) > 0 {
				fmt.Fprintf(warn, "warning: hook event %q has a definition with unmodeled fields (%s); event not captured\n", event, quotedKeys(extra))
				representable = false
				break
			}
			// A non-string matcher would be coerced to "" by asStr and captured as a
			// match-all — refuse the event rather than silently rewrite it.
			if rawMatcher, present := entry["matcher"]; present {
				if _, isStr := rawMatcher.(string); !isStr {
					fmt.Fprintf(warn, "warning: hook event %q has a definition whose \"matcher\" is not a string; event not captured\n", event)
					representable = false
					structural = true
					break
				}
			}
			matcher := asStr(entry["matcher"])
			// A definition must carry a "hooks" ARRAY of handlers. An absent, null,
			// or non-array "hooks" value can't be captured, so leave the whole event
			// uncaptured rather than silently contribute zero handlers while a
			// sibling def keeps the event alive (which the next apply would then own
			// and clobber this def). An empty array is fine — a def with no handlers.
			hooksArr, isArr := entry["hooks"].([]any)
			if !isArr {
				fmt.Fprintf(warn, "warning: hook event %q has a definition without a valid \"hooks\" array; event not captured\n", event)
				representable = false
				structural = true
				break
			}
			for _, rawH := range hooksArr {
				h, ok := rawH.(map[string]any)
				if !ok {
					fmt.Fprintf(warn, "warning: hook event %q has a malformed handler (not an object); event not captured\n", event)
					representable = false
					structural = true
					break defs
				}
				// A non-string type would be coerced to "" by asStr and captured as a
				// command handler; refuse the malformed shape instead.
				if rawType, present := h["type"]; present {
					if _, isStr := rawType.(string); !isStr {
						fmt.Fprintf(warn, "warning: hook event %q has a handler whose \"type\" is not a string; event not captured\n", event)
						representable = false
						structural = true
						break defs
					}
				}
				if typ := asStr(h["type"]); typ != "" && typ != "command" {
					fmt.Fprintf(warn, "warning: hook event %q has a %q-type handler agentsync cannot represent; event not captured\n", event, typ)
					representable = false
					break defs
				}
				if extra := unmodeledKeys(h, claudeHookEntryModeledKeys); len(extra) > 0 {
					fmt.Fprintf(warn, "warning: hook event %q has a handler with unmodeled fields (%s); event not captured\n", event, quotedKeys(extra))
					representable = false
					break defs
				}
				// The unmodeled-keys check runs BEFORE the command checks (matching
				// codex): a handler carrying an unmodeled field AND lacking a command
				// must surface as a SEMANTIC (retirement-triggering) refusal on the
				// field, not be short-circuited into a structural skip.
				// An absent or non-string command would be asStr-coerced to "" and
				// captured as an EMPTY-command handler — which the next apply, owning
				// the whole per-event array, would write over the user's native
				// handler. Checked AFTER the type check so it governs only command-type
				// handlers: a native prompt-type handler legitimately has no command,
				// and must keep its SEMANTIC (retirement-triggering) refusal above.
				if rawCmd, present := h["command"]; !present {
					fmt.Fprintf(warn, "warning: hook event %q has a handler without a \"command\"; event not captured\n", event)
					representable = false
					structural = true
					break defs
				} else if _, isStr := rawCmd.(string); !isStr {
					fmt.Fprintf(warn, "warning: hook event %q has a handler whose \"command\" is not a string; event not captured\n", event)
					representable = false
					structural = true
					break defs
				}
				captured = append(captured, source.Hook{
					Event:   untrusted.Wrap(event), // native settings.json map key
					Matcher: matcher,
					Type:    asStr(h["type"]),
					Command: asStr(h["command"]),
				})
			}
		}
		if representable {
			out = append(out, captured...)
		} else if !structural {
			refused = append(refused, event)
		}
	}
	sort.Strings(refused) // map iteration order — keep output deterministic
	return out, refused
}

// RefusedHookEvents implements adapter.HookIngestGuard — see the interface doc
// for the shared contract (semantic-only refusals, canonical names, the issue
// #124 corruption class this closes) and ingestHooks for what claude refuses.
// Claude leg: settings.json is re-read with the same strict-JSON UseNumber
// decode Ingest uses (jsonkeys.DecodeObject); claude spells events
// canonically, so refused needs no name mapping. Warnings are discarded here —
// Ingest already emitted them on the same shapes. The re-read exists because
// Ingest's shared (source.Canonical, error) signature has no channel for
// refusals; both calls run within one import, so the worst case of the
// destination changing between them is a stale warning.
func (a *Adapter) RefusedHookEvents(scope adapter.Scope, project string) ([]string, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	data, present, err := adapter.ReadFileOptional(p.Settings)
	if err != nil || !present {
		return nil, err
	}
	top, err := jsonkeys.DecodeObject(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", p.Settings, err)
	}
	_, refused := ingestHooks(top["hooks"], io.Discard)
	return refused, nil
}

// unmodeledKeys returns the sorted keys of m that are not in modeled.
func unmodeledKeys(m map[string]any, modeled map[string]bool) []string {
	var out []string
	for k := range m {
		if !modeled[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
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
