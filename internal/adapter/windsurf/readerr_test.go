package windsurf_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/windsurf"
)

// planterDir plants a DIRECTORY at a FILE path so os.ReadFile returns a
// non-os.IsNotExist error (EISDIR). planterFile plants a regular FILE at a
// DIRECTORY path so os.ReadDir returns a non-os.IsNotExist error (ENOTDIR).
// Both produce a deterministic, root-proof "present-but-unreadable" on-disk
// condition (chmod 0o000 is bypassable by the privileged container user), and
// both are anchored to a real filesystem state per the CLAUDE.md fidelity rule.
func planterDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func planterFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestIngest_UnreadableMemory_FailsLoud: a present-but-unreadable global rules
// file fails the ingest loudly instead of silently leaving Memory.Body empty
// (which reconcile could write back as a cleared memory).
func TestIngest_UnreadableMemory_FailsLoud(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, memPath string)
		wantErr  bool
		wantBody string
	}{
		{
			name:  "absent global rules is a silent skip",
			setup: func(t *testing.T, memPath string) {},
		},
		{
			name:     "present and valid global rules ingests",
			setup:    func(t *testing.T, memPath string) { writeFile(t, memPath, "# Global\n\nAlways on.\n") },
			wantBody: "# Global\n\nAlways on.\n",
		},
		{
			name:    "present but unreadable global rules fails loud",
			setup:   func(t *testing.T, memPath string) { planterDir(t, memPath) },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			memPath := filepath.Join(tmp, ".codeium", "windsurf", "memories", "global_rules.md")
			tt.setup(t, memPath)
			a := windsurf.New(windsurf.Options{TargetRoot: tmp})
			got, err := a.Ingest(adapter.ScopeUser, "")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a loud ingest error for an unreadable global rules file, got nil (body=%q)", got.Memory.Body)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Memory.Body != tt.wantBody {
				t.Fatalf("memory body: got %q want %q", got.Memory.Body, tt.wantBody)
			}
		})
	}
}

// TestIngest_UnreadableWorkflows_FailsLoud: a present-but-unreadable workflows
// directory fails loud rather than silently yielding an empty Commands slice.
func TestIngest_UnreadableWorkflows_FailsLoud(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, wfDir string)
		wantErr  bool
		wantCmds int
	}{
		{
			name:  "absent workflows dir is a silent skip",
			setup: func(t *testing.T, wfDir string) {},
		},
		{
			name:     "present and valid workflow ingests",
			setup:    func(t *testing.T, wfDir string) { writeFile(t, filepath.Join(wfDir, "deploy.md"), "1. tag\n2. push\n") },
			wantCmds: 1,
		},
		{
			name:    "present but unreadable workflows dir fails loud",
			setup:   func(t *testing.T, wfDir string) { planterFile(t, wfDir) },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj := t.TempDir()
			wfDir := filepath.Join(proj, ".windsurf", "workflows")
			tt.setup(t, wfDir)
			a := windsurf.New(windsurf.Options{TargetRoot: t.TempDir()})
			got, err := a.Ingest(adapter.ScopeProject, proj)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a loud ingest error for an unreadable workflows dir, got nil (cmds=%+v)", got.Commands)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.Commands) != tt.wantCmds {
				t.Fatalf("commands: got %d want %d (%+v)", len(got.Commands), tt.wantCmds, got.Commands)
			}
		})
	}
}
