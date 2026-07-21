package continuedev

import (
	"github.com/spxrogers/agentsync/internal/adapter"
)

// Apply routes every destination write through the supplied DestWriter rather
// than calling iox.AtomicWrite directly. Continue projects every component as a
// whole-file write (one block per file), so there is no per-key merge: a write is
// the op's content verbatim. The DestWriter owns the foreign-collision backup
// invariant — see the doc on adapter.DestWriter.
func (a *Adapter) Apply(ops []adapter.FileOp, w adapter.DestWriter) error {
	return adapter.DispatchOps(ops, w, func(op adapter.FileOp) error {
		return w.Write(op, op.Content)
	})
}
