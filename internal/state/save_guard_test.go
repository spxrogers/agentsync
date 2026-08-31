package state_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/state"
)

// TestSave_RefusesNonUTF8Keys pins the JSON-boundary guard. state.Key's own
// encoding is byte-safe — ParseKey(k.String()) == k for ANY bytes — but
// targets.json is JSON, and encoding/json rewrites an invalid UTF-8 byte in a
// string to U+FFFD. Persisting such a key would write a length prefix that no
// longer matches its payload, so the next Load would refuse the whole file — and
// the remedy that error suggests would not converge, because the next apply
// rebuilds and re-writes the same mangled key. Save refuses at creation time
// instead, and names the DESTINATION PATH, which is the thing the user can
// actually change: Linux paths are byte strings, so $HOME, a project root or a
// filename may legally contain a raw 0xFF.
func TestSave_RefusesNonUTF8Keys(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*state.Targets)
		wantPath string
	}{
		{
			name: "invalid byte in a files key path",
			mutate: func(tg *state.Targets) {
				tg.Files[state.Key{Agent: "claude", Scope: "user", Path: "${HOME}/bad\xffdir/CLAUDE.md"}] = state.FileEntry{SHA256: "a"}
			},
			wantPath: `${HOME}/bad\xffdir/CLAUDE.md`,
		},
		{
			name: "invalid byte in a project root",
			mutate: func(tg *state.Targets) {
				tg.Files[state.Key{
					Agent: "claude", Scope: "project",
					Project: "${HOME}/we\xffird", Path: "${HOME}/we\xffird/.mcp.json",
				}] = state.FileEntry{SHA256: "b"}
			},
			wantPath: `${HOME}/we\xffird/.mcp.json`,
		},
		{
			name: "invalid byte in a keys entry pointer",
			mutate: func(tg *state.Targets) {
				tg.Keys[state.Key{
					Agent: "claude", Scope: "user",
					Path: "${HOME}/.claude.json", Pointer: "/mcpServers/b\xffd",
				}] = state.KeyEntry{SHA256: "c"}
			},
			wantPath: "${HOME}/.claude.json",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "targets.json")
			tg := state.New()
			tc.mutate(tg)
			err := state.Save(p, tg)
			if err == nil {
				t.Fatal("Save must refuse a key the JSON container cannot carry losslessly")
			}
			if !errors.Is(err, state.ErrNonUTF8Key) {
				t.Fatalf("error must match state.ErrNonUTF8Key; got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantPath) {
				t.Fatalf("error must name the offending path %q; got %q", tc.wantPath, err)
			}
			// Save runs after the caller has already written its destinations, so
			// the message must say the run's writes landed — otherwise "rename it
			// and re-run" reads as if nothing happened.
			if !strings.Contains(err.Error(), "anything this run wrote is on disk and correct") {
				t.Fatalf("error must say the run's writes already landed; got %q", err)
			}
			if _, statErr := os.Stat(p); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("Save must refuse BEFORE writing; stat(%s) = %v", p, statErr)
			}
		})
	}
}

// TestSave_AcceptsColonAndPipeKeys is the negative control: the guard rejects
// only invalid UTF-8, never the ':' and '|' bytes the length-prefixed encoding
// exists to carry.
func TestSave_AcceptsColonAndPipeKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "targets.json")
	tg := state.New()
	tg.Files[state.Key{
		Agent: "claude", Scope: "project",
		Project: "${HOME}/work/app:staging", Path: "${HOME}/work/app:staging/.mcp|json",
	}] = state.FileEntry{SHA256: "a"}
	tg.Keys[state.Key{
		Agent: "claude", Scope: "user",
		Path: "${HOME}/.claude.json", Pointer: "/mcpServers/gh",
	}] = state.KeyEntry{SHA256: "b"}
	if err := state.Save(p, tg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := state.Load(p)
	if err != nil {
		t.Fatalf("Load of the saved file: %v", err)
	}
	if len(back.Files) != 1 || len(back.Keys) != 1 {
		t.Fatalf("round-trip lost entries: %+v", back)
	}
}
