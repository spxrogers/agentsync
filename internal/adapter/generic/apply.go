package generic

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// Apply routes every destination write through the supplied DestWriter rather
// than calling iox.AtomicWrite directly. The DestWriter owns the
// foreign-collision backup invariant — see the doc on adapter.DestWriter.
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

// applyWrite merges merge-json-keys ops (the mcpServers file, preserving foreign
// servers) and writes everything else (the memory file) whole.
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
	body, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged for %s: %w", op.Path, err)
	}
	return w.Write(op, append(body, '\n'))
}

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
