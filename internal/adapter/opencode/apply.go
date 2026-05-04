package opencode

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/iox"
)

func (a *Adapter) Apply(ops []adapter.FileOp) error {
	for _, op := range ops {
		switch op.Action {
		case "delete":
			if err := os.Remove(op.Path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("delete %s: %w", op.Path, err)
			}
		case "", "write":
			if err := a.applyWrite(op); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown action %q", op.Action)
		}
	}
	return nil
}

func (a *Adapter) applyWrite(op adapter.FileOp) error {
	mode := os.FileMode(op.Mode)
	if mode == 0 {
		mode = 0o644
	}
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
		return iox.AtomicWrite(op.Path, out, mode)
	}
	return iox.AtomicWrite(op.Path, op.Content, mode)
}
