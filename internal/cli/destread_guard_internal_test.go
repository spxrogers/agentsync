package cli

import (
	"strings"
	"testing"
)

// TestEveryDestinationReadGoesThroughTheGate enforces the invariant that makes
// the FIFO guard hold: no production code in this package reads an op's
// destination path with a bare os.ReadFile. It is an invariant over the two
// spellings below, not over every possible one — see LIMITS.
//
// Why an invariant and not just the behavior tests: the four reads MEASURED to
// hang (readDestFile, diff's and reconcile's per-op reads, and writeBackFileItem)
// are each pinned by a timeout-bounded test. The three in import's state-seeding
// path are not, and cannot be from here — `agentsync import <agent>` does hang
// on a non-regular destination, but upstream of these sites, in the adapter
// Ingest reads this gate does not cover (#242). Guarding them neither fixed that
// nor could have.
//
// They were routed through the gate anyway, because three structurally
// identical unguarded reads sitting beside four guarded ones invite exactly the
// question "why those and not these", and there is no answer that survives
// being written down. This test is what keeps that decision from rotting: a new
// bare destination read fails here even where no hang is demonstrable at the
// site itself.
//
// LIMITS, stated so a reader does not over-trust it:
//   - The pattern is TEXT-shaped. A read spelled through a variable
//     (`p := op.Path; os.ReadFile(p)`), via afero, or with os.Open + io.ReadAll
//     slips past. It catches the copy-paste that actually happened seven times,
//     not every conceivable spelling. Concretely: reverting readDestFile's own
//     read to `os.ReadFile(path)` is NOT caught here — the behavior tests are
//     what cover that one.
//   - It is scoped to THIS PACKAGE. `apply` and `import <agent>` still hang on
//     a non-regular destination through internal/render and the adapter Ingest
//     paths (#241, #242); this guard says nothing about them, and passing it
//     does not mean agentsync as a whole is safe.
//   - It scans comments and string literals too, so a mention of the pattern in
//     prose would trip it. That is the safe direction to fail.
func TestEveryDestinationReadGoesThroughTheGate(t *testing.T) {
	// The forbidden spellings: a bare read of an op's destination path.
	forbidden := []string{
		"os.ReadFile(op.Path)",
		"os.ReadFile(it.op.Path)",
	}
	match := func(src string) string {
		for _, pat := range forbidden {
			if strings.Contains(src, pat) {
				return pat
			}
		}
		return ""
	}

	// Negative control, run every time and not only when the tree is dirty:
	// a guard that passes whatever the tree looks like is worse than none,
	// because it reads as coverage. Both prior guards in this package do the
	// same. This one drives the REAL matcher over synthetic sources — if the
	// matcher is ever narrowed or a pattern is dropped, this fails here rather
	// than silently stopping the repo walk from finding anything.
	// The planted sources are LITERALS, deliberately not built from `forbidden`.
	// A control assembled out of the same slice it is checking is tautological:
	// it passes by construction and cannot notice a pattern being dropped, which
	// is the most likely way this guard dies.
	for _, planted := range []string{
		"func f() { data, _ := os.ReadFile(op.Path) }",
		"func f(it reconcileItem) { data, _ := os.ReadFile(it.op.Path) }",
	} {
		if got := match(planted); got == "" {
			t.Fatalf("negative control: matcher missed a planted bare destination read in %q — "+
				"a pattern has been dropped from `forbidden`, and the repo walk below "+
				"cannot be trusted", planted)
		}
	}
	if got := match("func f() { data, _ := readDestBytes(op.Path) }"); got != "" {
		t.Fatalf("negative control: matcher flagged the CORRECT spelling as %q — "+
			"it would fail on a compliant tree", got)
	}

	repoRoot := repoRootFromCaller(t)
	scanned := 0
	if err := walkRepoGoFiles(repoRoot, func(rel, src string) {
		// Scoped to this package deliberately. internal/render and the adapter
		// Apply paths also read op.Path, but on the WRITE path, which this
		// change does not cover. Do NOT read that as "handled elsewhere":
		// render.isRegularOrAbsent's own doc says it is what stops
		// `apply --dry-run` hanging on a FIFO, and that is false — Writer.Write's
		// convergence read never calls it, and `apply --dry-run` still hangs
		// (#241). This guard makes no claim about those reads either way.
		if !strings.HasPrefix(rel, "internal/cli/") {
			return
		}
		scanned++
		if pat := match(src); pat != "" {
			t.Errorf("%s contains %q — destination reads must go through readDestBytes "+
				"(internal/cli/destread.go), which refuses a non-regular path BEFORE the "+
				"open. os.ReadFile on a FIFO does not fail, it BLOCKS, so the read's own "+
				"error path never runs and the command never returns.", rel, pat)
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
