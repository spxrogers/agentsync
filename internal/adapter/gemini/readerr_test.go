package gemini_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
)

// plantDir plants a DIRECTORY at a FILE path (EISDIR on os.ReadFile). plantContent
// writes a real file. Both anchor to an on-disk "present-but-unreadable" condition
// (root-proof, unlike chmod 0o000) per the CLAUDE.md fidelity rule.
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
			tmp := t.TempDir()
			memPath := filepath.Join(tmp, ".gemini", "GEMINI.md")
			tt.setup(t, memPath)
			a := gemini.New(gemini.Options{TargetRoot: tmp, Stderr: io.Discard})
			got, err := a.Ingest(adapter.ScopeUser, "")
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

// TestIngest_UnreadableSettings_FailsLoud: settings.json holds MCP + hooks; a
// corrupt-but-present file fails loudly (DecodeJSONC parse error returned).
func TestIngest_UnreadableSettings_FailsLoud(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, settingsPath string)
		wantErr bool
	}{
		{name: "absent settings is a clean no-op", setup: func(t *testing.T, settingsPath string) {}},
		{name: "valid settings without mcp/hooks is a clean no-op", setup: func(t *testing.T, settingsPath string) { plantContent(t, settingsPath, `{"theme":"x"}`) }},
		{name: "corrupt-but-present settings fails loud", setup: func(t *testing.T, settingsPath string) { plantContent(t, settingsPath, "{ not valid json") }, wantErr: true},
		{name: "present but unreadable settings fails loud", setup: func(t *testing.T, settingsPath string) { plantDir(t, settingsPath) }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			settingsPath := filepath.Join(tmp, ".gemini", "settings.json")
			tt.setup(t, settingsPath)
			a := gemini.New(gemini.Options{TargetRoot: tmp, Stderr: io.Discard})
			got, err := a.Ingest(adapter.ScopeUser, "")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a loud ingest error for a corrupt/unreadable settings.json, got nil (mcp=%+v hooks=%+v)", got.MCPServers, got.Hooks)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.MCPServers) != 0 || len(got.Hooks) != 0 {
				t.Fatalf("expected no MCP/hooks, got mcp=%+v hooks=%+v", got.MCPServers, got.Hooks)
			}
		})
	}
}
