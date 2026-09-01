//go:build unix

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
			// The sentinel is pathless on the rationale that every caller wraps
			// it with the path itself. This is the caller that does, and the one
			// whose message a user reads mid-prompt, so both halves are pinned
			// here — otherwise dropping the wrap leaves the suite green and the
			// rationale untested.
			if !strings.Contains(err.Error(), p) {
				t.Errorf("error = %q, want it to name the destination %q: errDestNotRegular "+
					"carries no path, so this caller must supply it", err, p)
			}
			if !strings.Contains(err.Error(), "[i]gnore") || !strings.Contains(err.Error(), "re-run") {
				t.Errorf("error = %q, want a next step: the user is mid-prompt choosing a "+
					"keystroke, and this function's other refusals all name one", err)
			}
			// The remedy must not be one that hangs. This function's peers all
			// offer [o]verride, and an earlier version of this message copied
			// them — but [o] re-applies through render.Writer.Write's unguarded
			// convergence read, so on a non-regular destination it wedges
			// instead of failing (#241). Suggesting it is worse than saying
			// nothing, and this assertion is what stops it coming back by
			// symmetry with the neighbours.
			if strings.Contains(err.Error(), "[o]verride") {
				t.Errorf("error = %q recommends [o]verride, which HANGS on a non-regular "+
					"destination (#241) — offer it again only once that is fixed", err)
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

// TestHashFileSentinels pins hashFile's three-way answer, which every drift
// verdict in this package is built on. The values are opaque — callers only
// ever compare them for equality — so a changed sentinel does not fail a build
// or a type check. It silently changes what drift.Classify decides, at four
// call sites.
//
// Two of these rows cover behavior that a mutation sweep found UNPINNED: the
// whole suite stayed green with hashFile returning the shape sentinel for every
// error (absent included), and green again with the symlink arm removed
// entirely.
func TestHashFileSentinels(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, tmp string) string
		want  string
	}{
		{
			// The absent contract. "" is what drift.Classify reads as "no
			// destination", which is what makes an unrendered-but-recorded file
			// an Orphan rather than drift. Returning the shape sentinel here
			// instead would silently reclassify every absent destination — and
			// the ENOENT branch is taken 43 times in this package's own suite,
			// none of which asserted the value.
			name:  "an absent destination hashes to the empty sentinel",
			setup: func(t *testing.T, tmp string) string { return filepath.Join(tmp, "gone") },
			want:  "",
		},
		{
			// The symlink arm, which runs BEFORE the shape gate and is the one
			// place status and diff deliberately disagree: this refuses the
			// link, while readDestBytes (and so diff) follows it. destread.go's
			// doc comment asserts exactly that divergence; this is the test
			// behind the claim.
			name: "a symlink to a regular file is refused as a symlink, not followed",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				target := filepath.Join(tmp, "target")
				if err := os.WriteFile(target, []byte("payload"), 0o644); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(tmp, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			want: "symlink-not-regular-file",
		},
		{
			name:  "a FIFO is refused by shape",
			setup: mkfifoDest,
			want:  "not-a-regular-file",
		},
		{
			name: "an ordinary regular file hashes its content",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				p := filepath.Join(tmp, "dest")
				if err := os.WriteFile(p, []byte("payload"), 0o644); err != nil {
					t.Fatal(err)
				}
				return p
			},
			want: hashContent([]byte("payload")),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t, t.TempDir())
			// Bounded for the same reason as everything else here: a regression
			// that reintroduces an unguarded read hangs rather than fails.
			got := make(chan string, 1)
			go func() { got <- hashFile(path) }()
			select {
			case h := <-got:
				if h != tc.want {
					t.Errorf("hashFile = %q, want %q — these sentinels are compared only for "+
						"equality, so changing one silently changes drift.Classify's verdict",
						h, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("hashFile BLOCKED on %s", path)
			}
		})
	}

	// The divergence destread.go documents, asserted in both directions in one
	// place so the claim cannot rot: hashFile refuses the link, readDestBytes
	// reads through it.
	t.Run("readDestBytes follows the symlink hashFile refuses", func(t *testing.T) {
		tmp := t.TempDir()
		target := filepath.Join(tmp, "target")
		if err := os.WriteFile(target, []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(tmp, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		data, err := readDestBytes(link)
		if err != nil || string(data) != "payload" {
			t.Fatalf("readDestBytes(symlink) = (%q, %v), want the target's content: this "+
				"asymmetry with hashFile is what makes status report drift on a symlinked "+
				"destination while diff reads through it, and destread.go says so", data, err)
		}
	})
}
