package opencode

import (
	"fmt"
	"os"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// Apply routes every destination write through the supplied DestWriter
// rather than calling iox.AtomicWrite directly. This is the contract that
// keeps the foreign-collision backup guarantee honest — see the doc on
// adapter.DestWriter.
func (a *Adapter) Apply(ops []adapter.FileOp, w adapter.DestWriter) error {
	return adapter.DispatchOps(ops, w, func(op adapter.FileOp) error {
		return a.applyWrite(op, w)
	})
}

func (a *Adapter) applyWrite(op adapter.FileOp, w adapter.DestWriter) error {
	if op.MergeStrategy == "merge-jsonc-keys" {
		existing, _ := os.ReadFile(op.Path)
		ours, err := jsonkeys.DecodeObject(op.Content)
		if err != nil {
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
