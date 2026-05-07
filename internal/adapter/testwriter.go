package adapter

import (
	"os"

	"github.com/spxrogers/agentsync/internal/iox"
)

// PassThroughWriter is a DestWriter that performs writes via iox.AtomicWrite
// with no foreign-collision backup. It exists so adapter unit tests can
// exercise their Apply/applyWrite branches without standing up a full
// render.Writer + state.Targets — the production render.Writer wraps the
// same iox primitive but adds the backup decision.
//
// It is exported (not in *_test.go) so tests across the adapter/* tree
// can share it; production code must NOT use it. The forbidigo lint
// rule in .golangci.yml blocks any direct iox.AtomicWrite call outside
// the writer, but this helper is the explicit non-production exception.
type PassThroughWriter struct{}

// Write calls iox.AtomicWrite with the supplied final bytes.
func (PassThroughWriter) Write(op FileOp, finalBytes []byte) error {
	mode := os.FileMode(op.Mode)
	if mode == 0 {
		mode = 0o644
	}
	return iox.AtomicWrite(op.Path, finalBytes, mode)
}

// Delete removes op.Path. Idempotent on missing files.
func (PassThroughWriter) Delete(op FileOp) error {
	if err := os.Remove(op.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
