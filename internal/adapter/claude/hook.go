package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
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
// This is intentionally MORE diagnostic than Gemini's twin: it also warns on the
// structurally-malformed shapes (non-object def/handler, non-array event value or
// "hooks" value) that Gemini's ingestHooks drops silently. Bringing Gemini to
// parity is a separate follow-up (out of this issue's scope).
func ingestHooks(raw any, warn io.Writer) (out []source.Hook, refused []string) {
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil, nil
	}
	for event, rawEntries := range hooks {
		entries, ok := rawEntries.([]any)
		if !ok {
			fmt.Fprintf(warn, "warning: hook event %q value is not an array; event not captured\n", event)
			refused = append(refused, event)
			continue
		}
		var captured []source.Hook
		representable := true
	defs:
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				fmt.Fprintf(warn, "warning: hook event %q has a malformed definition (not an object); event not captured\n", event)
				representable = false
				break
			}
			if extra := unmodeledKeys(entry, claudeHookDefModeledKeys); len(extra) > 0 {
				fmt.Fprintf(warn, "warning: hook event %q has a definition with unmodeled fields (%s); event not captured\n", event, strings.Join(extra, ", "))
				representable = false
				break
			}
			// A non-string matcher would be coerced to "" by asStr and captured as a
			// match-all — refuse the event rather than silently rewrite it.
			if rawMatcher, present := entry["matcher"]; present {
				if _, isStr := rawMatcher.(string); !isStr {
					fmt.Fprintf(warn, "warning: hook event %q has a definition whose \"matcher\" is not a string; event not captured\n", event)
					representable = false
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
				break
			}
			for _, rawH := range hooksArr {
				h, ok := rawH.(map[string]any)
				if !ok {
					fmt.Fprintf(warn, "warning: hook event %q has a malformed handler (not an object); event not captured\n", event)
					representable = false
					break defs
				}
				// A non-string type would be coerced to "" by asStr and captured as a
				// command handler; refuse the malformed shape instead.
				if rawType, present := h["type"]; present {
					if _, isStr := rawType.(string); !isStr {
						fmt.Fprintf(warn, "warning: hook event %q has a handler whose \"type\" is not a string; event not captured\n", event)
						representable = false
						break defs
					}
				}
				if typ := asStr(h["type"]); typ != "" && typ != "command" {
					fmt.Fprintf(warn, "warning: hook event %q has a %q-type handler agentsync cannot represent; event not captured\n", event, typ)
					representable = false
					break defs
				}
				if extra := unmodeledKeys(h, claudeHookEntryModeledKeys); len(extra) > 0 {
					fmt.Fprintf(warn, "warning: hook event %q has a handler with unmodeled fields (%s); event not captured\n", event, strings.Join(extra, ", "))
					representable = false
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
		} else {
			refused = append(refused, event)
		}
	}
	sort.Strings(refused) // map iteration order — keep output deterministic
	return out, refused
}

// RefusedHookEvents implements adapter.HookIngestGuard: it re-reads the
// destination settings file and returns the hook events whose native entries
// ingestHooks refuses to capture (non-command handlers, unmodeled fields,
// malformed shapes). Import uses this to retire a stale canonical
// hooks/<event>.toml: an event that was captured while clean and then enriched
// natively is skipped by Ingest, but its stale canonical file would keep the
// next apply owning — and lossily rewriting — the user's native array (the
// second-order corruption class of issue #124). Warnings are discarded here;
// Ingest already emitted them on the same shapes.
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
