package cli

import (
	"bufio"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/ui"
)

// The tests below drive reconcileSession's methods directly. That is the point
// of the type (#232): before it existed, the prompt loops, the bulk-confirm
// state machine and the run's exit paths were reachable only by running the
// whole 375-line reconcileRun against a real ~/.agentsync tree, a real plan and
// a real destination — so the paths that decide when a pass STOPS (an EOF at
// either prompt, an EOF mid bulk-confirm, [q]uit with items still queued) were
// pinned by nothing. Measured on the pre-#232 tree: mutating any of those five
// exits failed zero tests.
//
// None of these tests touches the filesystem: every item carries hasText=false,
// so the prompt renders the SHA-prefix fallback, and every action they exercise
// (skip, quit) is a no-op or a print.

// newTestSession builds a session wired to a scripted stdin and an in-memory
// transcript. Only the fields the prompt/walk path reads are set; home, st and
// reg are deliberately left zero, which bounds what these tests may drive: the
// actions exercised below are [s]kip (a no-op), [o]verride (appends to
// overrideOps and marks dedupOverride) and [q]uit (prints). [w]rite-back and
// [i]gnore need a real ~/.agentsync and
// are covered end to end in reconcile_test.go instead — driving them from here
// would write relative to the test's working directory.
//
// The reader is NOT a fake: it is the same *bufio.Reader production wraps stdin
// in, over a strings.Reader, so readChar sees production's exact EOF behaviour.
func newTestSession(t *testing.T, stdin string) (*reconcileSession, *strings.Builder) {
	t.Helper()
	var out strings.Builder
	p := ui.New(&out, &out, ui.ColorNever)
	return &reconcileSession{
		p:              p,
		w:              p.Out,
		br:             bufio.NewReader(strings.NewReader(stdin)),
		writtenSources: map[string][]byte{},
		dedupOverride:  map[string]bool{},
	}, &out
}

// driftItem is a minimal actionable, non-orphan item: drift class, a distinct
// path, and no text (so the prompt shows the hash fallback and reads nothing).
func driftItem(path string) reconcileItem {
	return reconcileItem{
		agentName: "claude",
		op:        adapter.FileOp{Path: path},
		cls:       drift.Drift,
		hsrc:      "aaaa", hdest: "bbbb",
	}
}

// TestParseItemKey pins the per-item hotkey table, including the two case
// foldings the prompt deliberately does NOT do.
//
// The old loop matched `case 'w','W','o','O','s','S','i','q','Q'` and only then
// folded with `ch | 0x20`, so 'I' and 'D' fell to the ignore-and-re-read
// default by OMISSION. Folding every byte uniformly would silently add two
// accepted keystrokes — one of them a bulk [I]gnore that has no confirmation
// step at all, which is exactly the "stray capital wipes the queue" failure the
// bulk confirmation (#155) exists to prevent. The asymmetry is behaviour; this
// table is where it is written down.
func TestParseItemKey(t *testing.T) {
	tests := []struct {
		name     string
		ch       byte
		wantAct  reconcileAction
		wantBulk bool
		wantDiff bool
		wantOK   bool
	}{
		{name: "w write-back", ch: 'w', wantAct: actionWriteBack, wantOK: true},
		{name: "W bulk write-back", ch: 'W', wantAct: actionWriteBack, wantBulk: true, wantOK: true},
		{name: "o override", ch: 'o', wantAct: actionOverride, wantOK: true},
		{name: "O bulk override", ch: 'O', wantAct: actionOverride, wantBulk: true, wantOK: true},
		{name: "s skip", ch: 's', wantAct: actionSkip, wantOK: true},
		{name: "S bulk skip", ch: 'S', wantAct: actionSkip, wantBulk: true, wantOK: true},
		{name: "i ignore", ch: 'i', wantAct: actionIgnore, wantOK: true},
		{name: "q quit", ch: 'q', wantAct: actionQuit, wantOK: true},
		{name: "Q quit folds", ch: 'Q', wantAct: actionQuit, wantOK: true},
		{name: "d diff", ch: 'd', wantAct: actionNone, wantDiff: true, wantOK: true},
		// The two deliberate non-foldings, and the reason this table exists.
		{name: "I is NOT a bulk ignore", ch: 'I'},
		{name: "D is NOT a diff", ch: 'D'},
		{name: "unknown letter", ch: 'x'},
		{name: "digit", ch: '7'},
		{name: "NUL", ch: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			act, bulk, diff, ok := parseItemKey(tc.ch)
			if act != tc.wantAct || bulk != tc.wantBulk || diff != tc.wantDiff || ok != tc.wantOK {
				t.Errorf("parseItemKey(%q) = (%v, bulk=%v, diff=%v, ok=%v), want (%v, bulk=%v, diff=%v, ok=%v)",
					tc.ch, act, bulk, diff, ok, tc.wantAct, tc.wantBulk, tc.wantDiff, tc.wantOK)
			}
		})
	}
}

// TestBulkTargets pins the bulk-confirm count as the TRUE blast radius (#155):
// the items from the current one forward that a bulk choice would act on. Items
// already answered are behind the slice; a non-actionable item is not swept; and
// an orphan is never swept, because it has its own r/k prompt.
//
// A count of len(rest) passes the pre-existing end-to-end coverage (three
// drifted servers, all actionable, none orphaned — measured), so the mixed rows
// below are the ones that make the assertion mean anything.
func TestBulkTargets(t *testing.T) {
	orphan := driftItem("/o")
	orphan.orphan = true
	clean := driftItem("/c")
	clean.cls = drift.Clean

	tests := []struct {
		name string
		rest []reconcileItem
		want int
	}{
		{name: "empty queue", rest: nil, want: 0},
		{name: "all actionable", rest: []reconcileItem{driftItem("/a"), driftItem("/b")}, want: 2},
		{name: "orphans are never swept", rest: []reconcileItem{driftItem("/a"), orphan}, want: 1},
		{name: "non-actionable classes do not count", rest: []reconcileItem{driftItem("/a"), clean}, want: 1},
		{name: "only orphans", rest: []reconcileItem{orphan}, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := bulkTargets(tc.rest); got != tc.want {
				t.Errorf("bulkTargets = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestPromptItem_EOFStopsThePass pins the first of the three EOF exits: the
// input ends while the per-item prompt is waiting. The pass must STOP (so the
// caller reaches finish and flushes queued overrides + pruned state — #171)
// rather than treat the item as answered.
func TestPromptItem_EOFStopsThePass(t *testing.T) {
	s, out := newTestSession(t, "")
	it := driftItem("/dest/a")
	act, stop := s.promptItem(it, []reconcileItem{it})
	if !stop {
		t.Errorf("promptItem at EOF: stop = false, want true — the pass must end, not fall through to the next item")
	}
	if act != actionNone {
		t.Errorf("promptItem at EOF: action = %v, want %v — an unanswered item must not resolve to anything", act, actionNone)
	}
	if !strings.Contains(out.String(), "[w]rite-back") {
		t.Errorf("the prompt itself should still have been printed; got:\n%s", out.String())
	}
}

// TestPromptItem_EOFMidBulkConfirmStopsThePass pins the exit the issue calls out
// as unreachable from a test before the session existed: the input ends AFTER a
// capital W/O/S has printed the confirmation prompt but BEFORE the y/N answer.
//
// Two things are asserted, and the second is the subtle one: the transcript must
// end at "[y/N] " with no echoed character, because the echo is written only
// after the read succeeds. Treating an EOF as a declined confirmation would
// print a stray NUL and keep going.
func TestPromptItem_EOFMidBulkConfirmStopsThePass(t *testing.T) {
	s, out := newTestSession(t, "W")
	it := driftItem("/dest/a")
	act, stop := s.promptItem(it, []reconcileItem{it, driftItem("/dest/b")})
	if !stop {
		t.Errorf("promptItem at EOF mid-confirm: stop = false, want true")
	}
	if act != actionNone {
		t.Errorf("promptItem at EOF mid-confirm: action = %v, want %v", act, actionNone)
	}
	if s.bulk != actionNone {
		t.Errorf("an unconfirmed bulk choice must not be recorded; s.bulk = %v", s.bulk)
	}
	got := out.String()
	if !strings.Contains(got, "apply 'w' to all 2 remaining items? [y/N] ") {
		t.Errorf("the confirmation prompt should name the action and the blast radius; got:\n%s", got)
	}
	if !strings.HasSuffix(got, "[y/N] ") {
		t.Errorf("the transcript must end at the unanswered confirmation — nothing is echoed for a read that failed; got:\n%q", got)
	}
}

// TestPromptOrphan_EOFStopsThePass pins the third EOF exit, on the orphan
// remove/keep prompt. Same contract as the item prompt: stop the pass so finish
// still persists a removal made earlier in the same run.
func TestPromptOrphan_EOFStopsThePass(t *testing.T) {
	s, out := newTestSession(t, "")
	it := driftItem("/dest/skills/demo/SKILL.md")
	it.orphan = true
	if stop := s.promptOrphan(it); !stop {
		t.Error("promptOrphan at EOF: stop = false, want true")
	}
	if s.stateDirty {
		t.Error("an unanswered orphan prompt must not mark state dirty")
	}
	if !strings.Contains(out.String(), "[r]emove") {
		t.Errorf("the orphan prompt should still have been printed; got:\n%s", out.String())
	}
}

// TestPromptOrphan_QuitStopsThePass is the orphan prompt's [q]uit exit, the
// second of the two quits. It prints "quit" (the EOF exit does not) and stops.
func TestPromptOrphan_QuitStopsThePass(t *testing.T) {
	s, out := newTestSession(t, "q")
	it := driftItem("/dest/skills/demo/SKILL.md")
	it.orphan = true
	if stop := s.promptOrphan(it); !stop {
		t.Error("promptOrphan on [q]: stop = false, want true")
	}
	if !strings.Contains(out.String(), "quit") {
		t.Errorf("[q] at the orphan prompt should print quit; got:\n%s", out.String())
	}
}

// TestWalk_QuitLeavesRemainingItemsUnprompted pins that [q]uit ENDS the pass.
// The observable is the transcript: the third item is never printed at all.
//
// The pre-existing end-to-end quit tests each have a single drifted item, so
// "quit" appearing in the output is true whether or not the walk stops —
// measured: making applyAction's quit arm return false failed zero tests.
func TestWalk_QuitLeavesRemainingItemsUnprompted(t *testing.T) {
	s, out := newTestSession(t, "sq")
	s.walk([]reconcileItem{driftItem("/dest/a"), driftItem("/dest/b"), driftItem("/dest/c")})
	got := out.String()
	if n := strings.Count(got, "[w]rite-back"); n != 2 {
		t.Errorf("after [s] then [q] the walk must stop: %d item prompts, want 2\n%s", n, got)
	}
	if strings.Contains(got, "/dest/c") {
		t.Errorf("the item after [q]uit must never be prompted; got:\n%s", got)
	}
}

// TestWalk_ConfirmedBulkSkipsLaterPrompts pins the other half of the bulk state
// machine: once confirmed, the choice is recorded ON THE SESSION and every later
// item is resolved without a prompt.
//
// [S]kip is used deliberately: applying it touches nothing, so the assertion is
// purely about the prompt count. Dropping the `s.bulk = act` assignment — the
// bug this pins — leaves the confirmation working for the current item only and
// re-prompts the rest; measured against the pre-existing suite, it failed zero
// tests.
func TestWalk_ConfirmedBulkSkipsLaterPrompts(t *testing.T) {
	s, out := newTestSession(t, "Sy")
	s.walk([]reconcileItem{driftItem("/dest/a"), driftItem("/dest/b"), driftItem("/dest/c")})
	got := out.String()
	if n := strings.Count(got, "[w]rite-back"); n != 1 {
		t.Errorf("a confirmed bulk choice must prompt exactly once: %d prompts\n%s", n, got)
	}
	if s.bulk != actionSkip {
		t.Errorf("s.bulk = %v, want %v — the confirmed choice must persist for the rest of the queue", s.bulk, actionSkip)
	}
	if !strings.Contains(got, "apply 's' to all 3 remaining items?") {
		t.Errorf("the confirmation should name the action and the full remaining count; got:\n%s", got)
	}
}

// TestApplyAction_OnlyQuitStopsThePass pins the applyAction contract each walk
// exit depends on: quit stops, and nothing else does. The override row also
// asserts what "queues" means — one overrideOp per distinct agent+path, so a
// second [o] on the same item (two pointers inside one merge file) does not
// re-apply the file twice (dedupOverride).
func TestApplyAction_OnlyQuitStopsThePass(t *testing.T) {
	tests := []struct {
		name      string
		action    reconcileAction
		wantStop  bool
		wantQueue int // overrideOps after applying the action twice to the same item
	}{
		{name: "skip", action: actionSkip},
		{name: "override queues", action: actionOverride, wantQueue: 1},
		{name: "quit", action: actionQuit, wantStop: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestSession(t, "")
			it := driftItem("/dest/a")
			if got := s.applyAction(it, tc.action); got != tc.wantStop {
				t.Errorf("applyAction(%v) = %v, want %v", tc.action, got, tc.wantStop)
			}
			if tc.action == actionOverride && len(s.overrideOps) != 1 {
				t.Fatalf("after one [o]: %d queued override ops, want 1", len(s.overrideOps))
			}
			// The same item a second time: nothing new may be queued, and
			// stop must not change.
			if got := s.applyAction(it, tc.action); got != tc.wantStop {
				t.Errorf("second applyAction(%v) = %v, want %v", tc.action, got, tc.wantStop)
			}
			if len(s.overrideOps) != tc.wantQueue {
				t.Errorf("after applying %v twice: %d queued override ops, want %d", tc.action, len(s.overrideOps), tc.wantQueue)
			}
		})
	}
}
