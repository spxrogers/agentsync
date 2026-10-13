package generic

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// Apply routes every destination write through the supplied DestWriter rather
// than calling iox.AtomicWrite directly. The DestWriter owns the
// foreign-collision backup invariant — see the doc on adapter.DestWriter.
func (a *Adapter) Apply(ops []adapter.FileOp, w adapter.DestWriter) error {
	return adapter.DispatchOps(ops, w, func(op adapter.FileOp) error {
		return a.applyWrite(op, w)
	})
}

// applyWrite merges the mcpServers file (merge-jsonc-keys) and writes everything
// else (the memory file) whole. The merge is JSONC-tolerant via the shared
// jsonkeys.MergeJSONC engine (also behind OpenCode's opencode.json and Gemini's
// settings.json): a hand-edited settings file with comments/trailing commas
// (Zed, Copilot, Amp) is parsed and its foreign keys preserved rather than
// clobbered; the rewritten file is re-emitted as plain JSON — comments are
// stripped, a documented Known limit. Genuinely unparseable content refuses the
// merge instead of being treated as empty.
func (a *Adapter) applyWrite(op adapter.FileOp, w adapter.DestWriter) error {
	if op.MergeStrategy != "merge-jsonc-keys" {
		return w.Write(op, op.Content)
	}
	// ReadFileOptional rather than ReadExisting: the seed below needs to know
	// the destination is ABSENT, and ReadExisting folds absent into a nil
	// error. Testing errors.Is(err, fs.ErrNotExist) after it would never fire.
	existing, present, err := adapter.ReadFileOptional(op.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", op.Path, err)
	}
	var seeded map[string]any
	if !present {
		// Droid reads settings.json "hooks" only while hooks.json is absent,
		// then ignores that key once hooks.json exists. Seed the new file from
		// that object so a first apply does not drop events this render does
		// not own (a commandRegex group, a sibling Stop, timeout 0). The seed
		// is the merge base, not op.Content, so RecordOpsState does not record
		// those pointers as owned and a later apply cannot orphan-delete them.
		existing, seeded = a.factoryHooksSeed(op.Path)
	}
	if a.isFactoryHooksFile(op.Path) {
		a.warnFactoryLegacyHooks(op.Path)
	}
	ours, err := jsonkeys.DecodeObject(op.Content)
	if err != nil {
		return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
	}
	a.warnSeededEventsReplaced(op.Path, seeded, ours)
	merged, err := jsonkeys.MergeJSONC(existing, ours, op.OwnedKeys)
	if err != nil {
		return fmt.Errorf("merge %s: %w", op.Path, err)
	}
	return w.Write(op, merged)
}

func (a *Adapter) isFactoryHooksFile(path string) bool {
	if a.spec.Hooks.User == "" && a.spec.Hooks.Project == "" {
		return false
	}
	return filepath.Base(path) == "hooks.json"
}

// factoryHooksSeed returns the settings.json "hooks" object twice: encoded as
// the merge base for a new hooks.json, and decoded so applyWrite can tell the
// user which of those events the render replaces. Both are nil when there is
// nothing to preserve. A missing or unreadable settings file is not an apply
// failure: the hooks.json write still proceeds, and a read or shape problem is
// warned rather than blocking every other component.
func (a *Adapter) factoryHooksSeed(hooksPath string) ([]byte, map[string]any) {
	if !a.isFactoryHooksFile(hooksPath) {
		return nil, nil
	}
	settings := filepath.Join(filepath.Dir(hooksPath), "settings.json")
	// Through ReadFileOptional like every other destination read (#241/#242):
	// a FIFO at settings.json must not hang apply in open(2).
	data, present, err := adapter.ReadFileOptional(settings)
	if err != nil {
		fmt.Fprintf(a.warn(), "warning: could not read %s to preserve its hooks in hooks.json: %v\n", settings, err)
		return nil, nil
	}
	if !present {
		return nil, nil
	}
	doc, err := jsonkeys.DecodeJSONC(data)
	if err != nil {
		fmt.Fprintf(a.warn(), "warning: could not parse %s to preserve its hooks in hooks.json: %v\n", settings, err)
		return nil, nil
	}
	rawHooks, hasHooks := doc["hooks"]
	if !hasHooks {
		return nil, nil
	}
	hooks, ok := rawHooks.(map[string]any)
	if !ok {
		// Import warns about this shape too; staying silent here would let
		// apply create hooks.json, after which Droid never reads that key again.
		fmt.Fprintf(a.warn(), "warning: %s hooks value is not an object; nothing from it is carried into hooks.json\n", settings)
		return nil, nil
	}
	if len(hooks) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(hooks)
	if err != nil {
		return nil, nil
	}
	return body, hooks
}

// warnSeededEventsReplaced reports every settings.json event whose handlers
// stop running because this render owns that event in the new hooks.json.
//
// The seed keeps events agentsync does not render, but an event it DOES render
// is replaced by the canonical array. Nothing is deleted (settings.json is
// left as it was), yet Droid stops reading that key once hooks.json exists, so
// without this the user's handler would simply stop firing.
//
// The comparison is semantic, through the same parser import uses, not a byte
// compare. The common path is `agentsync import factory` (which reads the
// settings.json fallback) followed by `apply`; byte-comparing would warn there
// because a settings.json handler written without "matcher" or "type"
// re-renders with both. A seeded event agentsync cannot represent (a
// commandRegex group, say) never equals a rendered one, so it always warns.
func (a *Adapter) warnSeededEventsReplaced(hooksPath string, seeded, ours map[string]any) {
	if len(seeded) == 0 {
		return
	}
	events := make([]string, 0, len(ours))
	for event := range ours {
		if _, ok := seeded[event]; ok {
			events = append(events, event)
		}
	}
	sort.Strings(events)
	settings := filepath.Join(filepath.Dir(hooksPath), "settings.json")
	for _, event := range events {
		if sameFactoryEvent(event, seeded[event], ours[event]) {
			continue
		}
		fmt.Fprintf(a.warn(), "warning: the %q hooks in %s will stop running: agentsync now writes %q to %s, "+
			"which Droid reads instead of settings.json. Add them to hooks/%s.toml to keep them\n",
			event, settings, event, hooksPath, event)
	}
}

// sameFactoryEvent reports whether two native values for one event describe
// the same handlers once parsed the way import parses them.
func sameFactoryEvent(event string, native, rendered any) bool {
	nativeHooks, nativeRefused := parseFactoryHooks(map[string]any{event: native}, io.Discard)
	if len(nativeRefused) > 0 {
		return false
	}
	renderedHooks, _ := parseFactoryHooks(map[string]any{event: rendered}, io.Discard)
	return len(nativeHooks) > 0 && reflect.DeepEqual(nativeHooks, renderedHooks)
}

func (a *Adapter) warnFactoryLegacyHooks(hooksPath string) {
	legacy := filepath.Join(filepath.Dir(hooksPath), "hooks", "hooks.json")
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	fmt.Fprintf(a.warn(), "warning: %s still loads after hooks.json exists; Droid archives it to hooks/hooks.migrated.json on the next save in Droid\n", legacy)
}
