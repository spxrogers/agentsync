package render_test

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/render"
)

// TestCollisionReportString_Sanitizes pins that CollisionReport.String() strips
// control/bidi bytes from its config-derived Path/Pointer: every call site prints
// this string straight to a terminal (reconcile's override backup notice, update
// --apply's backup notice), so a shared-config component name with an ESC byte
// must not inject terminal escapes (issue #93/#171).
func TestCollisionReportString_Sanitizes(t *testing.T) {
	r := render.CollisionReport{
		Agent:    "claude",
		Path:     "/home/u/.claude/skills/sk\x1b[31mHACK/SKILL.md",
		Pointer:  "mcpServers/srv\x1b[31mPTR",
		BackupTo: "/home/u/.agentsync/.state/backups/x",
	}
	got := r.String()
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("CollisionReport.String() must sanitize config-derived Path/Pointer; got raw ESC:\n%q", got)
	}
	if !strings.Contains(got, "HACK") || !strings.Contains(got, "PTR") {
		t.Fatalf("sanitized markers missing — value blanked, not just sanitized:\n%q", got)
	}

	// Whole-file variant (no Pointer) must also sanitize.
	r2 := render.CollisionReport{Path: "cmd\x1b[31mWHOLE.md", BackupTo: "/b"}
	if strings.ContainsRune(r2.String(), 0x1b) {
		t.Fatalf("whole-file CollisionReport.String() left a raw ESC:\n%q", r2.String())
	}
}
