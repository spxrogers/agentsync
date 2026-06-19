package gemini

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// canonicalToGeminiHookEvent maps agentsync's canonical (Claude-shaped)
// lifecycle events to Gemini CLI's event names (per geminicli.com/docs/hooks).
// A canonical event with no entry here (SubagentStart/SubagentStop/PostCompact/
// PermissionRequest) has no Gemini target and is dropped with a reported Skip.
var canonicalToGeminiHookEvent = map[string]string{
	"PreToolUse":       "BeforeTool",
	"PostToolUse":      "AfterTool",
	"UserPromptSubmit": "BeforeAgent",
	"Stop":             "AfterAgent",
	"SessionStart":     "SessionStart",
	"SessionEnd":       "SessionEnd",
	"PreCompact":       "PreCompress",
	"Notification":     "Notification",
}

// geminiToCanonicalHookEvent is the inverse, used by Ingest. A Gemini-only event
// (BeforeModel/AfterModel/BeforeToolSelection) has no canonical equivalent and is
// not captured: agentsync only round-trips events it can also render.
var geminiToCanonicalHookEvent = invertHookEvents(canonicalToGeminiHookEvent)

func invertHookEvents(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

// renderHooks writes a single merge-jsonc-keys op for settings.json's `hooks`
// object. Gemini's hook schema is the SAME nested shape as Claude's (event →
// matcher group → hooks array of {type, command}), so the canonical Hook is
// remapped (event name only) rather than reshaped. Per-event ownership: agentsync
// owns the whole array under each event key it renders; foreign event keys the
// user authored are untouched. settings.json is shared with `mcpServers` (the
// other section this adapter owns), and the render pipeline scopes each op's
// OwnedKeys to its own section so they never clobber each other.
func (a *Adapter) renderHooks(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if len(c.Hooks) == 0 {
		return nil, nil, nil
	}
	byEvent := map[string][]map[string]any{}
	var skips []adapter.Skip
	for _, h := range c.Hooks {
		ge, ok := canonicalToGeminiHookEvent[h.Event]
		if !ok {
			skips = append(skips, adapter.Skip{
				Component: "hook",
				Name:      h.Event,
				Reason:    "Gemini CLI has no equivalent hook event",
				Kind:      adapter.SkipDropped,
			})
			continue
		}
		// agentsync models only command hooks (the only execution engine Gemini
		// documents, and the only kind the canonical Hook represents). Skip any
		// other type with a report rather than emitting an entry Gemini would
		// reject.
		if h.Type != "" && h.Type != "command" {
			skips = append(skips, adapter.Skip{
				Component: "hook",
				Name:      h.Event,
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
		byEvent[ge] = append(byEvent[ge], entry)
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
		MergeStrategy: "merge-jsonc-keys",
		OwnedKeys:     ownedKeys,
	}}, skips, nil
}

// Gemini's documented hook schema is wider than the canonical source.Hook:
// definitions can carry `sequential`, and individual handlers `name`/`timeout`.
// These enumerate the fields the canonical model CAN represent; anything else in
// an event makes that event unrepresentable — see ingestHooks.
var (
	geminiHookDefModeledKeys   = map[string]bool{"matcher": true, "hooks": true}
	geminiHookEntryModeledKeys = map[string]bool{"type": true, "command": true}
)

// ingestHooks decodes settings.json's `hooks` object into canonical hooks,
// warning on anything it cannot capture. Inverse of renderHooks: each Gemini
// event is mapped back to its canonical name, and each {type, command} handler
// becomes a source.Hook sharing the group's matcher. Two fail-safe skips, both
// warned: a Gemini-only event with no canonical equivalent (BeforeModel,
// AfterModel, BeforeToolSelection), and an event containing a definition or
// handler the canonical model cannot represent (non-command type, `sequential`,
// `name`, `timeout`, …). Capturing a lossy subset would let the next apply —
// which owns the whole per-event array — rewrite the user's native entry without
// those fields, so the WHOLE event is left uncaptured instead.
func ingestHooks(raw any, warn io.Writer) []source.Hook {
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	var out []source.Hook
	for geminiEvent, rawEntries := range hooks {
		canonEvent, ok := geminiToCanonicalHookEvent[geminiEvent]
		if !ok {
			fmt.Fprintf(warn, "warning: hook event %q has no canonical equivalent; not captured\n", geminiEvent)
			continue
		}
		entries, ok := rawEntries.([]any)
		if !ok {
			continue
		}
		var captured []source.Hook
		representable := true
	defs:
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				representable = false
				break
			}
			if extra := unmodeledKeys(entry, geminiHookDefModeledKeys); len(extra) > 0 {
				fmt.Fprintf(warn, "warning: hook event %q has a definition with unmodeled fields (%s); event not captured\n", geminiEvent, strings.Join(extra, ", "))
				representable = false
				break
			}
			matcher := asStr(entry["matcher"])
			hooksArr, _ := entry["hooks"].([]any)
			for _, rawH := range hooksArr {
				h, ok := rawH.(map[string]any)
				if !ok {
					representable = false
					break defs
				}
				if typ := asStr(h["type"]); typ != "" && typ != "command" {
					fmt.Fprintf(warn, "warning: hook event %q has a %q-type handler agentsync cannot represent; event not captured\n", geminiEvent, typ)
					representable = false
					break defs
				}
				if extra := unmodeledKeys(h, geminiHookEntryModeledKeys); len(extra) > 0 {
					fmt.Fprintf(warn, "warning: hook event %q has a handler with unmodeled fields (%s); event not captured\n", geminiEvent, strings.Join(extra, ", "))
					representable = false
					break defs
				}
				captured = append(captured, source.Hook{
					Event:   canonEvent,
					Matcher: matcher,
					Type:    asStr(h["type"]),
					Command: asStr(h["command"]),
				})
			}
		}
		if representable {
			out = append(out, captured...)
		}
	}
	return out
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
