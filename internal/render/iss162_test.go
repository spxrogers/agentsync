package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestWrite_ChmodReconverges is the regression for issue #162 item D: the
// convergence short-circuit must still re-converge a content-identical file
// whose MODE drifted (an executable skill script that lost its +x bit), and
// report that as a change — while a genuinely-unchanged file+mode stays a no-op
// (no mtime churn).
func TestWrite_ChmodReconverges(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	dest := filepath.Join(t.TempDir(), "run.sh")
	content := []byte("#!/bin/sh\necho hi\n")
	op := adapter.FileOp{Action: "write", Path: dest, Content: content, Mode: 0o755}
	st := state.New()

	// Initial write establishes content + 0755.
	w1 := render.NewWriter(st, home, home, adapter.ScopeUser, "", "claude")
	if err := w1.Write(op, content); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o755 {
		t.Fatalf("initial mode = %v, want 0755", fi.Mode().Perm())
	}

	// A genuine no-op (same content + mode) must NOT churn: reported unchanged.
	wNoop := render.NewWriter(st, home, home, adapter.ScopeUser, "", "claude")
	if err := wNoop.Write(op, content); err != nil {
		t.Fatalf("no-op write: %v", err)
	}
	if !wNoop.Unchanged()[dest] {
		t.Fatalf("genuine no-op (content+mode identical) must be reported unchanged")
	}

	// Now strip the +x bit WITHOUT changing content.
	if err := os.Chmod(dest, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	w2 := render.NewWriter(st, home, home, adapter.ScopeUser, "", "claude")
	if err := w2.Write(op, content); err != nil {
		t.Fatalf("reconverge write: %v", err)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode not re-converged: %v, want 0755", fi.Mode().Perm())
	}
	if w2.Unchanged()[dest] {
		t.Fatalf("a content-identical mode change must NOT be reported unchanged")
	}
	if !w2.Wrote()[dest] {
		t.Fatalf("a content-identical mode change must be reported as written")
	}
}

// TestRecordOpsState_MergeTomlNumericNoFalseDrift pins issue #162 item F after
// the review-loop correction: a merge-toml-keys owned value whose leaf is numeric
// (e.g. a codex MCP `timeout` — a routine passthrough Extra field) records
// WITHOUT error. A hard guard here used to FAIL apply on exactly this ordinary
// config; it was removed because an in-range integer hashes identically as the
// JSON source and the TOML dest, so there is no false drift. The only residual —
// an integer > 2^53 or a TOML datetime, unreachable with real configs — is
// documented at the record site, not guarded, so apply never fails on a number.
func TestRecordOpsState_MergeTomlNumericNoFalseDrift(t *testing.T) {
	testenv.RequireContainer(t)
	cases := []struct {
		name    string
		dest    string // on-disk TOML dest content
		ours    string // op.Content (JSON)
		wantErr string // "" = expect success
	}{
		{
			name:    "numeric owned value records cleanly (no apply failure)",
			dest:    "[mcp_servers.github]\ntimeout = 30\n",
			ours:    `{"mcp_servers":{"github":{"timeout":30}}}`,
			wantErr: "",
		},
		{
			name:    "string owned value records cleanly",
			dest:    "[mcp_servers.github]\ncommand = \"npx\"\n",
			ours:    `{"mcp_servers":{"github":{"command":"npx"}}}`,
			wantErr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dest := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(dest, []byte(tc.dest), 0o644); err != nil {
				t.Fatal(err)
			}
			st := state.New()
			op := adapter.FileOp{
				Action:        "write",
				Path:          dest,
				Content:       []byte(tc.ours),
				MergeStrategy: "merge-toml-keys",
				SourceID:      "mcp/github.toml",
			}
			err := render.RecordOpsState(st, home, "codex", adapter.ScopeUser, "", []adapter.FileOp{op})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("string-only owned value should record cleanly, got: %v", err)
			}
			if len(st.Keys) == 0 {
				t.Fatalf("expected a recorded key for the string-only owned value")
			}
		})
	}
}
