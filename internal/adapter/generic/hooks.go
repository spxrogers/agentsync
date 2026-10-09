package generic

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// factoryHookEvents is the event set Factory Droid documents for hooks.json
// (docs.factory.ai/reference/hooks-reference). Names match the canonical
// events, except PostCompact, which Droid does not have. An unsupported
// canonical event is skipped, never refused: refusing it would delete the
// shared hooks/<event>.toml other agents still use.
var factoryHookEvents = map[string]bool{
	"PreToolUse":       true,
	"PostToolUse":      true,
	"UserPromptSubmit": true,
	"Notification":     true,
	"Stop":             true,
	"SubagentStop":     true,
	"PreCompact":       true,
	"SessionStart":     true,
	"SessionEnd":       true,
}

var (
	factoryHookDefModeled   = map[string]bool{"matcher": true, "hooks": true}
	factoryHookEntryModeled = map[string]bool{"type": true, "command": true, "timeout": true}
)

// renderHooks writes hooks.json. The file's root IS the event map (not a
// "hooks" wrapper). agentsync owns each event array it renders; foreign
// top-level keys (hooksDisabled, showHookOutput) stay via merge-jsonc-keys.
// Timeout is seconds, the same unit Droid documents.
func (a *Adapter) renderHooks(c source.Canonical, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	path := a.hooksPath(scope, project)
	if path == "" || len(c.Hooks) == 0 {
		return nil, nil, nil
	}
	byEvent := map[string][]map[string]any{}
	var skips []adapter.Skip
	for _, h := range c.Hooks {
		event := h.Event.Unverified()
		if !factoryHookEvents[event] {
			skips = append(skips, adapter.Skip{
				Component: "hook", Name: event,
				Reason: fmt.Sprintf("Factory Droid has no %q hook event", event),
				Kind:   adapter.SkipDropped,
			})
			continue
		}
		if h.Type != "" && h.Type != "command" {
			skips = append(skips, adapter.Skip{
				Component: "hook", Name: event,
				Reason: fmt.Sprintf("agentsync models only command hooks; type %q is not projected", h.Type),
				Kind:   adapter.SkipDropped,
			})
			continue
		}
		handler := map[string]any{"type": "command", "command": h.Command}
		adapter.SetHookTimeout(handler, h.Timeout)
		entry := map[string]any{
			"matcher": h.Matcher,
			"hooks":   []map[string]any{handler},
		}
		byEvent[event] = append(byEvent[event], entry)
	}
	if len(byEvent) == 0 {
		return nil, skips, nil
	}
	owned := make([]string, 0, len(byEvent))
	for event := range byEvent {
		owned = append(owned, "/"+event)
	}
	sort.Strings(owned)
	body, err := json.MarshalIndent(byEvent, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal hooks: %w", err)
	}
	return []adapter.FileOp{{
		Action:        adapter.ActionWrite,
		Path:          path,
		Content:       append(body, '\n'),
		Mode:          0o644,
		SourceID:      "hooks/* (multiple)",
		MergeStrategy: "merge-jsonc-keys",
		OwnedKeys:     owned,
	}}, skips, nil
}

// ingestHooks reads hooks.json. When that file is absent, Droid falls back to
// the hooks key of the matching settings.json; import follows that fallback
// so those hooks are not invisible. Apply always writes hooks.json, which is
// the file Droid prefers once it exists.
func (a *Adapter) ingestHooks(scope adapter.Scope, project string) ([]source.Hook, error) {
	hooks, _, err := a.readFactoryHooks(scope, project, a.warn())
	return hooks, err
}

// hookedAdapter is what the registry stores for a spec that declares hooks.
// The method is on this type, not *Adapter, so the other breadth agents do
// not implement HookIngestGuard. Import's type assert would otherwise treat
// all 22 as hook guards, and the registry contract test would try to enrich
// a hooks file none of them write.
type hookedAdapter struct{ *Adapter }

// RefusedHookEvents implements adapter.HookIngestGuard. Only Factory-documented
// events are refused; an unsupported event must not retire the shared
// canonical file.
func (a *hookedAdapter) RefusedHookEvents(scope adapter.Scope, project string) ([]string, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, err
	}
	_, refused, err := a.readFactoryHooks(scope, project, io.Discard)
	return refused, err
}

// Register returns the value the CLI registry stores. Specs that declare a
// hooks file are wrapped so only they implement HookIngestGuard.
func Register(spec Spec, opts Options) adapter.Adapter {
	a := New(spec, opts)
	if spec.Hooks.User == "" && spec.Hooks.Project == "" {
		return a
	}
	return &hookedAdapter{Adapter: a}
}

func (a *Adapter) readFactoryHooks(scope adapter.Scope, project string, warn io.Writer) ([]source.Hook, []string, error) {
	path := a.hooksPath(scope, project)
	if path == "" {
		return nil, nil, nil
	}
	data, present, err := adapter.ReadFileOptional(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]any
	if present {
		doc, err = jsonkeys.DecodeJSONC(data)
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if doc, err = a.settingsHooks(scope, project, warn); err != nil {
		return nil, nil, err
	}
	if doc == nil {
		return nil, nil, nil
	}
	hooks, refused := parseFactoryHooks(doc, warn)
	return hooks, refused, nil
}

// settingsHooks reads the hooks object from settings.json beside hooks.json.
// A missing file or a missing hooks key is "no hooks", not an error.
func (a *Adapter) settingsHooks(scope adapter.Scope, project string, warn io.Writer) (map[string]any, error) {
	hooksFile := a.hooksPath(scope, project)
	if hooksFile == "" {
		return nil, nil
	}
	settings := filepath.Join(filepath.Dir(hooksFile), "settings.json")
	data, present, err := adapter.ReadFileOptional(settings)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", settings, err)
	}
	if !present {
		return nil, nil
	}
	doc, err := jsonkeys.DecodeJSONC(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", settings, err)
	}
	raw, ok := doc["hooks"]
	if !ok {
		return nil, nil
	}
	hooks, ok := raw.(map[string]any)
	if !ok {
		fmt.Fprintf(warn, "warning: %s hooks value is not an object; hooks not captured\n", settings)
		return nil, nil
	}
	return hooks, nil
}

func parseFactoryHooks(doc map[string]any, warn io.Writer) (out []source.Hook, refused []string) {
	events := make([]string, 0, len(doc))
	for event := range doc {
		events = append(events, event)
	}
	sort.Strings(events)
	for _, event := range events {
		raw := doc[event]
		if !factoryHookEvents[event] {
			if _, isArray := raw.([]any); isArray {
				fmt.Fprintf(warn, "warning: hook event %q is not a Factory Droid event; event not captured\n", event)
			}
			continue
		}
		entries, ok := raw.([]any)
		if !ok {
			fmt.Fprintf(warn, "warning: hook event %q value is not an array; event not captured\n", event)
			continue
		}
		var captured []source.Hook
		representable := true
		structural := false
		for _, item := range entries {
			def, ok := item.(map[string]any)
			if !ok {
				fmt.Fprintf(warn, "warning: hook event %q has an entry that is not an object; event not captured\n", event)
				structural = true
				representable = false
				break
			}
			if extra := unmodeledKeys(def, factoryHookDefModeled); extra != "" {
				fmt.Fprintf(warn, "warning: hook event %q has %s; event not captured\n", event, extra)
				representable = false
				break
			}
			handlers, ok := def["hooks"].([]any)
			if !ok {
				fmt.Fprintf(warn, "warning: hook event %q entry has no hooks array; event not captured\n", event)
				structural = true
				representable = false
				break
			}
			matcher, _ := def["matcher"].(string)
			if _, present := def["matcher"]; present {
				if _, isStr := def["matcher"].(string); !isStr {
					fmt.Fprintf(warn, "warning: hook event %q matcher is not a string; event not captured\n", event)
					structural = true
					representable = false
					break
				}
			}
			for _, hraw := range handlers {
				h, ok := hraw.(map[string]any)
				if !ok {
					fmt.Fprintf(warn, "warning: hook event %q has a handler that is not an object; event not captured\n", event)
					structural = true
					representable = false
					break
				}
				if extra := unmodeledKeys(h, factoryHookEntryModeled); extra != "" {
					fmt.Fprintf(warn, "warning: hook event %q has a handler with %s; event not captured\n", event, extra)
					representable = false
					break
				}
				typ, _ := h["type"].(string)
				if typ != "" && typ != "command" {
					fmt.Fprintf(warn, "warning: hook event %q has a %q-type handler agentsync cannot represent; event not captured\n", event, typ)
					representable = false
					break
				}
				cmd, cmdOK := h["command"].(string)
				if !cmdOK || cmd == "" {
					fmt.Fprintf(warn, "warning: hook event %q has a handler without a command; event not captured\n", event)
					structural = true
					representable = false
					break
				}
				timeout, tres := adapter.ParseHookTimeout(h)
				if tres != source.HookTimeoutOK {
					fmt.Fprintf(warn, "warning: hook event %q has a handler with %s; event not captured\n", event, tres.Reason())
					if tres.Structural() {
						structural = true
					}
					representable = false
					break
				}
				captured = append(captured, source.Hook{
					Event: untrusted.Wrap(event), Matcher: matcher, Type: "command", Command: cmd, Timeout: timeout,
				})
			}
			if !representable {
				break
			}
		}
		if !representable {
			if !structural {
				refused = append(refused, event)
			}
			continue
		}
		out = append(out, captured...)
	}
	return out, refused
}

func unmodeledKeys(obj map[string]any, modeled map[string]bool) string {
	var extra []string
	for k := range obj {
		if !modeled[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) == 0 {
		return ""
	}
	sort.Strings(extra)
	return fmt.Sprintf("unmodeled field %q", extra[0])
}
