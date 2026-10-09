package generic

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
	if !present {
		// Droid reads settings.json "hooks" only while hooks.json is absent,
		// then ignores that key once hooks.json exists. Seed the new file from
		// that object so a first apply does not drop events this render does
		// not own (a commandRegex group, a sibling Stop, timeout 0). The seed
		// is the merge base, not op.Content, so RecordOpsState does not record
		// those pointers as owned and a later apply cannot orphan-delete them.
		existing = a.factoryHooksSeed(op.Path)
	}
	if a.isFactoryHooksFile(op.Path) {
		a.warnFactoryLegacyHooks(op.Path)
	}
	ours, err := jsonkeys.DecodeObject(op.Content)
	if err != nil {
		return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
	}
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

// factoryHooksSeed is the settings.json "hooks" object, as JSON, or nil when
// there is nothing to preserve. A missing or unreadable settings file is not
// an apply failure: the hooks.json write still proceeds, and a parse problem
// is warned rather than blocking every other component.
func (a *Adapter) factoryHooksSeed(hooksPath string) []byte {
	if !a.isFactoryHooksFile(hooksPath) {
		return nil
	}
	settings := filepath.Join(filepath.Dir(hooksPath), "settings.json")
	// Through ReadFileOptional like every other destination read (#241/#242):
	// a FIFO at settings.json must not hang apply in open(2).
	data, present, err := adapter.ReadFileOptional(settings)
	if err != nil {
		fmt.Fprintf(a.warn(), "warning: could not read %s to preserve its hooks in hooks.json: %v\n", settings, err)
		return nil
	}
	if !present {
		return nil
	}
	doc, err := jsonkeys.DecodeJSONC(data)
	if err != nil {
		fmt.Fprintf(a.warn(), "warning: could not parse %s to preserve its hooks in hooks.json: %v\n", settings, err)
		return nil
	}
	raw, ok := doc["hooks"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	return body
}

func (a *Adapter) warnFactoryLegacyHooks(hooksPath string) {
	legacy := filepath.Join(filepath.Dir(hooksPath), "hooks", "hooks.json")
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	fmt.Fprintf(a.warn(), "warning: %s still loads after hooks.json exists; Droid archives it to hooks/hooks.migrated.json on the next save in Droid\n", legacy)
}
