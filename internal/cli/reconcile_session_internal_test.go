package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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
// overrideOps and marks dedupOverride), [q]uit (prints) and, with home pointed
// at a temp dir, [i]gnore (appends to ignore.toml under it). [w]rite-back needs
// a real ~/.agentsync, a plan and a destination and is covered end to end in
// reconcile_test.go instead. Two more arms would PANIC on this session rather
// than misbehave, which is what bounds the orphan and override tests: the
// orphan [r]emove arm (pruneStateFilesForPath on a nil st) and finish with a
// queued override (Registry.Lookup on a nil reg) — so the orphan tests press
// only [k], [q] or EOF, and nothing here calls finish;
// TestReconcile_FinishRunsExactlyOnce covers it end to end.
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
			if tc.action == actionOverride {
				if len(s.overrideOps) != 1 {
					t.Fatalf("after one [o]: %d queued override ops, want 1", len(s.overrideOps))
				}
				if oo := s.overrideOps[0]; oo.agentName != "claude" || oo.op.Path != "/dest/a" {
					t.Errorf("queued override = %+v, want claude's op for /dest/a", oo)
				}
			}
			// The same item a second time: nothing new may be queued, and
			// stop must not change.
			if got := s.applyAction(it, tc.action); got != tc.wantStop {
				t.Errorf("second applyAction(%v) = %v, want %v", tc.action, got, tc.wantStop)
			}
			if len(s.overrideOps) != tc.wantQueue {
				t.Errorf("after applying %v twice: %d queued override ops, want %d", tc.action, len(s.overrideOps), tc.wantQueue)
			}
			if tc.action == actionOverride {
				// The dedup key is agent AND path: another agent's item at the
				// same path is a different re-apply and must queue.
				other := it
				other.agentName = "opencode"
				s.applyAction(other, actionOverride)
				if len(s.overrideOps) != 2 {
					t.Errorf("after a second agent's [o] on the same path: %d queued, want 2", len(s.overrideOps))
				}
			}
		})
	}
}

// itemMenu is the per-item prompt's menu line, pinned here so the tests below
// can count how many times the user was (re)prompted.
const itemMenu = "  [w]rite-back  [o]verride  [s]kip  [i]gnore  [d]iff  [q]uit\n  > "

// TestPromptItem_DiffReprintsValuesAndMenu pins the [d]iff arm: it re-renders
// the item's values and the menu, echoes nothing, chooses no action, and the
// next key still decides the item. Measured on the pre-#232 tree and on commit
// 1: deleting the re-render failed zero tests — the arm was pinned only by the
// scripted-stdin harness.
func TestPromptItem_DiffReprintsValuesAndMenu(t *testing.T) {
	s, out := newTestSession(t, "ds")
	it := driftItem("/dest/a")
	act, stop := s.promptItem(it, []reconcileItem{it})
	if stop || act != actionSkip {
		t.Fatalf("promptItem = (%v, stop=%v), want (%v, false): [d] must not choose, the [s] after it must", act, stop, actionSkip)
	}
	got := out.String()
	if n := strings.Count(got, itemMenu); n != 2 {
		t.Errorf("menu printed %d time(s), want 2 (once before [d], once after); transcript:\n%s", n, got)
	}
	if n := strings.Count(got, "  destination: "); n != 2 {
		t.Errorf("values rendered %d time(s), want 2; transcript:\n%s", n, got)
	}
	if !strings.HasSuffix(got, "  > s\n") {
		t.Errorf("[d] is not echoed and only the deciding key is; the transcript must end at the echoed s, got:\n%q", got)
	}
}

// TestPromptItem_DeclinedBulkDoesNotReprintMenu pins the cancel path of the
// bulk confirmation: [N] prints "cancelled; choose a per-item action", records
// no bulk choice, and reads the next key WITHOUT re-printing the menu — the
// user is still at the same prompt, not a new one.
func TestPromptItem_DeclinedBulkDoesNotReprintMenu(t *testing.T) {
	s, out := newTestSession(t, "Wns")
	it := driftItem("/dest/a")
	act, stop := s.promptItem(it, []reconcileItem{it, driftItem("/dest/b")})
	if stop || act != actionSkip {
		t.Fatalf("promptItem = (%v, stop=%v), want (%v, false)", act, stop, actionSkip)
	}
	if s.bulk != actionNone {
		t.Errorf("a declined bulk choice must not be recorded; s.bulk = %v", s.bulk)
	}
	got := out.String()
	want := "apply 'w' to all 2 remaining items? [y/N] n\n  cancelled; choose a per-item action\ns\n"
	if !strings.HasSuffix(got, want) {
		t.Errorf("after declining, the next key is read at the same prompt with no menu re-print; transcript must end %q, got:\n%q", want, got)
	}
	if n := strings.Count(got, itemMenu); n != 1 {
		t.Errorf("menu printed %d time(s), want exactly 1; transcript:\n%s", n, got)
	}
}

// TestPromptItem_UnknownKeyIsIgnored pins what the prompt does with a byte
// parseItemKey rejects: nothing — no echo, no re-prompt, just the next read. The
// byte is a capital I, so this also pins that "not a bulk ignore" means ignored
// as a keystroke, not accepted as something else.
func TestPromptItem_UnknownKeyIsIgnored(t *testing.T) {
	s, out := newTestSession(t, "Is")
	it := driftItem("/dest/a")
	act, stop := s.promptItem(it, []reconcileItem{it})
	if stop || act != actionSkip {
		t.Fatalf("promptItem = (%v, stop=%v), want (%v, false)", act, stop, actionSkip)
	}
	if got := out.String(); !strings.HasSuffix(got, itemMenu+"s\n") {
		t.Errorf("an unknown key must leave the transcript untouched until a known one arrives; want it to end with the menu then the echoed s, got:\n%q", got)
	}
}

// TestWalk_BulkNeverSweepsOrphans pins that a confirmed bulk choice acts on
// the remaining actionable items only: an orphan in the queue still gets its
// own remove/keep prompt, and the confirmation's count excludes it.
func TestWalk_BulkNeverSweepsOrphans(t *testing.T) {
	s, out := newTestSession(t, "Syk")
	orphan := driftItem("/dest/b")
	orphan.orphan = true
	s.walk([]reconcileItem{driftItem("/dest/a"), orphan, driftItem("/dest/c")})
	got := out.String()
	if s.bulk != actionSkip {
		t.Fatalf("s.bulk = %v, want %v", s.bulk, actionSkip)
	}
	if !strings.Contains(got, "apply 's' to all 2 remaining items? [y/N] y\n") {
		t.Errorf("the blast radius must count the two drift items and not the orphan; transcript:\n%s", got)
	}
	if n := strings.Count(got, itemMenu); n != 1 {
		t.Errorf("item menu printed %d time(s), want 1 — the bulk choice answers /dest/c; transcript:\n%s", n, got)
	}
	if !strings.Contains(got, "  kept: /dest/b\n") {
		t.Errorf("the orphan must still be prompted and [k]ept; transcript:\n%s", got)
	}
}

// TestApplyAction_IgnoreAppendsToIgnoreFile pins the [i]gnore arm: the item's
// RAW label lands in ignore.toml under home and the transcript says so.
// Measured on commit 1: deleting the append and the print failed zero tests.
// home is the one piece of wiring this test sets, because appendIgnore writes
// under it; nothing else here needs a filesystem.
func TestApplyAction_IgnoreAppendsToIgnoreFile(t *testing.T) {
	s, out := newTestSession(t, "")
	s.home = t.TempDir()
	it := driftItem("/dest/.claude.json")
	it.ptr = "/mcpServers/demo"
	if stop := s.applyAction(it, actionIgnore); stop {
		t.Fatal("applyAction(actionIgnore) = stop, want the pass to continue")
	}
	data, err := os.ReadFile(filepath.Join(s.home, "ignore.toml"))
	if err != nil {
		t.Fatalf("ignore.toml not written: %v", err)
	}
	if want := "ignore = \"/dest/.claude.json#/mcpServers/demo\"\n"; string(data) != want {
		t.Errorf("ignore.toml = %q, want %q", data, want)
	}
	if got, want := out.String(), "  ignored: /dest/.claude.json#/mcpServers/demo\n"; got != want {
		t.Errorf("transcript = %q, want %q", got, want)
	}
}

// TestNewReconcileSession_RejectsMultipleAutoModes pins that the constructor,
// not its caller, enforces reconcileAuto's "at most one mode" invariant — and
// does so before loading anything, so the check costs no I/O and no session
// with two modes can be built (resolveAuto's switch would otherwise let
// writeBack win silently, the data-loss shape the check exists to prevent).
func TestNewReconcileSession_RejectsMultipleAutoModes(t *testing.T) {
	for _, auto := range []reconcileAuto{
		{writeBack: true, override: true},
		{writeBack: true, safe: true},
		{override: true, safe: true},
		{writeBack: true, override: true, safe: true},
	} {
		s, items, err := newReconcileSession(&cobra.Command{}, strings.NewReader(""), auto, "")
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("newReconcileSession(%+v) error = %v, want the mutually-exclusive error", auto, err)
		}
		if s != nil || items != nil {
			t.Errorf("newReconcileSession(%+v) returned a session or items alongside the error", auto)
		}
	}
}
