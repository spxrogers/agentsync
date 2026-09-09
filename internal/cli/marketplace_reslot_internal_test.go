package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/marketplace"
)

// TestReslotMarketplaceCache covers the move that puts a freshly-fetched cache
// under the marketplace's DECLARED name (#233): that name is what every later
// lookup derives the cache dir from, so a move that does not happen — or happens
// only partially — must never be reported as success. The stale-destination case
// is the routine one: os.Rename onto a different existing directory fails, so
// before the fix EVERY re-add kept the STALE tree and orphaned the fresh one.
// Replacing that destination must never cost a marketplace a cache it already
// has: not when the source is missing or is a link rather than a tree, and not
// when the destination IS the source under another spelling (a case-insensitive
// filesystem).
func TestReslotMarketplaceCache(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, root string) (from, to string)
		wantErr string // substring; "" means the move must succeed
		// after runs once the outcome has been checked, for arms whose contract
		// is also about what the move did NOT touch.
		after func(t *testing.T, from, to string)
	}{
		{
			name: "moves the fetched tree under the declared name",
			setup: func(t *testing.T, root string) (string, string) {
				from := filepath.Join(root, "slug")
				mustWrite(t, filepath.Join(from, "marker.txt"), "fresh")
				return from, filepath.Join(root, "declared")
			},
		},
		{
			name: "replaces a stale cache left by an earlier add",
			setup: func(t *testing.T, root string) (string, string) {
				from := filepath.Join(root, "slug")
				mustWrite(t, filepath.Join(from, "marker.txt"), "fresh")
				to := filepath.Join(root, "declared")
				mustWrite(t, filepath.Join(to, "marker.txt"), "stale")
				mustWrite(t, filepath.Join(to, "gone.txt"), "stale")
				return from, to
			},
			after: func(t *testing.T, _, to string) {
				// The stale tree is renamed aside while the fresh one moves in and
				// discarded only afterwards; a completed replace leaves no aside.
				if _, err := os.Lstat(to + marketplace.CacheAsideSuffix); !os.IsNotExist(err) {
					t.Errorf("a completed replace must discard the tree it moved aside: %s%s (err=%v)", to, marketplace.CacheAsideSuffix, err)
				}
			},
		},
		{
			name: "propagates a failure to prepare the cache root",
			setup: func(t *testing.T, root string) (string, string) {
				from := filepath.Join(root, "slug")
				mustWrite(t, filepath.Join(from, "marker.txt"), "fresh")
				// A regular file where the cache root should be: MkdirAll fails.
				notADir := filepath.Join(root, "not-a-dir")
				mustWrite(t, notADir, "x")
				return from, filepath.Join(notADir, "declared")
			},
			wantErr: "prepare marketplace cache dir",
		},
		{
			name: "leaves an existing destination alone when there is nothing to move",
			setup: func(t *testing.T, root string) (string, string) {
				to := filepath.Join(root, "declared")
				mustWrite(t, filepath.Join(to, "stale.txt"), "stale")
				return filepath.Join(root, "slug"), to
			},
			wantErr: "move marketplace cache",
			after: func(t *testing.T, _, to string) {
				if _, err := os.Stat(filepath.Join(to, "stale.txt")); err != nil {
					t.Errorf("a missing source must not cost the marketplace its existing cache; stale.txt under %s: %v", to, err)
				}
			},
		},
		{
			// A link where the fetched tree should be is not a tree to move: no
			// fetcher leaves one, and installing the link as the cache (a link to
			// the destination itself would dangle once the stale tree is gone)
			// must be refused with the destination untouched.
			name: "refuses a symlink at the source rather than moving the link",
			setup: func(t *testing.T, root string) (string, string) {
				to := filepath.Join(root, "declared")
				mustWrite(t, filepath.Join(to, "stale.txt"), "stale")
				from := filepath.Join(root, "slug")
				if err := os.Symlink(to, from); err != nil {
					t.Fatal(err)
				}
				return from, to
			},
			wantErr: "move marketplace cache",
			after: func(t *testing.T, from, to string) {
				if _, err := os.Stat(filepath.Join(to, "stale.txt")); err != nil {
					t.Errorf("a refused move must leave the existing cache alone; stale.txt under %s: %v", to, err)
				}
				if fi, err := os.Lstat(to); err != nil || fi.Mode()&os.ModeSymlink != 0 {
					t.Errorf("the destination must still be the real tree, not a link (err=%v)", err)
				}
				if fi, err := os.Lstat(from); err != nil || fi.Mode()&os.ModeSymlink == 0 {
					t.Errorf("the refused link must be left where it was (err=%v)", err)
				}
			},
		},
		{
			// The plain rename moves a link into an EMPTY slot as happily as a
			// tree — the first-add path — so the refusal has to come before it,
			// not only once a stale tree has made the rename fail.
			name: "refuses a symlink at the source even when nothing is at the destination",
			setup: func(t *testing.T, root string) (string, string) {
				mustWrite(t, filepath.Join(root, "elsewhere", "keep.txt"), "keep")
				from := filepath.Join(root, "slug")
				if err := os.Symlink(filepath.Join(root, "elsewhere"), from); err != nil {
					t.Fatal(err)
				}
				return from, filepath.Join(root, "declared")
			},
			wantErr: "move marketplace cache",
			after: func(t *testing.T, from, to string) {
				if _, err := os.Lstat(to); !os.IsNotExist(err) {
					t.Errorf("the link must not be installed as the cache: %s exists (err=%v)", to, err)
				}
				if fi, err := os.Lstat(from); err != nil || fi.Mode()&os.ModeSymlink == 0 {
					t.Errorf("the refused link must be left where it was (err=%v)", err)
				}
			},
		},
		{
			// On a case-insensitive filesystem (macOS, Windows) a slug and a
			// declared name that differ only in case are ONE directory; this
			// Linux-only suite stands that in with identical paths, which reach
			// the same guard. A replace here would remove the fresh tree and then
			// fail to rename what is gone; the tree must be left in place.
			name: "keeps the tree when the destination already is the fetched tree",
			setup: func(t *testing.T, root string) (string, string) {
				from := filepath.Join(root, "slug")
				mustWrite(t, filepath.Join(from, "marker.txt"), "fresh")
				return from, from
			},
		},
		{
			// A symlink standing at the destination is a LINK to unlink, not the
			// tree under another name, and replacing it must never reach through
			// to whatever it points at.
			name: "replaces a symlink at the destination without touching its target",
			setup: func(t *testing.T, root string) (string, string) {
				from := filepath.Join(root, "slug")
				mustWrite(t, filepath.Join(from, "marker.txt"), "fresh")
				mustWrite(t, filepath.Join(root, "elsewhere", "keep.txt"), "keep")
				to := filepath.Join(root, "declared")
				if err := os.Symlink(filepath.Join(root, "elsewhere"), to); err != nil {
					t.Fatal(err)
				}
				return from, to
			},
			after: func(t *testing.T, _, to string) {
				if _, err := os.Stat(filepath.Join(filepath.Dir(to), "elsewhere", "keep.txt")); err != nil {
					t.Errorf("replacing a symlinked destination must unlink the link, never its target: %v", err)
				}
				if fi, err := os.Lstat(to); err != nil || fi.Mode()&os.ModeSymlink != 0 {
					t.Errorf("the destination must now be the real tree, not a link (err=%v)", err)
				}
			},
		},
		{
			// A symlink at the destination that points at the SOURCE is what a
			// following stat would mistake for "already in place": the tree must
			// still move under the declared name, or the slug directory becomes
			// an orphan behind a link that `marketplace remove` unlinks alone.
			name: "replaces a symlink at the destination even when it points at the source",
			setup: func(t *testing.T, root string) (string, string) {
				from := filepath.Join(root, "slug")
				mustWrite(t, filepath.Join(from, "marker.txt"), "fresh")
				to := filepath.Join(root, "declared")
				if err := os.Symlink(from, to); err != nil {
					t.Fatal(err)
				}
				return from, to
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			from, to := tc.setup(t, root)

			err := reslotMarketplaceCache(from, to)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("re-slot must report the failure, not swallow it; got nil error")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error must name the failing step %q; got: %v", tc.wantErr, err)
				}
				if tc.after != nil {
					tc.after(t, from, to)
				}
				return
			}
			if err != nil {
				t.Fatalf("re-slot: %v", err)
			}
			got, rerr := os.ReadFile(filepath.Join(to, "marker.txt"))
			if rerr != nil {
				t.Fatalf("read the re-slotted cache: %v", rerr)
			}
			if string(got) != "fresh" {
				t.Errorf("declared-name cache must hold the FRESHLY fetched tree; marker.txt = %q, want %q", got, "fresh")
			}
			if _, err := os.Lstat(filepath.Join(to, "gone.txt")); err == nil {
				t.Errorf("a stale cache must be replaced, not merged: gone.txt survived at %s", to)
			}
			if _, err := os.Lstat(from); from != to && !os.IsNotExist(err) {
				t.Errorf("the slug directory must not survive the move (it would be an orphan cache): %s (err=%v)", from, err)
			}
			if tc.after != nil {
				tc.after(t, from, to)
			}
		})
	}
}

// TestAddMarketplaceSource_ReslotFailureRegistersNothing pins the whole-command
// half of #233: when the cache cannot be re-slotted under the declared name,
// `marketplace add` must FAIL rather than write marketplaces/<name>.toml and a
// state entry pointing at a cache that is not there. The forcing function is a
// hostile marketplace.json — a 300-character declared name is one path segment
// and every Linux filesystem caps a name at 255 bytes, so the move fails with
// ENAMETOOLONG anywhere this runs. Before the fix the failure was swallowed and
// surfaced later as a different error from the TOML write, which is why the
// assertion names the re-slot error rather than accepting "some error".
func TestAddMarketplaceSource_ReslotFailureRegistersNothing(t *testing.T) {
	home := t.TempDir()
	fixture := filepath.Join(t.TempDir(), "fixture-mp")
	longName := strings.Repeat("n", 300)
	mustWrite(t, filepath.Join(fixture, ".claude-plugin", "marketplace.json"),
		`{"name": "`+longName+`", "owner": {"name": "x"}, "plugins": []}`)

	_, _, err := addMarketplaceSource(home, marketplace.Source{Relative: fixture}, fixture, func(string, ...any) {})
	if err == nil {
		t.Fatalf("add must fail when the cache cannot be re-slotted under the declared name; got nil error")
	}
	if !strings.Contains(err.Error(), "move marketplace cache") {
		t.Fatalf("the failure must be reported as the cache move it is; got: %v", err)
	}
	if entries, rerr := os.ReadDir(filepath.Join(home, "marketplaces")); rerr == nil && len(entries) != 0 {
		t.Errorf("a failed add must register nothing; marketplaces/ holds %d file(s)", len(entries))
	}
	if _, serr := os.Stat(filepath.Join(home, ".state", "targets.json")); serr == nil {
		t.Errorf("a failed add must record no state entry; %s exists", filepath.Join(home, ".state", "targets.json"))
	}
	if entries, rerr := os.ReadDir(filepath.Join(home, ".state", "cache", "marketplaces")); rerr == nil && len(entries) != 0 {
		t.Errorf("a failed add must leave no fetch cache behind (searchAllMarketplaces would offer it to a bare-id plugin add as an unregistered marketplace); cache root holds %d entr(y/ies)", len(entries))
	}
}

// TestSearchAllMarketplaces_SkipsCacheAsides pins the other half of the aside
// contract. swapDir parks the old tree at <name>..old while a replace is in
// flight, and an interrupted replace leaves it there; a bare-id `plugin add`
// must never resolve against that copy — it would register the plugin under a
// name no cache directory can be derived from, the #233 shape again.
func TestSearchAllMarketplaces_SkipsCacheAsides(t *testing.T) {
	home := t.TempDir()
	aside := filepath.Join(home, ".state", "cache", "marketplaces", "shared"+marketplace.CacheAsideSuffix)
	mustWrite(t, filepath.Join(aside, ".claude-plugin", "marketplace.json"),
		`{"name": "shared", "owner": {"name": "x"}, "plugins": [{"name": "ghost", "source": "./ghost"}]}`)

	if _, _, via, err := searchAllMarketplaces(home, "ghost"); err == nil {
		t.Fatalf("a cache aside must not be searched as a marketplace; ghost resolved via %q", via)
	}
}
