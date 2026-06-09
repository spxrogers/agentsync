package gemini

import (
	"encoding/json"
	"fmt"

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

// renderHooks writes a single merge-json-keys op for settings.json's `hooks`
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
		MergeStrategy: "merge-json-keys",
		OwnedKeys:     ownedKeys,
	}}, skips, nil
}

// ingestHooks decodes settings.json's `hooks` object into canonical hooks.
// Inverse of renderHooks: each Gemini event is mapped back to its canonical name
// (events with no canonical equivalent are skipped), and each {type, command}
// handler becomes a source.Hook sharing the group's matcher.
func ingestHooks(raw any) []source.Hook {
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	var out []source.Hook
	for geminiEvent, rawEntries := range hooks {
		canonEvent, ok := geminiToCanonicalHookEvent[geminiEvent]
		if !ok {
			continue // Gemini-only event agentsync doesn't model
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
			matcher := asStr(entry["matcher"])
			hooksArr, _ := entry["hooks"].([]any)
			for _, rawH := range hooksArr {
				h, ok := rawH.(map[string]any)
				if !ok {
					continue
				}
				out = append(out, source.Hook{
					Event:   canonEvent,
					Matcher: matcher,
					Type:    asStr(h["type"]),
					Command: asStr(h["command"]),
				})
			}
		}
	}
	return out
}
