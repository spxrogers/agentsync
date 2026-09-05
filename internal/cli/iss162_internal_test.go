package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// TestStatusDiff_ModeDriftDetection exercises the helpers that give status,
// diff and explain their mode-drift awareness (issue #162 item D): destModePerm's
// filesystem triage feeding planItem.opModeDrifted — the ONE mode question every
// surface asks since #229 PR-C — and modeHunk, which makes diff emit a "mode"
// hunk for a content-identical chmod that would otherwise read as "no diff". A
// file whose mode still matches, or whose intended mode is unspecified (0),
// stays a no-op — preserving the mtime-churn-avoidance intent. The four
// predicate rows are the truth table of the RECORDED-mode helper status used
// before #229 PR-C, carried over 1:1 with "recorded" reread as "intended"
// (op.Mode); the status-side oracle that distinguishes the two formulas is
// TestStatus_ModeDriftUsesOpModeNotRecordedMode below.
func TestStatusDiff_ModeDriftDetection(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.sh")
	const content = "#!/bin/sh\n"
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil { // umask-proof
		t.Fatal(err)
	}
	item := func(intended uint32, path string) planItem {
		perm, regular := destModePerm(path)
		return planItem{op: adapter.FileOp{Path: path, Mode: intended}, destPerm: perm, destRegular: regular}
	}

	// opModeDrifted, over destModePerm's triage.
	if perm, reg := destModePerm(p); perm != 0o755 || !reg {
		t.Fatalf("destModePerm(0755 file) = (%04o, %v), want (0755, true)", perm, reg)
	}
	if item(0o755, p).opModeDrifted() {
		t.Errorf("opModeDrifted: 0755 intended vs 0755 on disk must be false")
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if perm, reg := destModePerm(p); perm != 0o644 || !reg {
		t.Fatalf("destModePerm(0644 file) = (%04o, %v), want (0644, true)", perm, reg)
	}
	if !item(0o755, p).opModeDrifted() {
		t.Errorf("opModeDrifted: 0755 intended vs 0644 on disk must be true (drift)")
	}
	if item(0, p).opModeDrifted() {
		t.Errorf("opModeDrifted: intended mode 0 (unspecified) must never be drift")
	}
	absent := filepath.Join(dir, "absent")
	if perm, reg := destModePerm(absent); perm != 0 || reg {
		t.Fatalf("destModePerm(absent) = (%04o, %v), want (0, false)", perm, reg)
	}
	if item(0o755, absent).opModeDrifted() {
		t.Errorf("opModeDrifted: a missing file must not be reported as mode drift")
	}

	// modeHunk (diff side): intended 0755 vs on-disk 0644 → a hunk. The walk
	// records the on-disk side (destModePerm) and the op carries the intended.
	src, dst, ok := modeHunk(item(0o755, p))
	if !ok {
		t.Fatalf("modeHunk: expected a hunk for intended 0755 vs on-disk 0644")
	}
	if src != "mode 0755" || dst != "mode 0644" {
		t.Errorf("modeHunk src=%q dst=%q, want 'mode 0755' / 'mode 0644'", src, dst)
	}
	if _, _, ok := modeHunk(item(0o644, p)); ok {
		t.Errorf("modeHunk: no hunk expected when intended mode == on-disk mode")
	}
	if _, _, ok := modeHunk(item(0, p)); ok {
		t.Errorf("modeHunk: no hunk expected for an unspecified intended mode (0)")
	}
}

// TestStatus_ModeDriftUsesOpModeNotRecordedMode pins #229 axis 14 for status:
// the permission check measures the destination against the mode the next
// apply would WRITE (op.Mode), not the mode state RECORDED at the last apply.
// The fixture is the one configuration on which the two formulas disagree —
// recorded 0644 == disk 0644, op.Mode 0755 — so it is a single-fault flip:
// the recorded-mode formula answers `clean`, the op.Mode formula `drift`.
// (The characterization harness's T-09 does NOT discriminate here: its
// recorded mode and op.Mode BOTH differ from disk, so both formulas fire.)
func TestStatus_ModeDriftUsesOpModeNotRecordedMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "run.sh")
	const content = "#!/bin/sh\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil { // umask-proof
		t.Fatal(err)
	}
	userHome := t.TempDir()
	s := state.New()
	s.Files[stateFileKey(userHome, "claude", adapter.ScopeUser, "", p)] = state.FileEntry{SHA256: hashContent([]byte(content)), Mode: 0o644}
	plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{"claude": {Ops: []adapter.FileOp{
		{Action: adapter.ActionWrite, Path: p, Content: []byte(content), Mode: 0o755, SourceID: "skills/x/run.sh"},
	}}}}
	model := buildStatusModel(plan, []string{"claude"}, s, userHome, adapter.ScopeUser, "")
	// Both halves, so a walk that emitted zero items cannot pass: exactly one
	// item, and it is the drifted one.
	if len(model.Agents) != 1 || len(model.Agents[0].Items) != 1 {
		t.Fatalf("want exactly one status item, got %+v", model.Agents)
	}
	if model.Summary["drift"] != 1 || model.Agents[0].Items[0].Class != "drift" {
		t.Errorf("status must compare against op.Mode (0755 != disk 0644), not the RECORDED mode "+
			"(0644 == disk): summary=%v item=%+v", model.Summary, model.Agents[0].Items[0])
	}
}
