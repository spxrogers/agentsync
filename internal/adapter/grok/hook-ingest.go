package grok

import (
	"fmt"
	"io"
	"slices"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

var (
	hookDefModeledKeys   = map[string]bool{"matcher": true, "hooks": true}
	hookEntryModeledKeys = map[string]bool{"type": true, "command": true, "timeout": true}
)

// ingestHooks captures only whole events representable by source.Hook. Native
// fields such as failClosed and HTTP handlers refuse the event, allowing import to
// retire stale canonical hooks. Timeout is modeled. Malformed shapes only warn and never retire.
// The first invalid definition or handler determines the refusal classification.
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
			if extra := adapter.UnmodeledKeys(entry, hookDefModeledKeys); len(extra) > 0 {
				fmt.Fprintf(warn, "warning: hook event %q has a definition with unmodeled fields (%s); event not captured\n", event, adapter.QuotedKeys(extra))
				representable = false
				break
			}
			if rawMatcher, present := entry["matcher"]; present {
				if _, isStr := rawMatcher.(string); !isStr {
					fmt.Fprintf(warn, "warning: hook event %q has a definition whose \"matcher\" is not a string; event not captured\n", event)
					representable = false
					structural = true
					break
				}
			}
			matcher := asStr(entry["matcher"])
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
				if extra := adapter.UnmodeledKeys(h, hookEntryModeledKeys); len(extra) > 0 {
					fmt.Fprintf(warn, "warning: hook event %q has a handler with unmodeled fields (%s); event not captured\n", event, adapter.QuotedKeys(extra))
					representable = false
					break defs
				}
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
				timeout, tok, timeoutStructural := adapter.ParseHookTimeout(h)
				if !tok {
					fmt.Fprintf(warn, "warning: hook event %q has a handler whose \"timeout\" is not an integer; event not captured\n", event)
					representable = false
					structural = timeoutStructural
					break defs
				}
				captured = append(captured, source.Hook{
					Event:   untrusted.Wrap(event), // native hooks JSON map key
					Matcher: matcher,
					Type:    asStr(h["type"]),
					Command: asStr(h["command"]),
					Timeout: timeout,
				})
			}
		}
		if representable {
			out = append(out, captured...)
		} else if !structural {
			refused = append(refused, event)
		}
	}
	slices.Sort(refused) // Keep output deterministic despite map iteration order.
	return out, refused
}

func asStr(v any) string { s, _ := v.(string); return s }
