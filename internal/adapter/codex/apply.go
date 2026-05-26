package codex

import (
	"encoding/json"
	"fmt"
	"os"

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

// applyWrite performs the per-op merge and hands post-merge bytes to the writer.
// op.Content is always JSON (the pipeline's pointer-merge currency); the
// destination FILE is TOML for config.toml (merge-toml-keys) and JSON for
// hooks.json (merge-json-keys). The writer compares pre-merge op.Content against
// the destination for collision detection, so we still pass the raw FileOp.
func (a *Adapter) applyWrite(op adapter.FileOp, w adapter.DestWriter) error {
	switch op.MergeStrategy {
	case "merge-toml-keys":
		existing, _ := os.ReadFile(op.Path)
		ours, err := jsonkeys.DecodeObject(op.Content)
		if err != nil {
			return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
		}
		out, err := MergeTOML(existing, ours, op.OwnedKeys)
		if err != nil {
			return err
		}
		return w.Write(op, out)
	case "merge-json-keys":
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
	default:
		return w.Write(op, op.Content)
	}
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
