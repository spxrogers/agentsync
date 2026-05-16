package opencode

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// Apply routes every destination write through the supplied DestWriter
// rather than calling iox.AtomicWrite directly. This is the contract that
// keeps the foreign-collision backup guarantee honest — see the doc on
// adapter.DestWriter.
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

func (a *Adapter) applyWrite(op adapter.FileOp, w adapter.DestWriter) error {
	if op.MergeStrategy == "merge-jsonc-keys" {
		existing, _ := os.ReadFile(op.Path)
		var ours map[string]any
		if err := json.Unmarshal(op.Content, &ours); err != nil {
			return fmt.Errorf("parse our payload: %w", err)
		}
		out, err := MergeJSONC(existing, ours, op.OwnedKeys)
		if err != nil {
			return err
		}
		return w.Write(op, out)
	}
	return w.Write(op, op.Content)
}
