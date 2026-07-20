package continuedev_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/continuedev"
)

// plantDir plants a DIRECTORY at a FILE path (EISDIR on os.ReadFile).
// plantFileAtDir plants a regular FILE at a DIRECTORY path (ENOTDIR on
// os.ReadDir). plantContent writes a real file. Each anchors to an on-disk
// "present-but-unreadable" condition (root-proof) per the CLAUDE.md fidelity rule.
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
			tmp := t.TempDir()
			memPath := filepath.Join(tmp, ".continue", "rules", "agentsync.md")
			tt.setup(t, memPath)
			a := continuedev.New(continuedev.Options{TargetRoot: tmp, Stderr: io.Discard})
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

// TestIngest_UnreadableMCPDir_FailsLoud: a present-but-unreadable .continue/
// mcpServers directory fails loud rather than silently yielding no MCP servers.
func TestIngest_UnreadableMCPDir_FailsLoud(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, mcpDir string)
		wantErr bool
		wantSrv int
	}{
		{name: "absent mcp dir is a silent skip", setup: func(t *testing.T, mcpDir string) {}},
		{name: "present and valid mcp block ingests", setup: func(t *testing.T, mcpDir string) {
			plantContent(t, filepath.Join(mcpDir, "gh.yaml"), "mcpServers:\n  - name: gh\n    command: npx\n")
		}, wantSrv: 1},
		{name: "present but unreadable mcp dir fails loud", setup: func(t *testing.T, mcpDir string) { plantFileAtDir(t, mcpDir) }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			mcpDir := filepath.Join(tmp, ".continue", "mcpServers")
			tt.setup(t, mcpDir)
			a := continuedev.New(continuedev.Options{TargetRoot: tmp, Stderr: io.Discard})
			got, err := a.Ingest(adapter.ScopeUser, "")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a loud ingest error for an unreadable mcpServers dir, got nil (mcp=%+v)", got.MCPServers)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.MCPServers) != tt.wantSrv {
				t.Fatalf("mcp servers: got %d want %d (%+v)", len(got.MCPServers), tt.wantSrv, got.MCPServers)
			}
		})
	}
}
