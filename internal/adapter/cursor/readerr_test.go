package cursor_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/cursor"
)

// plantDir plants a DIRECTORY at a FILE path (EISDIR on os.ReadFile). plantContent
// writes a real file. Both anchor to an on-disk "present-but-unreadable" condition
// (root-proof, unlike chmod 0o000) per the CLAUDE.md fidelity rule. Note: cursor's
// ingest_test.go already defines writeFile, so this file uses distinct names.
func plantDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
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

// Cursor memory is project-scope only (user rules live in Cursor's app-local
// storage, so p.Memory == "" at user scope).
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
			memPath := filepath.Join(proj, "AGENTS.md")
			tt.setup(t, memPath)
			a := cursor.New(cursor.Options{TargetRoot: t.TempDir(), Stderr: io.Discard})
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

// TestIngest_UnreadableHooks_FailsLoud: a corrupt-but-present hooks.json now fails
// loudly (was swallowed via json.Unmarshal(...) == nil), matching the MCP block.
func TestIngest_UnreadableHooks_FailsLoud(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, hooksPath string)
		wantErr bool
	}{
		{name: "absent hooks.json is a clean no-op", setup: func(t *testing.T, hooksPath string) {}},
		{name: "valid hooks.json without hooks key is a clean no-op", setup: func(t *testing.T, hooksPath string) { plantContent(t, hooksPath, `{"version":1}`) }},
		{name: "corrupt-but-present hooks.json fails loud", setup: func(t *testing.T, hooksPath string) { plantContent(t, hooksPath, "{ not valid json") }, wantErr: true},
		{name: "present but unreadable hooks.json fails loud", setup: func(t *testing.T, hooksPath string) { plantDir(t, hooksPath) }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj := t.TempDir()
			hooksPath := filepath.Join(proj, ".cursor", "hooks.json")
			tt.setup(t, hooksPath)
			a := cursor.New(cursor.Options{TargetRoot: t.TempDir(), Stderr: io.Discard})
			got, err := a.Ingest(adapter.ScopeProject, proj)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a loud ingest error for a corrupt/unreadable hooks.json, got nil (hooks=%+v)", got.Hooks)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.Hooks) != 0 {
				t.Fatalf("expected no hooks, got %+v", got.Hooks)
			}
		})
	}
}
