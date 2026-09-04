package gemini

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
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

// geminiAlwaysFireHookEvent marks the Gemini events that ALWAYS fire and take no
// matcher. Per geminicli.com/docs/hooks (upstream event table), only the tool
// events BeforeTool/AfterTool accept a (regex) matcher; every lifecycle event
// — BeforeAgent, AfterAgent, SessionStart, SessionEnd, PreCompress, Notification
// — fires unconditionally, so a matcher there is silently ignored by Gemini. A
// non-empty canonical Matcher on one of these is dropped with a reported
// SkipReduced (the "capture it or acknowledge it" invariant) rather than emitted
// into a position that can never take effect. Keyed by the Gemini event name (the
// value side of canonicalToGeminiHookEvent).
var geminiAlwaysFireHookEvent = map[string]bool{
	"BeforeAgent":  true,
	"AfterAgent":   true,
	"SessionStart": true,
	"SessionEnd":   true,
	"PreCompress":  true,
	"Notification": true,
}

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
//
// Three on-disk-fidelity behaviors reconstruct the native shape from the flat
// canonical model: (1) consecutive Hooks sharing the same (Event, Matcher) are
// COALESCED into a single group whose `hooks` array carries one {type, command}
// per Hook — the faithful inverse of ingestHooks, which flattens a native
// multi-handler group into N flat Hooks, so ingest→render is idempotent; (2) an
// empty Type is OMITTED (never a stray "type":"" Gemini did not ask for); (3) a
// matcher on an always-fire event is dropped with a reported SkipReduced (Gemini
// ignores it there — see geminiAlwaysFireHookEvent).
func (a *Adapter) renderHooks(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if len(c.Hooks) == 0 {
		return nil, nil, nil
	}
	byEvent := map[string][]map[string]any{}
	var skips []adapter.Skip
	for _, h := range c.Hooks {
		ge, ok := canonicalToGeminiHookEvent[h.Event.Unverified()]
		if !ok {
			skips = append(skips, adapter.Skip{
				Component: "hook",
				Name:      h.Event.String(),
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
				Name:      h.Event.String(),
				Reason:    fmt.Sprintf("agentsync models only command hooks; type %q is not projected", h.Type),
				Kind:      adapter.SkipDropped,
			})
			continue
		}
		// Always-fire events take no matcher; a non-empty one would be silently
		// ignored by Gemini, so drop it with a reported SkipReduced instead of
		// emitting it into a position that can never take effect.
		matcher := h.Matcher
		if matcher != "" && geminiAlwaysFireHookEvent[ge] {
			skips = append(skips, adapter.Skip{
				Component: "hook",
				Name:      h.Event.String(),
				Reason:    fmt.Sprintf("Gemini event %s is always-fire and takes no matcher; matcher %q dropped", ge, matcher),
				Kind:      adapter.SkipReduced,
			})
			matcher = ""
		}
		// Omit an empty "type": the canonical Hook documents Type as "command"
		// but does not require it, and "type":"" is not something Gemini asked
		// for. When present, Type is always "command" (the only kind reached here).
		handler := map[string]any{"command": h.Command}
		if h.Type != "" {
			handler["type"] = h.Type
		}
		// Coalesce with the previous group iff this Hook shares its (event,
		// matcher) — reconstructing the native multi-handler group (one group, an
		// N-element `hooks` array) rather than exploding into N single-handler
		// groups. The flat model preserves order, so consecutive same-(event,
		// matcher) Hooks are exactly the handlers one native group ingested to.
		if groups := byEvent[ge]; len(groups) > 0 && groups[len(groups)-1]["matcher"] == matcher {
			last := groups[len(groups)-1]
			last["hooks"] = append(last["hooks"].([]map[string]any), handler)
		} else {
			byEvent[ge] = append(groups, map[string]any{
				"matcher": matcher,
				"hooks":   []map[string]any{handler},
			})
		}
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
		Action:        adapter.ActionWrite,
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
// becomes a source.Hook sharing the group's matcher. A Gemini-only event with
// no canonical equivalent (BeforeModel, AfterModel, BeforeToolSelection) is
// warned and skipped — never refused: no canonical hooks/<event>.toml can
// exist for it, so there is nothing for import to retire. For a mappable
// event, if ANY definition carries an unmodeled key (`sequential`), or ANY
// handler is a non-empty non-"command" type, or ANY handler carries an
// unmodeled key (`name`, `timeout`, …), the WHOLE event is left uncaptured
// with a warning: capturing a lossy subset would let the next apply — which
// owns the whole per-event array — rewrite the user's native entry without
// those fields.
//
// refused reports the SEMANTICALLY refused events under their CANONICAL names
// (the spelling import retires — hooks/<canonical>.toml), sorted. The
// structural flag mirrors the claude twin: a malformed native shape (non-object
// def/handler, non-string matcher/type/command, missing hooks array — likely a
// settings.json typo) warns and skips capture but never joins refused, because
// import deletes canonical config for every refused event and a native typo
// must not be destructive. First failure wins — a structural typo earlier in
// the array masks a semantic def later (deliberate; see the claude twin's
// comment). Diagnostics are at parity with claude's ingestHooks (malformed
// shapes warn instead of dropping silently).
func ingestHooks(raw any, warn io.Writer) (out []source.Hook, refused []string) {
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil, nil
	}
	for geminiEvent, rawEntries := range hooks {
		canonEvent, ok := geminiToCanonicalHookEvent[geminiEvent]
		if !ok {
			fmt.Fprintf(warn, "warning: hook event %q has no canonical equivalent; not captured\n", geminiEvent)
			continue
		}
		entries, ok := rawEntries.([]any)
		if !ok {
			fmt.Fprintf(warn, "warning: hook event %q value is not an array; event not captured\n", geminiEvent)
			continue // structural: warn + skip capture, but never a retire-triggering refusal
		}
		var captured []source.Hook
		representable := true
		structural := false
	defs:
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				fmt.Fprintf(warn, "warning: hook event %q has a malformed definition (not an object); event not captured\n", geminiEvent)
				representable = false
				structural = true
				break
			}
			if extra := adapter.UnmodeledKeys(entry, geminiHookDefModeledKeys); len(extra) > 0 {
				fmt.Fprintf(warn, "warning: hook event %q has a definition with unmodeled fields (%s); event not captured\n", geminiEvent, adapter.QuotedKeys(extra))
				representable = false
				break
			}
			// A non-string matcher would be coerced to "" by asStr and captured as a
			// match-all — refuse the event rather than silently rewrite it.
			if rawMatcher, present := entry["matcher"]; present {
				if _, isStr := rawMatcher.(string); !isStr {
					fmt.Fprintf(warn, "warning: hook event %q has a definition whose \"matcher\" is not a string; event not captured\n", geminiEvent)
					representable = false
					structural = true
					break
				}
			}
			matcher := asStr(entry["matcher"])
			// A definition must carry a "hooks" ARRAY of handlers — see the claude
			// twin for why an absent/invalid one refuses the whole event instead of
			// silently contributing zero handlers. An empty array is fine.
			hooksArr, isArr := entry["hooks"].([]any)
			if !isArr {
				fmt.Fprintf(warn, "warning: hook event %q has a definition without a valid \"hooks\" array; event not captured\n", geminiEvent)
				representable = false
				structural = true
				break
			}
			for _, rawH := range hooksArr {
				h, ok := rawH.(map[string]any)
				if !ok {
					fmt.Fprintf(warn, "warning: hook event %q has a malformed handler (not an object); event not captured\n", geminiEvent)
					representable = false
					structural = true
					break defs
				}
				// A non-string type would be coerced to "" by asStr and captured as a
				// command handler; refuse the malformed shape instead.
				if rawType, present := h["type"]; present {
					if _, isStr := rawType.(string); !isStr {
						fmt.Fprintf(warn, "warning: hook event %q has a handler whose \"type\" is not a string; event not captured\n", geminiEvent)
						representable = false
						structural = true
						break defs
					}
				}
				if typ := asStr(h["type"]); typ != "" && typ != "command" {
					fmt.Fprintf(warn, "warning: hook event %q has a %q-type handler agentsync cannot represent; event not captured\n", geminiEvent, typ)
					representable = false
					break defs
				}
				if extra := adapter.UnmodeledKeys(h, geminiHookEntryModeledKeys); len(extra) > 0 {
					fmt.Fprintf(warn, "warning: hook event %q has a handler with unmodeled fields (%s); event not captured\n", geminiEvent, adapter.QuotedKeys(extra))
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
				// handler. Checked AFTER the type and unmodeled-keys checks so it governs
				// only fully-modeled command-type handlers: a semantically-refusable
				// handler (prompt-type, or carrying an unmodeled field) must keep its
				// retirement-triggering refusal above.
				if rawCmd, present := h["command"]; !present {
					fmt.Fprintf(warn, "warning: hook event %q has a handler without a \"command\"; event not captured\n", geminiEvent)
					representable = false
					structural = true
					break defs
				} else if _, isStr := rawCmd.(string); !isStr {
					fmt.Fprintf(warn, "warning: hook event %q has a handler whose \"command\" is not a string; event not captured\n", geminiEvent)
					representable = false
					structural = true
					break defs
				}
				captured = append(captured, source.Hook{
					Event:   untrusted.Wrap(canonEvent), // remapped from native config
					Matcher: matcher,
					Type:    asStr(h["type"]),
					Command: asStr(h["command"]),
				})
			}
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
// for the shared contract and ingestHooks for what gemini refuses. Gemini leg:
// settings.json is re-read as JSONC (jsonkeys.DecodeJSONC), exactly as Ingest
// parses it; refused events surface under their CANONICAL names (BeforeTool →
// PreToolUse), and Gemini-only events (BeforeModel, …) never appear — no
// canonical file exists to retire. Warnings are discarded here; Ingest already
// emitted them on the same shapes.
func (a *Adapter) RefusedHookEvents(scope adapter.Scope, project string) ([]string, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	data, present, err := adapter.ReadFileOptional(p.Settings)
	if err != nil || !present {
		return nil, err
	}
	top, err := jsonkeys.DecodeJSONC(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", p.Settings, err)
	}
	_, refused := ingestHooks(top["hooks"], io.Discard)
	return refused, nil
}

// NativeHookEvent implements adapter.HookEventNamer: Gemini renames canonical
// hook events in settings.json (PreToolUse -> BeforeTool, …), so import's
// stale-hook retirement must disown gemini's "/hooks/<native>" state key under
// the NATIVE spelling — matching only the canonical name would leave the key
// owned with no canonical render, and the next apply's orphan cleanup would
// delete the event from the user's settings.json.
func (a *Adapter) NativeHookEvent(canonical string) (string, bool) {
	native, ok := canonicalToGeminiHookEvent[canonical]
	return native, ok
}
