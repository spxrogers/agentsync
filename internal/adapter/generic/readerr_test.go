package generic_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/generic"
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

// readErrSpec is a minimal breadth-tier spec targeting memory + MCP + skills under
// the user home, used to exercise the generic ingest read-error discrimination.
func readErrSpec() generic.Spec {
	return generic.Spec{
		Name:   "readerrtest",
		Memory: generic.FileTarget{User: "MEM.md"},
		MCP:    generic.MCPTarget{User: filepath.Join(".g", "mcp.json")},
		Skills: generic.FileTarget{User: filepath.Join(".g", "skills")},
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
			memPath := filepath.Join(tmp, "MEM.md")
			tt.setup(t, memPath)
			a := generic.New(readErrSpec(), generic.Options{TargetRoot: tmp})
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

func TestIngest_UnreadableMCP_FailsLoud(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, mcpPath string)
		wantErr bool
		wantSrv int
	}{
		{name: "absent mcp is a clean no-op", setup: func(t *testing.T, mcpPath string) {}},
		{name: "present and valid mcp ingests", setup: func(t *testing.T, mcpPath string) {
			plantContent(t, mcpPath, `{"mcpServers":{"gh":{"command":"npx"}}}`)
		}, wantSrv: 1},
		{name: "corrupt-but-present mcp fails loud", setup: func(t *testing.T, mcpPath string) { plantContent(t, mcpPath, "{ not valid json") }, wantErr: true},
		{name: "present but unreadable mcp fails loud", setup: func(t *testing.T, mcpPath string) { plantDir(t, mcpPath) }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			mcpPath := filepath.Join(tmp, ".g", "mcp.json")
			tt.setup(t, mcpPath)
			a := generic.New(readErrSpec(), generic.Options{TargetRoot: tmp})
			got, err := a.Ingest(adapter.ScopeUser, "")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a loud ingest error for a corrupt/unreadable mcp file, got nil (mcp=%+v)", got.MCPServers)
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

// TestIngest_UnreadableSkillsDir_FailsLoud: a present-but-unreadable skills root
// fails loud. The per-skill SKILL.md skip stays a documented silent no-op, but the
// directory-level read no longer swallows a real error into an empty skill set.
func TestIngest_UnreadableSkillsDir_FailsLoud(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, skillsDir string)
		wantErr    bool
		wantSkills int
	}{
		{name: "absent skills dir is a silent skip", setup: func(t *testing.T, skillsDir string) {}},
		{name: "present and valid skill ingests", setup: func(t *testing.T, skillsDir string) {
			plantContent(t, filepath.Join(skillsDir, "myskill", "SKILL.md"), "---\nname: myskill\ndescription: A skill.\n---\nDo the thing.\n")
		}, wantSkills: 1},
		{name: "present but unreadable skills dir fails loud", setup: func(t *testing.T, skillsDir string) { plantFileAtDir(t, skillsDir) }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			skillsDir := filepath.Join(tmp, ".g", "skills")
			tt.setup(t, skillsDir)
			a := generic.New(readErrSpec(), generic.Options{TargetRoot: tmp})
			got, err := a.Ingest(adapter.ScopeUser, "")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a loud ingest error for an unreadable skills dir, got nil (skills=%+v)", got.Skills)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.Skills) != tt.wantSkills {
				t.Fatalf("skills: got %d want %d (%+v)", len(got.Skills), tt.wantSkills, got.Skills)
			}
		})
	}
}
