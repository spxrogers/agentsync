package continuedev

import (
	"fmt"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// Apply routes every destination write through the supplied DestWriter rather
// than calling iox.AtomicWrite directly. Continue projects every component as a
// whole-file write (one block per file), so there is no per-key merge: a write is
// the op's content verbatim. The DestWriter owns the foreign-collision backup
// invariant — see the doc on adapter.DestWriter.
func (a *Adapter) Apply(ops []adapter.FileOp, w adapter.DestWriter) error {
	for _, op := range ops {
		switch op.Action {
		case "delete":
			if err := w.Delete(op); err != nil {
				return fmt.Errorf("delete %s: %w", op.Path, err)
			}
		case "", "write":
			if err := w.Write(op, op.Content); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown action %q", op.Action)
		}
	}
	return nil
}
