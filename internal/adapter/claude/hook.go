package claude

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderHooks writes a single op for settings.json containing /hooks/<event>
// entries. Per-event ownership: agentsync owns the entire array under its
// event key. Foreign event keys (e.g. PreToolUse if user has authored
// directly) are NOT touched if they're not in canonical.
func (a *Adapter) renderHooks(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
	if len(c.Hooks) == 0 {
		return nil, nil
	}
	byEvent := map[string][]map[string]any{}
	var ownedKeys []string
	for _, h := range c.Hooks {
		entry := map[string]any{
			"matcher": h.Matcher,
			"hooks": []map[string]any{{
				"type":    h.Type,
				"command": h.Command,
			}},
		}
		byEvent[h.Event] = append(byEvent[h.Event], entry)
	}
	for event := range byEvent {
		ownedKeys = append(ownedKeys, "/hooks/"+event)
	}
	obj := map[string]any{"hooks": byEvent}
	body, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal hooks: %w", err)
	}
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.Settings,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "hooks/* (multiple)",
		MergeStrategy: "merge-json-keys",
		OwnedKeys:     ownedKeys,
	}}, nil
}
