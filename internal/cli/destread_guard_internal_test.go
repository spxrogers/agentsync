package cli

import (
	"strings"
	"testing"
)

// TestEveryDestinationReadGoesThroughTheGate enforces the invariant that makes
// the FIFO guard hold: no production code in this package reads a destination
// path with a bare os.ReadFile.
//
// Why an invariant and not just the behavior tests: the four reads that were
// MEASURED to hang (readDestFile, diff's and reconcile's per-op reads, and
// writeBackFileItem) are each pinned by a timeout-bounded test. The three in
// import's state-seeding path are not — no fixture was found that reaches them
// with a non-regular destination, and with every guard removed `agentsync
// import` still returned. They were routed through the gate anyway, because
// three structurally identical unguarded reads sitting beside four guarded ones
// invite exactly the question "why those and not these", and the honest answer
// — "nobody found a fixture yet" — is not a reason to leave them. This test is
// what keeps that decision from silently rotting: a new bare destination read
// fails here even where no hang can be demonstrated.
//
// LIMITS, stated so a reader does not over-trust it:
//   - The pattern is TEXT-shaped. A read spelled through a variable
//     (`p := op.Path; os.ReadFile(p)`), via afero, or with os.Open + io.ReadAll
//     slips past. It catches the copy-paste that actually happened seven times,
//     not every conceivable spelling.
//   - It scans comments and string literals too, so a mention of the pattern in
//     prose would trip it. That is the safe direction to fail.
func TestEveryDestinationReadGoesThroughTheGate(t *testing.T) {
	// The forbidden spellings: a bare read of an op's destination path.
	forbidden := []string{
		"os.ReadFile(op.Path)",
		"os.ReadFile(it.op.Path)",
	}

	repoRoot := repoRootFromCaller(t)
	scanned := 0
	if err := walkRepoGoFiles(repoRoot, func(rel, src string) {
		// Scoped to this package deliberately. internal/render and the adapter
		// Apply paths also read op.Path, but those are the WRITE path, with
		// their own upstream shape handling (see render.isRegularOrAbsent's doc
		// comment, which names apply's pre-delete read). They were not audited
		// here and this guard makes no claim about them.
		if !strings.HasPrefix(rel, "internal/cli/") {
			return
		}
		scanned++
		for _, pat := range forbidden {
			if strings.Contains(src, pat) {
				t.Errorf("%s contains %q — destination reads must go through readDestBytes "+
					"(internal/cli/destread.go), which refuses a non-regular path BEFORE the "+
					"open. os.ReadFile on a FIFO does not fail, it BLOCKS, so the read's own "+
					"error path never runs and the command never returns.", rel, pat)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}

	// Proof of life. Without this the guard passes vacuously the day the walk
	// stops finding files — the failure mode that makes a green guard worthless.
	// It already earned its keep once: the first draft used os.ReadDir(".") and
	// silently scanned nothing.
	if scanned < 10 {
		t.Fatalf("scanned only %d production files under internal/cli/; the guard is not "+
			"looking at the code it claims to check", scanned)
	}
}
