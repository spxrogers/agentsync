package generic

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/tailscale/hujson"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// readFile returns the raw bytes at path, or nil on any read error.
func readFile(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

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

// applyWrite merges the mcpServers file (merge-jsonc-keys) and writes everything
// else (the memory file) whole. The merge is JSONC-tolerant: a hand-edited
// settings file with comments/trailing commas (Zed, Copilot, Amp) is parsed and
// its foreign keys preserved, rather than failing to parse and being clobbered.
// (Like OpenCode's MergeJSONC, the rewritten file is re-emitted as plain JSON —
// comments are not preserved; a documented v1 limit.)
func (a *Adapter) applyWrite(op adapter.FileOp, w adapter.DestWriter) error {
	if op.MergeStrategy != "merge-jsonc-keys" {
		return w.Write(op, op.Content)
	}
	ours, err := jsonkeys.DecodeObject(op.Content)
	if err != nil {
		return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
	}
	merged, err := mergeJSONC(readFile(op.Path), ours, op.OwnedKeys)
	if err != nil {
		return fmt.Errorf("merge %s: %w", op.Path, err)
	}
	return w.Write(op, merged)
}

// mergeJSONC parses existing JSONC (tolerating comments + trailing commas),
// merges ours by owned pointer, and re-emits plain JSON. Empty/missing input is
// treated as an empty object.
func mergeJSONC(existing []byte, ours map[string]any, ownedPointers []string) ([]byte, error) {
	if len(existing) == 0 {
		existing = []byte("{}")
	}
	val, err := hujson.Parse(existing)
	if err != nil {
		return nil, fmt.Errorf("parse jsonc: %w", err)
	}
	val.Standardize()
	existingMap, err := jsonkeys.DecodeObject(val.Pack())
	if err != nil {
		return nil, fmt.Errorf("standardize jsonc: %w", err)
	}
	merged, _, _ := jsonkeys.MergeKeys(existingMap, ours, ownedPointers)
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal merged: %w", err)
	}
	return append(out, '\n'), nil
}
