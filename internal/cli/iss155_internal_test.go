package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// TestApplyScopePrompt_GoesToStderr proves the interactive scope menu is written
// to stderr, never stdout — so a caller resolving scope interactively before a
// `status --json` / `diff --json` run gets a byte-clean payload on stdout.
//
// The end-to-end prompt is gated on an interactive TTY (stdinIsTerminal), which a
// test's string reader is not, so we exercise promptScopeChoice directly with
// separated stdout/stderr buffers — the honest unit boundary for the routing fix.
func TestApplyScopePrompt_GoesToStderr(t *testing.T) {
	var out, errBuf bytes.Buffer
	cmd := &cobra.Command{Use: "status"}
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader("2\n")) // choose user scope

	sc, root, err := promptScopeChoice(cmd, "/proj", "/home/u")
	if err != nil {
		t.Fatalf("promptScopeChoice: %v", err)
	}
	if sc != adapter.ScopeUser || root != "" {
		t.Fatalf("expected user scope; got sc=%v root=%q", sc, root)
	}
	// The whole menu must land on stderr, and stdout must stay byte-clean.
	if out.Len() != 0 {
		t.Fatalf("scope prompt leaked to stdout:\n%s", out.String())
	}
	if !strings.Contains(errBuf.String(), "project tree") {
		t.Fatalf("scope menu should be on stderr; got:\n%s", errBuf.String())
	}
}

// TestBuildStatusModel_OrphanDriftedAndForeignCollision drives the two drift
// classes that previously had no buildStatusModel coverage, end-to-end through
// the real classifier, with on-disk + state fixtures:
//
//   - foreign-collision: an op the source renders, no state entry (never
//     applied), and a differing file already on disk.
//   - orphan-drifted: a whole file agentsync owns in state but no op renders
//     anymore, whose on-disk content has since been hand-edited away from what
//     was applied (a local edit the next apply would delete).
func TestBuildStatusModel_OrphanDriftedAndForeignCollision(t *testing.T) {
	tests := []struct {
		name  string
		class string
		setup func(t *testing.T, userHome string) (render.RenderPlan, *state.Targets)
	}{
		{
			name:  "foreign-collision",
			class: "foreign-collision",
			setup: func(t *testing.T, userHome string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				dest := filepath.Join(userHome, "dest", "foreign.md")
				mustWrite(t, dest, "FOREIGN") // differs from the rendered "SOURCE"
				plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{
					"claude": {Ops: []adapter.FileOp{{Path: dest, Content: []byte("SOURCE")}}},
				}}
				// No state entry for dest → happlied == "" → foreign-collision.
				return plan, state.New()
			},
		},
		{
			name:  "orphan-drifted",
			class: "orphan-drifted",
			setup: func(t *testing.T, userHome string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				orphan := filepath.Join(userHome, "dest", "orphan.md")
				mustWrite(t, orphan, "EDITED") // hand-edited away from what was applied
				// The plan renders SOME other file so claude has a result, but NOT
				// the orphan — so OrphanFiles surfaces it.
				other := filepath.Join(userHome, "dest", "kept.md")
				mustWrite(t, other, "KEPT")
				plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{
					"claude": {Ops: []adapter.FileOp{{Path: other, Content: []byte("KEPT")}}},
				}}
				s := state.New()
				// Owned in state (applied "APPLIED"), no op renders it anymore, and
				// on-disk "EDITED" differs from applied → orphan-drifted.
				s.Files[stateFileKey(userHome, "claude", adapter.ScopeUser, "", orphan)] = state.FileEntry{
					SHA256: hashContent([]byte("APPLIED")),
				}
				// Seed the kept file's state so it classifies clean (not noise).
				s.Files[stateFileKey(userHome, "claude", adapter.ScopeUser, "", other)] = state.FileEntry{
					SHA256: hashContent([]byte("KEPT")),
				}
				return plan, s
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			userHome := t.TempDir()
			plan, s := tc.setup(t, userHome)
			model := buildStatusModel(plan, []string{"claude"}, s, userHome, adapter.ScopeUser, "")
			if got := model.Summary[tc.class]; got != 1 {
				t.Fatalf("Summary[%q] = %d, want 1; full summary=%v", tc.class, got, model.Summary)
			}
		})
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
