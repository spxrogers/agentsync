package claude

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// Apply executes ops against Claude's native destinations. All writes
// route through the supplied DestWriter; we never call iox.AtomicWrite
// or os.Remove directly here. The DestWriter owns the foreign-collision
// backup invariant.
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

// applyWrite performs the per-op merge (for merge-json-keys) and hands
// the post-merge bytes to the writer. The writer compares pre-merge
// `op.Content` against the destination for per-key collision detection,
// so we still pass the raw FileOp (carrying op.Content) along.
func (a *Adapter) applyWrite(op adapter.FileOp, w adapter.DestWriter) error {
	if op.MergeStrategy == "merge-json-keys" {
		existing := readJSONFile(op.Path)
		var ours map[string]any
		if err := json.Unmarshal(op.Content, &ours); err != nil {
			return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
		}
		merged, _, _ := MergeKeys(existing, ours, op.OwnedKeys)
		body, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal merged for %s: %w", op.Path, err)
		}
		return w.Write(op, append(body, '\n'))
	}
	return w.Write(op, op.Content)
}

func readJSONFile(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	if m == nil {
		return map[string]any{}
	}
	return m
}
