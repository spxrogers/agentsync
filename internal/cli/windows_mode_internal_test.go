package cli

import (
	"runtime"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/drift"
)

func TestHostPermissionDrift(t *testing.T) {
	i := planItem{op: adapter.FileOp{Mode: 0o644}, destPerm: 0o666, destRegular: true, cls: drift.Clean}
	want := drift.Drift
	if runtime.GOOS == "windows" {
		want = drift.Clean
	}
	if got := i.classWithModeDrift(); got != want {
		t.Fatalf("0666 vs 0644: %v, want %v", got, want)
	}
	i.destPerm = 0o444
	if i.classWithModeDrift() != drift.Drift {
		t.Fatal("read-only mismatch must remain drift")
	}
	i.op.Mode = 0
	if i.opModeDrifted() {
		t.Fatal("unspecified mode must not drift")
	}
	i.op.Mode = 0o644
	i.destRegular = false
	if i.opModeDrifted() {
		t.Fatal("absent destination must use content classification")
	}
}
