package gemini

import (
	"errors"
	"fmt"
	"io/fs"
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

// applyWrite performs the per-op merge (for merge-jsonc-keys) and hands the
// post-merge bytes to the writer. Gemini's single key-merge destination is
// settings.json (both `mcpServers` and `hooks`); everything else is a whole-file
// replace. Gemini CLI itself reads settings.json as JSONC (comments are a
// supported, documented state of the file), so the merge MUST be JSONC-tolerant:
// parsing a commented file as strict JSON would treat it as empty and clobber
// every foreign key the user set. jsonkeys.MergeJSONC parses leniently and
// refuses (errors) on genuinely unparseable content rather than merging against
// an empty map. The writer compares pre-merge op.Content against the destination
// for per-key collision detection, so we pass the raw FileOp along.
func (a *Adapter) applyWrite(op adapter.FileOp, w adapter.DestWriter) error {
	if op.MergeStrategy != "merge-jsonc-keys" {
		return w.Write(op, op.Content)
	}
	existing, err := os.ReadFile(op.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", op.Path, err)
	}
	ours, err := jsonkeys.DecodeObject(op.Content)
	if err != nil {
		return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
	}
	body, err := jsonkeys.MergeJSONC(existing, ours, op.OwnedKeys)
	if err != nil {
		return fmt.Errorf("merge %s: %w", op.Path, err)
	}
	return w.Write(op, body)
}
