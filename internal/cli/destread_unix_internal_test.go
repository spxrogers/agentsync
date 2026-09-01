//go:build unix

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// TestReadDestBytesShape pins the gate every destination read now passes
// through.
//
// Every assertion here is bounded by a timeout, and that is the point rather
// than caution: this defect class does not FAIL, it HANGS. os.ReadFile on a
// FIFO blocks in the open waiting for a writer that never comes, so a test
// written as a plain call would wedge the suite with no diagnostic instead of
// reporting in 5s. Nothing in this file ever opens the FIFO it creates —
// mkfifo, chmod and os.Stat do not.
func TestReadDestBytesShape(t *testing.T) {
	cases := []struct {
		name string
		// setup returns the path to read. It must never OPEN that path.
		setup    func(t *testing.T, tmp string) string
		wantErr  error // errDestNotRegular, os.ErrNotExist, or nil
		wantData string
	}{
		{
			// The sharp one: present, stats fine, and its open never returns.
			name:    "a FIFO destination is refused by shape",
			setup:   mkfifoDest,
			wantErr: errDestNotRegular,
		},
		{
			name: "a directory destination is refused by shape",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				p := filepath.Join(tmp, "dest")
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErr: errDestNotRegular,
		},
		{
			// The fail-open row, and the reason the gate does not refuse on any
			// stat failure: an absent destination is ordinary, and every caller
			// already handles its ENOENT. A shape error here would name the
			// wrong problem.
			name: "an absent destination still reports the truthful ENOENT",
			setup: func(t *testing.T, tmp string) string {
				return filepath.Join(tmp, "absent")
			},
			wantErr: os.ErrNotExist,
		},
		{
			// The guard against an over-broad arm.
			name: "an ordinary regular destination is read",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				p := filepath.Join(tmp, "dest")
				if err := os.WriteFile(p, []byte("payload"), 0o644); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantData: "payload",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t, t.TempDir())

			type result struct {
				data []byte
				err  error
			}
			// By channel, not a captured variable, so a read that DID block
			// cannot race the assertions under -race.
			ch := make(chan result, 1)
			go func() {
				d, err := readDestBytes(path)
				ch <- result{d, err}
			}()
			var got result
			select {
			case got = <-ch:
			case <-time.After(5 * time.Second):
				t.Fatalf("readDestBytes BLOCKED on %s — the destination must be STAT'd for "+
					"shape before it is opened: os.ReadFile on a FIFO waits for a writer "+
					"that never comes, so the read's own error path never runs", path)
			}

			if tc.wantErr == nil {
				if got.err != nil {
					t.Fatalf("readDestBytes(%s) = %v, want success", path, got.err)
				}
				if string(got.data) != tc.wantData {
					t.Errorf("data = %q, want %q", got.data, tc.wantData)
				}
				return
			}
			if !errors.Is(got.err, tc.wantErr) {
				t.Fatalf("readDestBytes(%s) error = %v, want errors.Is(_, %v)", path, got.err, tc.wantErr)
			}
			if got.data != nil {
				t.Errorf("data = %q on an error, want nil", got.data)
			}
		})
	}
}

// TestKeyMergeAndWriteBackReadsAreGuarded covers the two callers whose own read
// is not reachable from an end-to-end CLI run in this package.
//
// readDestFile is the read ALL FOUR drift walks share for key-merge ops, so an
// unguarded one wedged `status` — a command advertised as read-only — on a
// FIFO-shaped ~/.claude.json. writeBackFileItem is the one a user reaches one
// keystroke LATER: with only the classification reads guarded, a FIFO
// destination classifies as drift, the user presses [w], and the hang lands
// there instead.
func TestKeyMergeAndWriteBackReadsAreGuarded(t *testing.T) {
	t.Run("readDestFile returns an empty map rather than blocking", func(t *testing.T) {
		p := mkfifoDest(t, t.TempDir())
		ch := make(chan map[string]any, 1)
		go func() { ch <- readDestFile("merge-json-keys", p) }()
		select {
		case got := <-ch:
			if len(got) != 0 {
				t.Errorf("readDestFile = %v, want an empty map", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("readDestFile BLOCKED on a FIFO destination (%s) — this is the read "+
				"every key-merge drift walk shares, including read-only `status`", p)
		}
	})

	t.Run("writeBackFileItem returns an error rather than blocking", func(t *testing.T) {
		p := mkfifoDest(t, t.TempDir())
		it := reconcileItem{op: adapter.FileOp{Path: p, SourceID: "demo"}}
		ch := make(chan error, 1)
		go func() { ch <- writeBackFileItem(t.TempDir(), it) }()
		select {
		case err := <-ch:
			if err == nil {
				t.Fatal("writeBackFileItem = nil, want an error: a FIFO carries no dest content to write back")
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("writeBackFileItem BLOCKED on a FIFO destination (%s) — [w] must refuse, "+
				"not wedge the reconcile session", p)
		}
	})
}

// mkfifoDest creates a 0600 FIFO at a destination path and returns it. mkfifo
// and chmod do not open the FIFO; nothing in this file ever does.
func mkfifoDest(t *testing.T, tmp string) string {
	t.Helper()
	p := filepath.Join(tmp, "dest")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
