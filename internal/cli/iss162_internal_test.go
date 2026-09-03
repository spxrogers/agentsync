package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// TestStatusDiff_ModeDriftDetection exercises the helpers that give status and
// diff their mode-drift awareness (issue #162 item D): destModePerm's filesystem
// triage feeding planItem.recordedModeDrifted, which upgrades a content-clean
// file whose RECORDED mode diverged from disk to `drift` in buildStatusModel,
// and modeHunk, which makes diff emit a "mode" hunk for a content-identical
// chmod that would otherwise read as "no diff". A file whose mode still
// matches, or whose recorded/intended mode is unspecified (0), stays a no-op —
// preserving the mtime-churn-avoidance intent. The four status rows are the
// truth table of the modeDrifted helper this replaced (#229).
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
	item := func(recorded uint32, path string) planItem {
		perm, regular := destModePerm(path)
		return planItem{recordedMode: recorded, destPerm: perm, destRegular: regular}
	}

	// recordedModeDrifted (status side), over destModePerm's triage.
	if perm, reg := destModePerm(p); perm != 0o755 || !reg {
		t.Fatalf("destModePerm(0755 file) = (%04o, %v), want (0755, true)", perm, reg)
	}
	if item(0o755, p).recordedModeDrifted() {
		t.Errorf("recordedModeDrifted: 0755 recorded vs 0755 on disk must be false")
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if perm, reg := destModePerm(p); perm != 0o644 || !reg {
		t.Fatalf("destModePerm(0644 file) = (%04o, %v), want (0644, true)", perm, reg)
	}
	if !item(0o755, p).recordedModeDrifted() {
		t.Errorf("recordedModeDrifted: 0755 recorded vs 0644 on disk must be true (drift)")
	}
	if item(0, p).recordedModeDrifted() {
		t.Errorf("recordedModeDrifted: recorded mode 0 (unspecified) must never be drift")
	}
	absent := filepath.Join(dir, "absent")
	if perm, reg := destModePerm(absent); perm != 0 || reg {
		t.Fatalf("destModePerm(absent) = (%04o, %v), want (0, false)", perm, reg)
	}
	if item(0o755, absent).recordedModeDrifted() {
		t.Errorf("recordedModeDrifted: a missing file must not be reported as mode drift")
	}

	// status asks the RECORDED mode, not the op's (#229 axis 14 is PR-C's
	// switch): recorded 0644 matches the 0644 on disk, so the file is clean even
	// though op.Mode says the next apply would chmod it to 0755.
	userHome := t.TempDir()
	s := state.New()
	s.Files[stateFileKey(userHome, "claude", adapter.ScopeUser, "", p)] = state.FileEntry{SHA256: hashContent([]byte(content)), Mode: 0o644}
	plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{"claude": {Ops: []adapter.FileOp{
		{Action: "write", Path: p, Content: []byte(content), Mode: 0o755, SourceID: "skills/x/run.sh"},
	}}}}
	if model := buildStatusModel(plan, []string{"claude"}, s, userHome, adapter.ScopeUser, ""); model.Summary["clean"] != 1 {
		t.Errorf("status must compare against the RECORDED mode (0644 == disk), not op.Mode (0755): summary=%v", model.Summary)
	}

	// modeHunk (diff side): intended 0755 vs on-disk 0644 → a hunk. The walk
	// records the on-disk side (destModePerm) and the op carries the intended.
	hunkItem := func(wantMode uint32) planItem {
		perm, regular := destModePerm(p)
		return planItem{op: adapter.FileOp{Path: p, Mode: wantMode}, destPerm: perm, destRegular: regular}
	}
	src, dst, ok := modeHunk(hunkItem(0o755))
	if !ok {
		t.Fatalf("modeHunk: expected a hunk for intended 0755 vs on-disk 0644")
	}
	if src != "mode 0755" || dst != "mode 0644" {
		t.Errorf("modeHunk src=%q dst=%q, want 'mode 0755' / 'mode 0644'", src, dst)
	}
	if _, _, ok := modeHunk(hunkItem(0o644)); ok {
		t.Errorf("modeHunk: no hunk expected when intended mode == on-disk mode")
	}
	if _, _, ok := modeHunk(hunkItem(0)); ok {
		t.Errorf("modeHunk: no hunk expected for an unspecified intended mode (0)")
	}
}
