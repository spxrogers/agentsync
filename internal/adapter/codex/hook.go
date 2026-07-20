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
		// event is a machine map key / lookup — raw, not the sanitizing String().
		event := h.Event.Unverified()
		if !codexHookEvents[event] {
			skips = append(skips, adapter.Skip{
				Component: "hook",
				Name:      h.Event.String(),
				Reason:    "Codex does not recognize this lifecycle event",
				Kind:      adapter.SkipDropped,
			})
			continue
		}
		// Codex only EXECUTES `command` hook handlers; it tolerates (parses and
		// skips) any other/empty `type` without rejecting the file. So a
		// non-command handler still round-trips through render, but it is a silent
		// no-op at runtime — surface that as a reduced skip so the user is told it
		// won't run. Empty type is left unreported (it is the common default and
		// not necessarily a mistake). See https://developers.openai.com/codex/.
		if h.Type != "" && h.Type != "command" {
			skips = append(skips, adapter.Skip{
				Component: "hook",
				Name:      h.Event.String(),
				Reason:    "Codex only runs `command` hook handlers; this handler type is parsed but never executed",
				Kind:      adapter.SkipReduced,
			})
		}
		entry := map[string]any{
			"matcher": h.Matcher,
			"hooks": []map[string]any{{
				"type":    h.Type,
				"command": h.Command,
			}},
		}
		byEvent[event] = append(byEvent[event], entry)
	}
	if len(byEvent) == 0 {
		return nil, skips, nil
	}
	obj := map[string]any{"hooks": byEvent}
	body, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal hooks: %w", err)
	}
	// OwnedKeys is intentionally left unset here: render.Plan populates it from
	// persisted state for every merge-toml-keys op (scoped to the op's sections),
	// so a hand-set value would be dead — always overwritten before Apply. The
	// hook orphan-cleanup path is exercised directly in settings_test.go
	// (TestMergeTOML_RemovesOrphanedHookKey). See adapter.FileOp.OwnedKeys.
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.Config,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "hooks/* (multiple)",
		MergeStrategy: "merge-toml-keys",
	}}, skips, nil
}
