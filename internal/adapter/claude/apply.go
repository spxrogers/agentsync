package claude

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// Apply executes ops against Claude's native destinations. All writes
// route through the supplied DestWriter; we never call iox.AtomicWrite
// or os.Remove directly here. The DestWriter owns the foreign-collision
// backup invariant.
func (a *Adapter) Apply(ops []adapter.FileOp, w adapter.DestWriter) error {
	return adapter.DispatchOps(ops, w, func(op adapter.FileOp) error {
		return a.applyWrite(op, w)
	})
}

// applyWrite performs the per-op merge (for merge-json-keys) and hands
// the post-merge bytes to the writer. The writer compares pre-merge
// `op.Content` against the destination for per-key collision detection,
// so we still pass the raw FileOp (carrying op.Content) along.
func (a *Adapter) applyWrite(op adapter.FileOp, w adapter.DestWriter) error {
	if op.MergeStrategy == "merge-json-keys" {
		existing, err := readJSONFile(op.Path)
		if err != nil {
			return fmt.Errorf("read %s: %w", op.Path, err)
		}
		ours, err := jsonkeys.DecodeObject(op.Content)
		if err != nil {
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

func readJSONFile(path string) (map[string]any, error) {
	data, err := adapter.ReadExisting(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	// Decode preserving json.Number so a foreign integer > 2^53 isn't rounded
	// when the merged file is re-marshalled.
	m, err := jsonkeys.DecodeObject(data)
	if err != nil {
		return map[string]any{}, nil
	}
	return m, nil
}
