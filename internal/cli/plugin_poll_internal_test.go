package cli

import (
	"os"
	"path/filepath"
	"testing"
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
				mustWrite(t, filepath.Join(dst+"..old", "marker.txt"), "older")
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
			if _, lerr := os.Lstat(dst + "..old"); !os.IsNotExist(lerr) {
				t.Errorf("no aside may survive the swap: %s..old (err=%v)", dst, lerr)
			}
			if _, lerr := os.Lstat(src); !tc.wantErr && !os.IsNotExist(lerr) {
				t.Errorf("a completed swap must consume the source: %s (err=%v)", src, lerr)
			}
		})
	}
}
