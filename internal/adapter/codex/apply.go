package codex

import (
	"fmt"
	"os"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// Apply routes every destination write through the supplied DestWriter rather
// than calling iox.AtomicWrite directly. This is the contract that keeps the
// foreign-collision backup guarantee honest — see the doc on adapter.DestWriter.
func (a *Adapter) Apply(ops []adapter.FileOp, w adapter.DestWriter) error {
	return adapter.DispatchOps(ops, w, func(op adapter.FileOp) error {
		return a.applyWrite(op, w)
	})
}

// applyWrite performs the per-op merge and hands post-merge bytes to the writer.
// Codex has a single key-merge destination: ~/.codex/config.toml (both
// [mcp_servers.*] and [hooks.*]), so the only merge strategy is merge-toml-keys;
// everything else is a whole-file replace. op.Content is always JSON (the
// pipeline's pointer-merge currency) even though the file is TOML. The writer
// compares pre-merge op.Content against the destination for collision detection,
// so we still pass the raw FileOp.
func (a *Adapter) applyWrite(op adapter.FileOp, w adapter.DestWriter) error {
	if op.MergeStrategy != "merge-toml-keys" {
		return w.Write(op, op.Content)
	}
	// A read error other than not-exist (e.g. a permission problem) must NOT be
	// coerced to "empty file": MergeTOML would then merge our section into an
	// empty map and the write would silently drop the user's foreign config.toml
	// keys. Fail loud instead.
	existing, err := os.ReadFile(op.Path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", op.Path, err)
	}
	ours, err := jsonkeys.DecodeObject(op.Content)
	if err != nil {
		return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
	}
	out, err := MergeTOML(existing, ours, op.OwnedKeys)
	if err != nil {
		return err
	}
	return w.Write(op, out)
}
