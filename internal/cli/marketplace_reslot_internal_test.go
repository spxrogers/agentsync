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
// is the routine one: os.Rename onto a non-empty directory fails with ENOTEMPTY,
// so before the fix EVERY re-add kept the STALE tree and orphaned the fresh one.
// Replacing that destination must never cost a marketplace a cache it already
// has: not when the source is missing, and not when the destination IS the
// source under another spelling (a case-insensitive filesystem).
func TestReslotMarketplaceCache(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, root string) (from, to string)
		wantErr string // substring; "" means the move must succeed
		// after runs once the error has been checked, for arms whose contract
		// is about what the failure did NOT touch.
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
			name: "propagates a failure to move the tree",
			setup: func(t *testing.T, root string) (string, string) {
				// Nothing at `from`: the rename cannot succeed.
				return filepath.Join(root, "slug"), filepath.Join(root, "declared")
			},
			wantErr: "move marketplace cache",
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
			// On a case-insensitive filesystem (macOS, Windows) a slug and a
			// declared name that differ only in case are ONE directory; this
			// Linux-only suite stands that in with identical paths. A replace
			// here would remove the fresh tree and then fail to rename what is
			// gone; the move must adopt the spelling and keep the tree.
			name: "adopts the declared spelling when the destination already names the fetched tree",
			setup: func(t *testing.T, root string) (string, string) {
				from := filepath.Join(root, "slug")
				mustWrite(t, filepath.Join(from, "marker.txt"), "fresh")
				return from, from
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
