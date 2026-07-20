package claude_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
)

// planterDir creates a DIRECTORY at path (making parents) so a later os.ReadFile
// of that path returns a non-os.IsNotExist error (EISDIR / "is a directory").
// This "present-but-unreadable" fixture is deterministic and root-proof — unlike
// chmod 0o000, which the non-root-but-privileged container user can still read —
// so it exercises the read-error branch that must fail loudly rather than read as
// "component absent". Anchored to a real on-disk condition per the CLAUDE.md
// fidelity rule (a memmap/model round-trip cannot produce a real read error).
func planterDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
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

// TestIngest_UnreadableMemory_FailsLoud proves the core C6 fix: a present-but-
// unreadable CLAUDE.md fails the ingest loudly instead of silently yielding an
// empty Memory.Body that drift/reconcile could misread as "the user cleared
// their memory" and write nothing back over the canonical source.
func TestIngest_UnreadableMemory_FailsLoud(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, memPath string)
		wantErr  bool
		wantBody string
	}{
		{
			name:  "absent memory is a silent skip (legitimate not-configured case)",
			setup: func(t *testing.T, memPath string) {},
		},
		{
			name:     "present and valid memory ingests",
			setup:    func(t *testing.T, memPath string) { writeFile(t, memPath, "# Mem\n\nBody.\n") },
			wantBody: "# Mem\n\nBody.\n",
		},
		{
			name:    "present but unreadable memory fails loud (never empty)",
			setup:   func(t *testing.T, memPath string) { planterDir(t, memPath) },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			memPath := filepath.Join(tmp, ".claude", "CLAUDE.md")
			tt.setup(t, memPath)
			a := claude.New(claude.Options{TargetRoot: tmp})
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

// TestIngest_UnreadableSettings_HooksFailLoud is the regression that would have
// caught the MCP-loud / hooks-silent asymmetry: a corrupt-but-present
// settings.json now makes the hooks read fail loudly (matching the MCP block),
// rather than swallowing the parse error and reporting "no hooks" — which drift
// could classify as "hooks cleared". A valid settings.json with no hooks key
// stays a clean, error-free no-op.
func TestIngest_UnreadableSettings_HooksFailLoud(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, settingsPath string)
		wantErr bool
	}{
		{
			name:  "absent settings.json is a clean no-op",
			setup: func(t *testing.T, settingsPath string) {},
		},
		{
			name:  "valid settings.json with no hooks key is a clean no-op",
			setup: func(t *testing.T, settingsPath string) { writeFile(t, settingsPath, `{"model":"opus"}`) },
		},
		{
			name:    "corrupt-but-present settings.json fails loud (was silently swallowed)",
			setup:   func(t *testing.T, settingsPath string) { writeFile(t, settingsPath, "{ not valid json") },
			wantErr: true,
		},
		{
			name:    "present but unreadable settings.json fails loud",
			setup:   func(t *testing.T, settingsPath string) { planterDir(t, settingsPath) },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			settingsPath := filepath.Join(tmp, ".claude", "settings.json")
			tt.setup(t, settingsPath)
			a := claude.New(claude.Options{TargetRoot: tmp})
			got, err := a.Ingest(adapter.ScopeUser, "")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a loud ingest error for a corrupt/unreadable settings.json, got nil (hooks=%+v)", got.Hooks)
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
