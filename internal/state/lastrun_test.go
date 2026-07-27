package state_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// The run record is a UX marker with no authority over anything else, so its
// whole contract is "degrade usefully". These pin the degradations.

func TestLoadLastRun(t *testing.T) {
	testenv.RequireContainer(t)

	cases := []struct {
		name        string
		write       *string // nil = do not create the file
		wantCorrupt bool
		wantErr     bool
		check       func(t *testing.T, l *state.LastRun)
	}{
		{
			name:  "missing file is not an error",
			write: nil,
			check: func(t *testing.T, l *state.LastRun) {
				if l.Version != "" || len(l.NoticesSeen) != 0 {
					t.Errorf("missing file should yield the zero record, got %+v", l)
				}
				// The point of returning a usable value: no nil-check needed.
				if l.Seen("anything") {
					t.Error("a machine with no record has seen nothing")
				}
			},
		},
		{
			name:  "well-formed record round-trips",
			write: ptr(`{"version":"0.11.0","notices_seen":["a","b"]}`),
			check: func(t *testing.T, l *state.LastRun) {
				if l.Version != "0.11.0" || !l.Seen("a") || !l.Seen("b") {
					t.Errorf("unexpected record: %+v", l)
				}
				if l.Seen("c") {
					t.Error("Seen reported an id that is not in the record")
				}
			},
		},
		{
			name:  "JSON null yields a usable zero value",
			write: ptr(`null`),
			check: func(t *testing.T, l *state.LastRun) {
				if l.Seen("x") {
					t.Error("null record should have seen nothing")
				}
			},
		},
		{
			name:        "empty file is corrupt, not fatal",
			write:       ptr(``),
			wantCorrupt: true,
		},
		{
			name:        "truncated json is corrupt",
			write:       ptr(`{"version":"0.11.0","notices_`),
			wantCorrupt: true,
		},
		{
			name:        "wrong shape (array) is corrupt",
			write:       ptr(`["nope"]`),
			wantCorrupt: true,
		},
		{
			name:        "wrong field types are corrupt",
			write:       ptr(`{"version":42}`),
			wantCorrupt: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "last-run.json")
			if tc.write != nil {
				if err := os.WriteFile(p, []byte(*tc.write), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			got, err := state.LoadLastRun(p)
			// The contract that makes callers simple: NEVER nil, whatever happens.
			if got == nil {
				t.Fatal("LoadLastRun returned a nil record; callers rely on it never being nil")
			}
			if tc.wantCorrupt {
				if !errors.Is(err, state.ErrCorruptLastRun) {
					t.Fatalf("want ErrCorruptLastRun, got %v", err)
				}
				// A corrupt record must still hand back something usable, so the
				// caller can show the notice and overwrite the file.
				if got.Seen("anything") {
					t.Error("a corrupt record must not claim to have seen a notice")
				}
				return
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

// TestLoadLastRun_UnreadableIsAPlainError distinguishes "cannot read" from
// "read it, cannot parse" — the caller shows the notice for one and stays quiet
// for the other, so conflating them is the bug this separation prevents.
func TestLoadLastRun_UnreadableIsAPlainError(t *testing.T) {
	testenv.RequireContainer(t)
	dir := t.TempDir()
	// A path whose PARENT is a regular file: ReadFile fails ENOTDIR, which is
	// not os.ErrNotExist and not a parse failure.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := state.LoadLastRun(filepath.Join(blocker, "last-run.json"))
	if err == nil {
		t.Fatal("an unreadable path must report an error")
	}
	if errors.Is(err, state.ErrCorruptLastRun) {
		t.Error("an I/O failure must NOT be reported as a corrupt record: the caller treats them differently")
	}
	if got == nil {
		t.Error("LoadLastRun must never return a nil record")
	}
}

func TestMarkSeenAndSave(t *testing.T) {
	testenv.RequireContainer(t)

	t.Run("MarkSeen is idempotent and sorted", func(t *testing.T) {
		var l state.LastRun
		l.MarkSeen("b")
		l.MarkSeen("a")
		l.MarkSeen("b") // duplicate
		l.MarkSeen("")  // ignored
		if len(l.NoticesSeen) != 2 || l.NoticesSeen[0] != "a" || l.NoticesSeen[1] != "b" {
			t.Fatalf("want sorted unique [a b], got %v", l.NoticesSeen)
		}
	})

	t.Run("nil receiver is safe", func(t *testing.T) {
		var l *state.LastRun
		if l.Seen("x") {
			t.Error("nil record cannot have seen anything")
		}
		l.MarkSeen("x") // must not panic
	})

	t.Run("save then load round-trips", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "last-run.json")
		rec := &state.LastRun{Version: "0.11.0"}
		rec.MarkSeen("0.11.0-cli-surface")
		if err := state.SaveLastRun(p, rec); err != nil {
			t.Fatal(err)
		}
		got, err := state.LoadLastRun(p)
		if err != nil {
			t.Fatal(err)
		}
		if got.Version != "0.11.0" || !got.Seen("0.11.0-cli-surface") {
			t.Fatalf("round trip lost data: %+v", got)
		}
	})

	t.Run("saving nil is refused, not a panic", func(t *testing.T) {
		if err := state.SaveLastRun(filepath.Join(t.TempDir(), "x.json"), nil); err == nil {
			t.Error("SaveLastRun(nil) must return an error")
		}
	})
}

func ptr(s string) *string { return &s }
