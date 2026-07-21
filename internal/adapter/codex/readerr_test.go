package codex_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/codex"
)

// plantDir plants a DIRECTORY at a FILE path (EISDIR on os.ReadFile). plantContent
// writes a real file. Both anchor the test to an on-disk "present-but-unreadable"
// condition — root-proof, unlike chmod 0o000 — per the CLAUDE.md fidelity rule.
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
			memPath := filepath.Join(tmp, ".codex", "AGENTS.md")
			tt.setup(t, memPath)
			a := codex.New(codex.Options{TargetRoot: tmp, Stderr: io.Discard})
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

// TestIngest_UnreadableConfig_FailsLoud: config.toml holds both MCP and hooks, so
// a corrupt-but-present config now fails loudly (was silently swallowed, reading
// as "no MCP, no hooks" that drift could classify as both components cleared).
func TestIngest_UnreadableConfig_FailsLoud(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, cfgPath string)
		wantErr bool
	}{
		{name: "absent config is a clean no-op", setup: func(t *testing.T, cfgPath string) {}},
		{name: "valid config without mcp/hooks is a clean no-op", setup: func(t *testing.T, cfgPath string) { plantContent(t, cfgPath, "model = \"gpt\"\n") }},
		{name: "corrupt-but-present config fails loud", setup: func(t *testing.T, cfgPath string) { plantContent(t, cfgPath, "= not valid toml") }, wantErr: true},
		{name: "present but unreadable config fails loud", setup: func(t *testing.T, cfgPath string) { plantDir(t, cfgPath) }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			cfgPath := filepath.Join(tmp, ".codex", "config.toml")
			tt.setup(t, cfgPath)
			a := codex.New(codex.Options{TargetRoot: tmp, Stderr: io.Discard})
			got, err := a.Ingest(adapter.ScopeUser, "")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a loud ingest error for a corrupt/unreadable config.toml, got nil (mcp=%+v hooks=%+v)", got.MCPServers, got.Hooks)
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
