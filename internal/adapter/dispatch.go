package adapter

import "fmt"

// DispatchOps runs the standard write/delete dispatch over ops: it calls
// w.Delete for each "delete" action and writeOp for each "" / "write" action
// (the empty string is treated as "write"), and returns
// fmt.Errorf("unknown action %q", op.Action) for anything else. A delete error
// is wrapped as fmt.Errorf("delete %s: %w", op.Path, err); a writeOp error is
// returned as-is (writeOp owns its own wrapping).
//
// Every adapter's Apply (except noop's trivial no-op) delegates here so this
// dispatch — and its exact error strings — lives in exactly one place. The only
// per-adapter variation is the writeOp closure: most adapters pass
// a.applyWrite(op, w) (a per-op key-merge then w.Write); continuedev, which has
// no merge, passes w.Write(op, op.Content) directly. writeOp MUST route through
// the DestWriter — DispatchOps never touches the filesystem itself, preserving
// the DestWriter foreign-collision backup invariant.
func DispatchOps(ops []FileOp, w DestWriter, writeOp func(FileOp) error) error {
	for _, op := range ops {
		switch op.Action {
		case "delete":
			if err := w.Delete(op); err != nil {
				return fmt.Errorf("delete %s: %w", op.Path, err)
			}
		case "", "write":
			if err := writeOp(op); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown action %q", op.Action)
		}
	}
	return nil
}
