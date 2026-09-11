package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/marketplace"
)

// TestSwapDir pins the replace both cache callers rely on — the plugin-upgrade
// cache swap and the marketplace re-slot (#233): the old tree must survive
// until the new one is standing in its place, so a swap that fails part-way
// leaves the cache the caller had rather than none, and a completed swap leaves
// neither the source nor the tree it moved aside.
func TestSwapDir(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, src, dst string)
		wantErr bool
		// wantDst is the marker the destination must hold afterwards.
		wantDst string
	}{
		{
			name: "replaces the old tree with the new one",
			setup: func(t *testing.T, src, dst string) {
				mustWrite(t, filepath.Join(src, "marker.txt"), "fresh")
				mustWrite(t, filepath.Join(dst, "marker.txt"), "stale")
			},
			wantDst: "fresh",
		},
		{
			name: "installs the new tree when nothing is at the destination",
			setup: func(t *testing.T, src, _ string) {
				mustWrite(t, filepath.Join(src, "marker.txt"), "fresh")
			},
			wantDst: "fresh",
		},
		{
			// The forcing function is a missing source: the old tree has already
			// been moved aside when the rename into place fails, and it must
			// come back rather than stay aside or be discarded.
			name: "keeps the old tree when the new one cannot be moved in",
			setup: func(t *testing.T, _, dst string) {
				mustWrite(t, filepath.Join(dst, "marker.txt"), "stale")
			},
			wantErr: true,
			wantDst: "stale",
		},
		{
			name: "clears a leftover aside from an interrupted earlier swap",
			setup: func(t *testing.T, src, dst string) {
				mustWrite(t, filepath.Join(src, "marker.txt"), "fresh")
				mustWrite(t, filepath.Join(dst, "marker.txt"), "stale")
				mustWrite(t, filepath.Join(dst+marketplace.CacheAsideSuffix, "marker.txt"), "older")
			},
			wantDst: "fresh",
		},
		{
			// An earlier replace that could not put the old tree back (or was
			// interrupted before it could) leaves it at the aside with nothing
			// at the destination. A retry must put it back before proceeding,
			// so that a second failure restores it rather than losing it.
			name: "puts a stranded old tree back when the new one cannot be moved in",
			setup: func(t *testing.T, _, dst string) {
				mustWrite(t, filepath.Join(dst+marketplace.CacheAsideSuffix, "marker.txt"), "stranded")
			},
			wantErr: true,
			wantDst: "stranded",
		},
		{
			name: "replaces a stranded old tree when nothing else is at the destination",
			setup: func(t *testing.T, src, dst string) {
				mustWrite(t, filepath.Join(src, "marker.txt"), "fresh")
				mustWrite(t, filepath.Join(dst+marketplace.CacheAsideSuffix, "marker.txt"), "stranded")
			},
			wantDst: "fresh",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			src, dst := filepath.Join(root, "incoming"), filepath.Join(root, "cache")
			tc.setup(t, src, dst)

			err := swapDir(src, dst)

			if (err != nil) != tc.wantErr {
				t.Fatalf("swapDir error = %v, wantErr %v", err, tc.wantErr)
			}
			got, rerr := os.ReadFile(filepath.Join(dst, "marker.txt"))
			if rerr != nil {
				t.Fatalf("the destination must hold a tree afterwards: %v", rerr)
			}
			if string(got) != tc.wantDst {
				t.Errorf("destination marker.txt = %q, want %q", got, tc.wantDst)
			}
			if _, lerr := os.Lstat(dst + marketplace.CacheAsideSuffix); !os.IsNotExist(lerr) {
				t.Errorf("no aside may survive the swap: %s%s (err=%v)", dst, marketplace.CacheAsideSuffix, lerr)
			}
			if _, lerr := os.Lstat(src); !tc.wantErr && !os.IsNotExist(lerr) {
				t.Errorf("a completed swap must consume the source: %s (err=%v)", src, lerr)
			}
		})
	}
}

// TestSwapDir_KeepsBothTreesWhenTheRenameInFails forces the failure the
// rollback exists for with a source the swap cannot move: a tree on another
// filesystem (/dev/shm is a tmpfs on Linux; the test skips where it is missing,
// unwritable, or on the same filesystem as the test's temp dir). Unlike a
// missing source, this tree survives the failed rename, so the arm also pins
// that the source is left where it was — and it is the one witness the closed
// window has: a swap that checks the source first and then removes the
// destination passes every other test in the package.
func TestSwapDir_KeepsBothTreesWhenTheRenameInFails(t *testing.T) {
	shm, err := os.MkdirTemp("/dev/shm", "agentsync-swapdir-")
	if err != nil {
		t.Skipf("no writable /dev/shm to force a cross-device rename: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shm) })
	// A /dev/shm that exists but cannot take a file (full, or mounted with
	// size=0) is a reason to skip, not a failure of the swap.
	writeOrSkip := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Skipf("cannot write under /dev/shm to force a cross-device rename: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Skipf("cannot write under /dev/shm to force a cross-device rename: %v", err)
		}
	}
	root := t.TempDir()
	probe := filepath.Join(shm, "probe")
	writeOrSkip(filepath.Join(probe, "x"), "x")
	if err := os.Rename(probe, filepath.Join(root, "probe")); err == nil {
		t.Skip("/dev/shm and the test temp dir are one filesystem; a rename between them cannot fail")
	}
	src, dst := filepath.Join(shm, "incoming"), filepath.Join(root, "cache")
	writeOrSkip(filepath.Join(src, "marker.txt"), "fresh")
	mustWrite(t, filepath.Join(dst, "marker.txt"), "stale")

	err = swapDir(src, dst)

	// The failure must be the rename that moves the new tree IN — the one the
	// restore exists for — not an earlier step that touched nothing.
	var le *os.LinkError
	if !errors.As(err, &le) || le.Old != src {
		t.Fatalf("the swap must fail at moving the new tree in (rename %s → %s); got: %v", src, dst, err)
	}
	if got, rerr := os.ReadFile(filepath.Join(dst, "marker.txt")); rerr != nil || string(got) != "stale" {
		t.Errorf("the old tree must be put back; marker.txt = %q (err=%v)", got, rerr)
	}
	if got, rerr := os.ReadFile(filepath.Join(src, "marker.txt")); rerr != nil || string(got) != "fresh" {
		t.Errorf("the source must be left where it was; marker.txt = %q (err=%v)", got, rerr)
	}
	if _, lerr := os.Lstat(dst + marketplace.CacheAsideSuffix); !os.IsNotExist(lerr) {
		t.Errorf("no aside may survive the swap (err=%v)", lerr)
	}
}
