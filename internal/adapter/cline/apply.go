package cline

import (
	"encoding/json"
	"fmt"

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

// applyWrite performs the per-op merge (for the merge-json-keys ~/.cline/mcp.json)
// and hands post-merge bytes to the writer; rules/workflows are whole-file replace.
func (a *Adapter) applyWrite(op adapter.FileOp, w adapter.DestWriter) error {
	if op.MergeStrategy != "merge-json-keys" {
		return w.Write(op, op.Content)
	}
	existing, err := readJSONFile(op.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", op.Path, err)
	}
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

// readJSONFile reads and decodes a JSON object file, returning an empty map on
// any read/parse error. Decode preserves json.Number so a foreign integer > 2^53
// isn't rounded when the merged file is re-marshalled.
func readJSONFile(path string) (map[string]any, error) {
	data, err := adapter.ReadExisting(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	m, err := jsonkeys.DecodeObject(data)
	if err != nil {
		return map[string]any{}, nil
	}
	return m, nil
}
