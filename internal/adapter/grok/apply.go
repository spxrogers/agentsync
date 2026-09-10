package grok

import (
	"encoding/json"
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// Apply uses the existing merge engines; DestWriter owns backups and all writes.
func (a *Adapter) Apply(ops []adapter.FileOp, w adapter.DestWriter) error {
	return adapter.DispatchOps(ops, w, func(op adapter.FileOp) error {
		if op.MergeStrategy == "" || op.MergeStrategy == "replace" {
			return w.Write(op, op.Content)
		}
		existing, _, err := adapter.ReadFileOptional(op.Path)
		if err != nil {
			return fmt.Errorf("read %s: %w", op.Path, err)
		}
		ours, err := jsonkeys.DecodeObject(op.Content)
		if err != nil {
			return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
		}
		var out []byte
		switch op.MergeStrategy {
		case "merge-toml-keys":
			// go-toml treats json.Number as a string; preserve native numeric types.
			jsonkeys.ConvertNumbers(ours)
			out, err = codex.MergeTOML(existing, ours, op.OwnedKeys)
		case "merge-json-keys":
			top := map[string]any{}
			if len(existing) > 0 {
				top, err = jsonkeys.DecodeObject(existing)
			}
			if err != nil {
				return fmt.Errorf("parse %s: %w", op.Path, err)
			}
			merged, _, _ := jsonkeys.MergeKeys(top, ours, op.OwnedKeys)
			out, err = json.MarshalIndent(merged, "", "  ")
			out = append(out, '\n')
		default:
			return fmt.Errorf("unsupported Grok merge strategy %q", op.MergeStrategy)
		}
		if err != nil {
			return err
		}
		return w.Write(op, out)
	})
}
