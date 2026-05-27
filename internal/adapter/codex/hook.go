package codex

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// codexHookEvents is the set of lifecycle events Codex recognizes in
// hooks.json (per the Codex hooks docs). A canonical hook whose event is not in
// this set has no Codex target and is dropped with a reported Skip.
var codexHookEvents = map[string]bool{
	"SessionStart":      true,
	"SubagentStart":     true,
	"PreToolUse":        true,
	"PermissionRequest": true,
	"PostToolUse":       true,
	"PreCompact":        true,
	"PostCompact":       true,
	"UserPromptSubmit":  true,
	"SubagentStop":      true,
	"Stop":              true,
}

// renderHooks writes a single merge-toml-keys op for ~/.codex/config.toml's
// `[hooks.*]` tables. Codex reads hooks from either a hooks.json or inline
// `[hooks]` tables in config.toml (the two are equivalent); agentsync uses the
// config.toml form so Codex has a SINGLE key-merge destination/strategy — the
// orphan-cleanup synthesis in the render pipeline applies one KeyMergeStrategy()
// per adapter, so a second key-merge file in a different format (JSON hooks.json)
// would be cleaned with the wrong (TOML) strategy and fail. op.Content is JSON
// (`{"hooks": {<event>: [...]}}`) — the pointer-merge currency — and MergeTOML
// emits it as `[[hooks.<event>]]` arrays-of-tables. The schema (event → matcher
// group → command handlers) matches Claude's. Per-event ownership: agentsync owns
// the entire array under each event key it renders; foreign event keys the user
// authored are left untouched. Canonical events Codex doesn't recognize are
// dropped with a Skip.
func (a *Adapter) renderHooks(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if len(c.Hooks) == 0 {
		return nil, nil, nil
	}
	byEvent := map[string][]map[string]any{}
	var skips []adapter.Skip
	for _, h := range c.Hooks {
		if !codexHookEvents[h.Event] {
			skips = append(skips, adapter.Skip{
				Component: "hook",
				Name:      h.Event,
				Reason:    "Codex does not recognize this lifecycle event",
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
		byEvent[h.Event] = append(byEvent[h.Event], entry)
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
		Path:          p.Config,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "hooks/* (multiple)",
		MergeStrategy: "merge-toml-keys",
		OwnedKeys:     ownedKeys,
	}}, skips, nil
}
