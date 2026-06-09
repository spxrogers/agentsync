package cursor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// Apply routes every destination write through the supplied DestWriter rather
// than calling iox.AtomicWrite directly. This is the contract that keeps the
// foreign-collision backup guarantee honest — see the doc on adapter.DestWriter.
func (a *Adapter) Apply(ops []adapter.FileOp, w adapter.DestWriter) error {
	for _, op := range ops {
		switch op.Action {
		case "delete":
			if err := w.Delete(op); err != nil {
				return fmt.Errorf("delete %s: %w", op.Path, err)
			}
		case "", "write":
			if err := a.applyWrite(op, w); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown action %q", op.Action)
		}
	}
	return nil
}

// applyWrite performs the per-op merge (for merge-json-keys) and hands the
// post-merge bytes to the writer. Cursor has two key-merge JSON destinations —
// `.cursor/mcp.json` and `.cursor/hooks.json` — and the writer compares pre-merge
// op.Content against the destination for per-key collision detection, so we pass
// the raw FileOp along.
func (a *Adapter) applyWrite(op adapter.FileOp, w adapter.DestWriter) error {
	if op.MergeStrategy != "merge-json-keys" {
		return w.Write(op, op.Content)
	}
	existing := readJSONFile(op.Path)
	ours, err := jsonkeys.DecodeObject(op.Content)
	if err != nil {
		return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
	}
	merged, _, _ := jsonkeys.MergeKeys(existing, ours, op.OwnedKeys)
	// Cursor's `.cursor/hooks.json` REQUIRES a top-level integer `version` field
	// (Cursor 3.x refuses to load ANY hooks from a file missing it). We
	// (re)assert it here, AFTER the key-merge, rather than render it into
	// op.Content: a top-level scalar in op.Content would be recorded as an owned
	// key (CollectPointers yields "/version") and then stripped by the render
	// pipeline's orphan-cleanup when the last canonical hook is removed —
	// silently breaking a user's own foreign hook events left in the same file.
	// Injecting it post-merge keeps version always present and never owned, even
	// on the synthesized empty-`{}` orphan-cleanup op (which also runs through
	// here for the hooks path).
	if filepath.Base(op.Path) == hooksFileName {
		merged["version"] = cursorHooksVersion
	}
	body, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged for %s: %w", op.Path, err)
	}
	return w.Write(op, append(body, '\n'))
}

// readJSONFile reads and decodes a JSON object file, returning an empty map on
// any read/parse error. Decode preserves json.Number so a foreign integer > 2^53
// isn't rounded when the merged file is re-marshalled.
func readJSONFile(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	m, err := jsonkeys.DecodeObject(data)
	if err != nil {
		return map[string]any{}
	}
	return m
}
