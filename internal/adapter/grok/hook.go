package grok

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
	"github.com/spxrogers/agentsync/internal/source"
)

// Grok uses canonical event names but does not implement every Claude event.
var hookEvents = []string{
	"SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"PostToolUseFailure", "PermissionDenied", "Stop", "StopFailure", "Notification",
	"SubagentStart", "SubagentStop", "PreCompact", "PostCompact",
}

func renderHooks(hooks []source.Hook, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	byEvent := map[string][]map[string]any{}
	var skips []adapter.Skip
	for _, h := range hooks {
		event := h.Event.Unverified()
		if !slices.Contains(hookEvents, event) || (h.Type != "" && h.Type != "command") {
			skips = append(skips, adapter.Skip{
				Component: "hook", Name: h.Event.String(), Kind: adapter.SkipDropped,
				Reason: "Grok projection requires a supported lifecycle event and a command handler",
			})
			continue
		}
		handler := map[string]any{"command": h.Command}
		if h.Type != "" {
			handler["type"] = h.Type
		}
		groups := byEvent[event]
		// Restore consecutive handlers in the same matcher group, preserving
		// their execution order across native -> canonical -> native capture.
		if len(groups) > 0 && groups[len(groups)-1]["matcher"] == h.Matcher {
			last := groups[len(groups)-1]
			last["hooks"] = append(last["hooks"].([]map[string]any), handler)
		} else {
			groups = append(groups, map[string]any{"matcher": h.Matcher, "hooks": []map[string]any{handler}})
		}
		byEvent[event] = groups
	}
	if len(byEvent) == 0 {
		return nil, skips, nil
	}
	body, err := json.MarshalIndent(map[string]any{"hooks": byEvent}, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal grok hooks: %w", err)
	}
	return []adapter.FileOp{{
		Path: p.Hooks, Content: append(body, '\n'), Mode: 0o644,
		SourceID: "hooks/* (multiple)", MergeStrategy: "merge-json-keys",
	}}, skips, nil
}

// Only agentsync.json is captured. Importing other hook files into this file
// would leave the originals active and execute every imported hook twice.
func readHooks(path string, warn io.Writer) ([]source.Hook, []string, error) {
	data, present, err := adapter.ReadFileOptional(path)
	if err != nil || !present {
		return nil, nil, err
	}
	top, err := jsonkeys.DecodeObject(data)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if raw, present := top["hooks"]; present {
		if _, ok := raw.(map[string]any); !ok {
			return nil, nil, fmt.Errorf("parse %s: hooks must be an object", path)
		}
	}
	hooks, refused := claude.IngestHooks(top["hooks"], warn)
	var out []source.Hook
	for _, h := range hooks {
		if slices.Contains(hookEvents, h.Event.Unverified()) {
			out = append(out, h)
		} else {
			fmt.Fprintf(warn, "warning: Grok hook event %q is unsupported; event not captured\n", h.Event.Unverified())
			refused = append(refused, h.Event.Unverified())
		}
	}
	slices.Sort(refused)
	return out, slices.Compact(refused), nil
}

func (a *Adapter) RefusedHookEvents(scope adapter.Scope, project string) ([]string, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, err
	}
	if err := a.validateHome(); err != nil {
		return nil, err
	}
	_, refused, err := readHooks(a.resolvePaths(scope, project).Hooks, io.Discard)
	return refused, err
}
