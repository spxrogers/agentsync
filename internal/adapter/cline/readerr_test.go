package cline_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/cline"
)

// plantDir plants a DIRECTORY at a FILE path (EISDIR on os.ReadFile).
// plantFileAtDir plants a regular FILE at a DIRECTORY path (ENOTDIR on
// os.ReadDir). plantContent writes a real file. Each anchors to an on-disk
// "present-but-unreadable" condition — root-proof, unlike chmod 0o000 — per the
// CLAUDE.md fidelity rule. Cline implements no diagnostics sink, so a per-item
// read error is RETURNED rather than warned; these tests assert the loud error.
func plantDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func plantFileAtDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func plantContent(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIngest_UnreadableMemory_FailsLoud(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, memPath string)
		wantErr  bool
		wantBody string
	}{
		{name: "absent memory is a silent skip", setup: func(t *testing.T, memPath string) {}},
		{name: "present and valid memory ingests", setup: func(t *testing.T, memPath string) { plantContent(t, memPath, "# Mem\n\nBody.\n") }, wantBody: "# Mem\n\nBody.\n"},
		{name: "present but unreadable memory fails loud", setup: func(t *testing.T, memPath string) { plantDir(t, memPath) }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj := t.TempDir()
			memPath := filepath.Join(proj, ".clinerules", "agentsync.md")
			tt.setup(t, memPath)
			a := cline.New(cline.Options{TargetRoot: t.TempDir()})
			got, err := a.Ingest(adapter.ScopeProject, proj)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a loud ingest error for an unreadable memory file, got nil (body=%q)", got.Memory.Body)
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

func TestIngest_UnreadableWorkflows_FailsLoud(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, wfDir string)
		wantErr  bool
		wantCmds int
	}{
		{name: "absent workflows dir is a silent skip", setup: func(t *testing.T, wfDir string) {}},
		{name: "present and valid workflow ingests", setup: func(t *testing.T, wfDir string) {
			// Must carry the agentsync-owned marker: post-#175 cline ingest is
			// ownership-scoped, so an unmarked (foreign) workflow is deliberately not
			// captured. This case pins the read-error path, so use an owned workflow.
			plantContent(t, filepath.Join(wfDir, "deploy.md"), "<!-- agentsync:managed cline-workflow -->\n1. tag\n")
		}, wantCmds: 1},
		{name: "present but unreadable workflows dir fails loud", setup: func(t *testing.T, wfDir string) { plantFileAtDir(t, wfDir) }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj := t.TempDir()
			wfDir := filepath.Join(proj, ".clinerules", "workflows")
			tt.setup(t, wfDir)
			a := cline.New(cline.Options{TargetRoot: t.TempDir()})
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
