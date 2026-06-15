package generic

import (
	"errors"
	"fmt"
	"io/fs"
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

// applyWrite merges the mcpServers file (merge-jsonc-keys) and writes everything
// else (the memory file) whole. The merge is JSONC-tolerant via the shared
// jsonkeys.MergeJSONC engine (also behind OpenCode's opencode.json and Gemini's
// settings.json): a hand-edited settings file with comments/trailing commas
// (Zed, Copilot, Amp) is parsed and its foreign keys preserved rather than
// clobbered; the rewritten file is re-emitted as plain JSON — comments are
// stripped, a documented Known limit. Genuinely unparseable content refuses the
// merge instead of being treated as empty.
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
	merged, err := jsonkeys.MergeJSONC(existing, ours, op.OwnedKeys)
	if err != nil {
		return fmt.Errorf("merge %s: %w", op.Path, err)
	}
	return w.Write(op, merged)
}
