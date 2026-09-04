//go:build unix

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/state"
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

// TestWriteBackFileItemMessageMatchesTheFailure pins that the remedy named in
// writeBackFileItem's refusal depends on WHY the read failed.
//
// The shape sentence ("remove or replace the non-regular file") was once
// appended to every readDestBytes error, so deleting a managed file — the
// common case, which is itself drift and offers [w] — produced
// "no such file or directory — remove or replace the non-regular file at that
// path", advice that describes a situation the user is not in.
func TestWriteBackFileItemMessageMatchesTheFailure(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "deleted.md")
	err := writeBackFileItem(t.TempDir(), reconcileItem{op: adapter.FileOp{Path: absent, SourceID: "demo"}})
	if err == nil {
		t.Fatal("writeBackFileItem = nil for an absent destination, want an error")
	}
	if strings.Contains(err.Error(), "non-regular") {
		t.Errorf("error = %q calls an ABSENT destination non-regular; the remedy must match "+
			"the failure that actually occurred", err)
	}
	if !strings.Contains(err.Error(), "[i]gnore") {
		t.Errorf("error = %q, want it to still name a next step", err)
	}
	// [o]verride is left out of the advice only for a NON-REGULAR destination,
	// where it would hang (#241). For an absent one it is safe — Writer.Write's
	// convergence read gets ENOENT and falls through to the write — and it is
	// the actual fix, so leaving it out here would deny the user the remedy
	// that works.
	if !strings.Contains(err.Error(), "[o]verride") {
		t.Errorf("error = %q, want it to offer [o]verride: the destination is absent, not "+
			"non-regular, so re-applying canonical is safe and is the fix", err)
	}
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
			// The symlink arm, which runs BEFORE the shape gate: with
			// AGENTSYNC_ALLOW_SYMLINK_DEST unset (the suite's state) a link is
			// refused, not followed — the same policy under which apply
			// refuses to write through it. This is what every whole-file
			// surface classifies a refused link from; diff keys its "symlink"
			// hunk on the same value.
			name: "a symlink to a regular file is refused as a symlink when the env is unset",
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
			// The opt-in half of the same policy: with the env set the link is
			// resolved and the TARGET's content hashed — the file apply
			// converges through the link — so a chezmoi setup can classify
			// clean. t.Setenv is scoped to this subtest's t.
			name: "a symlink to a regular file is resolved when the env is set",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				t.Setenv(iox.AllowSymlinkDestEnv, "1")
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
			// A literal, for the same reason as the regular-file row below:
			// this is the digest of "payload", what state records for it.
			want: "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5",
		},
		{
			// Opted in, but the link does not resolve: a distinct sentinel, so
			// diff and reconcile can say "fix the link" instead of "set the
			// switch you already set". Pins destReadPath's EvalSymlinks arm.
			name: "a dangling link is unresolvable when the env is set",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				t.Setenv(iox.AllowSymlinkDestEnv, "1")
				link := filepath.Join(tmp, "link")
				if err := os.Symlink(filepath.Join(tmp, "gone"), link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			want: "symlink-target-unresolvable",
		},
		{
			// A link to a present non-regular target is a SHAPE problem whatever
			// the switch says: every surface names the FIFO, not the link, so
			// nobody is told to set a switch that would only reveal the FIFO.
			name: "a symlink to a FIFO is refused by shape even when the env is unset",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				t.Setenv(iox.AllowSymlinkDestEnv, "")
				if err := os.Unsetenv(iox.AllowSymlinkDestEnv); err != nil {
					t.Fatal(err)
				}
				fifo := mkfifoDest(t, tmp)
				link := filepath.Join(tmp, "link")
				if err := os.Symlink(fifo, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			want: "not-a-regular-file",
		},
		{
			name:  "a FIFO is refused by shape",
			setup: mkfifoDest,
			want:  "not-a-regular-file",
		},
		{
			// The parity row. A destination that cannot be STAT'd at all —
			// here because a parent component is a regular file, so the walk
			// gets ENOTDIR — answered "not-a-regular-file" before this package
			// had a read gate, and must still. Splitting it off to "" would
			// read as absent, moving such a destination from ForeignCollision
			// to New when nothing was applied — and New is SafeForAutoApply.
			name: "an unstattable destination keeps the shape sentinel",
			setup: func(t *testing.T, tmp string) string {
				t.Helper()
				blocker := filepath.Join(tmp, "notadir")
				if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(blocker, "dest")
			},
			want: "not-a-regular-file",
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
			// A literal, not hashContent([]byte("payload")): computing the
			// expectation with the function under test is a tautology — salting
			// hashContent leaves this row green. This digest is what a state
			// file records for that content.
			want: "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5",
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
						"equality — and diff now keys its shape hunk's prose on the shape one",
						h, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("hashFile BLOCKED on %s", path)
			}
		})
	}

	// The asymmetry destread.go documents, asserted in both directions in one
	// place so the claim cannot rot: hashFile (a WHOLE-FILE read) refuses the
	// link with the env unset, while readDestBytes still reads through it —
	// because it is also the key-merge read (readDestFile) and import's
	// state-seeding read, where the symlink policy deliberately does not
	// apply. The whole-file reads route through destReadPath before reaching
	// it; this one does not.
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
			t.Fatalf("readDestBytes(symlink) = (%q, %v), want the target's content: it is "+
				"the key-merge and import read, which decode through a link on every "+
				"surface as apply writes through it; only the whole-file reads apply "+
				"the symlink policy, and destread.go says so", data, err)
		}
		if got := hashFile(link); got != "symlink-not-regular-file" {
			t.Fatalf("hashFile(symlink) = %q with the env unset, want the symlink sentinel: "+
				"the whole-file read must NOT share readDestBytes' read-through", got)
		}
	})
}

// TestWriteBackFileItemRefusesASymlinkItIsNotReadingThrough pins the
// reconcile half of #229 axis 9. A whole-file destination the drift walk
// refused as a symlink reaches the prompt as drift; one keystroke later [w]
// must not read THROUGH the link and capture a file the classification never
// looked at. With the env unset the write-back refuses, names the switch and
// says apply and the four drift commands need it; with it set the write-back captures
// the linked file — so the gate follows the policy rather than always
// refusing.
func TestWriteBackFileItemRefusesASymlinkItIsNotReadingThrough(t *testing.T) {
	mkLink := func(t *testing.T) string {
		t.Helper()
		tmp := t.TempDir()
		target := filepath.Join(tmp, "target")
		if err := os.WriteFile(target, []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(tmp, "link.md")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		return link
	}

	t.Run("env unset: refuses, naming the switch and the commands that need it", func(t *testing.T) {
		t.Setenv(iox.AllowSymlinkDestEnv, "") // registers the restore
		if err := os.Unsetenv(iox.AllowSymlinkDestEnv); err != nil {
			t.Fatal(err)
		}
		home := t.TempDir()
		link := mkLink(t)
		err := writeBackFileItem(home, reconcileItem{op: adapter.FileOp{Path: link, SourceID: "demo"}})
		if err == nil {
			t.Fatal("writeBackFileItem = nil for a refused symlink, want an error: [w] must not " +
				"capture through a link the classification did not read through")
		}
		for _, want := range []string{link, iox.AllowSymlinkDestEnv + "=1", "for apply, status, diff, reconcile and explain", "[i]gnore"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to contain %q", err, want)
			}
		}
		// [o]verride re-applies through Writer.Write, whose mode arm chmods
		// the TARGET through the link (#248): never suggest it here.
		if strings.Contains(err.Error(), "[o]verride") {
			t.Errorf("error = %q offers [o]verride on a refused symlink", err)
		}
		if strings.Contains(err.Error(), "non-regular") {
			t.Errorf("error = %q calls a symlink non-regular; the remedy must match the failure", err)
		}
		if _, serr := os.Stat(filepath.Join(home, "demo")); serr == nil {
			t.Errorf("write-back wrote %s despite refusing", filepath.Join(home, "demo"))
		}
	})

	t.Run("env unset: a link to a FIFO takes the shape arm, whose advice omits [o]verride", func(t *testing.T) {
		t.Setenv(iox.AllowSymlinkDestEnv, "")
		if err := os.Unsetenv(iox.AllowSymlinkDestEnv); err != nil {
			t.Fatal(err)
		}
		tmp := t.TempDir()
		fifo := mkfifoDest(t, tmp)
		link := filepath.Join(tmp, "link.md")
		if err := os.Symlink(fifo, link); err != nil {
			t.Fatal(err)
		}
		err := writeBackFileItem(t.TempDir(), reconcileItem{op: adapter.FileOp{Path: link, SourceID: "demo"}})
		if err == nil || !errors.Is(err, errDestNotRegular) {
			t.Fatalf("writeBackFileItem = %v, want the shape refusal (errDestNotRegular) for a link to a FIFO", err)
		}
		if strings.Contains(err.Error(), "[o]verride") || strings.Contains(err.Error(), iox.AllowSymlinkDestEnv) {
			t.Errorf("error = %q: a link to a FIFO must get the shape remedy, not the symlink one", err)
		}
	})

	t.Run("env set: a dangling link is unresolvable and the advice does not name the switch", func(t *testing.T) {
		t.Setenv(iox.AllowSymlinkDestEnv, "1")
		tmp := t.TempDir()
		link := filepath.Join(tmp, "link.md")
		if err := os.Symlink(filepath.Join(tmp, "gone"), link); err != nil {
			t.Fatal(err)
		}
		err := writeBackFileItem(t.TempDir(), reconcileItem{op: adapter.FileOp{Path: link, SourceID: "demo"}})
		if err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
			t.Fatalf("writeBackFileItem = %v, want the unresolvable-link refusal", err)
		}
		if strings.Contains(err.Error(), iox.AllowSymlinkDestEnv+"=1") || strings.Contains(err.Error(), "[o]verride") {
			t.Errorf("error = %q tells a user who already set the switch to set it, or offers [o]verride", err)
		}
	})

	for _, env := range []string{"", "1"} {
		t.Run("a symlink loop is refused without [o]verride, env="+strconv.Quote(env), func(t *testing.T) {
			t.Setenv(iox.AllowSymlinkDestEnv, env)
			if env == "" {
				if err := os.Unsetenv(iox.AllowSymlinkDestEnv); err != nil {
					t.Fatal(err)
				}
			}
			tmp := t.TempDir()
			loop := filepath.Join(tmp, "loop.md")
			if err := os.Symlink("loop.md", loop); err != nil {
				t.Fatal(err)
			}
			err := writeBackFileItem(t.TempDir(), reconcileItem{op: adapter.FileOp{Path: loop, SourceID: "demo"}})
			if err == nil {
				t.Fatal("writeBackFileItem = nil for a symlink loop")
			}
			if strings.Contains(err.Error(), "[o]verride") || strings.Contains(err.Error(), "cannot stat") {
				t.Errorf("error = %q: a loop must take a symlink arm (no [o]verride), not the generic one", err)
			}
			if env == "1" && !strings.Contains(err.Error(), "cannot be resolved") {
				t.Errorf("error = %q, want the unresolvable-link refusal once opted in", err)
			}
			if env == "" && !strings.Contains(err.Error(), iox.AllowSymlinkDestEnv+"=1") {
				t.Errorf("error = %q, want the switch named while it is unset", err)
			}
		})
	}

	t.Run("env set: captures the linked file", func(t *testing.T) {
		t.Setenv(iox.AllowSymlinkDestEnv, "1")
		home := t.TempDir()
		link := mkLink(t)
		if err := writeBackFileItem(home, reconcileItem{op: adapter.FileOp{Path: link, SourceID: "demo"}}); err != nil {
			t.Fatalf("writeBackFileItem with the env set: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(home, "demo"))
		if err != nil || string(got) != "payload" {
			t.Fatalf("captured source = (%q, %v), want the linked file's content", got, err)
		}
	})
}

// TestReadDestBytesReportsAStatFailureAsItself pins that a stat failure which
// is not absence surfaces as the real error rather than as errDestNotRegular.
//
// It matters because the sentinel is not private: reconcile's write-back names
// it and tells the user to "remove or replace the non-regular file at that
// path". Answering it for a symlink loop or a permission problem states
// something false about the destination.
//
// ELOOP rather than EACCES because these tests run as root in the container,
// where permission bits are not enforced and an EACCES fixture would not fail.
func TestReadDestBytesReportsAStatFailureAsItself(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}

	_, err := readDestBytes(a)
	if err == nil {
		t.Fatal("readDestBytes on a symlink loop = nil, want ELOOP")
	}
	if errors.Is(err, errDestNotRegular) {
		t.Errorf("error = %v, want the real stat failure: a symlink loop is not a shape "+
			"problem, and reporting it as one makes reconcile tell the user to remove a "+
			"non-regular file that isn't there", err)
	}
	if !errors.Is(err, syscall.ELOOP) {
		t.Errorf("error = %v, want it to wrap ELOOP", err)
	}
	if !errors.Is(err, errDestUnstattable) {
		t.Errorf("error = %v, want it to wrap errDestUnstattable so hashFile can map it "+
			"to the shape sentinel and keep base parity", err)
	}
	// Pathless, like its sibling sentinel: the caller supplies the path, and a
	// *fs.PathError here makes reconcile print "read dest X: cannot stat
	// destination: stat X: ...". Counting occurrences rather than asserting a
	// literal keeps this from breaking on a reworded message.
	if n := strings.Count(err.Error(), a); n != 0 {
		t.Errorf("error = %q names the path %d time(s); it must carry none — the caller "+
			"wraps it with the path and a *fs.PathError would double it", err, n)
	}
}

// TestDiffPrintsAShapeHunkForANonRegularDestination pins that a FIFO at a
// whole-file destination — bare, or behind a symlink whatever the switch says —
// reaches diff as a "shape" hunk rather than as the whole source rendered
// against an "empty" destination.
func TestDiffPrintsAShapeHunkForANonRegularDestination(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     string
		link    bool
		content string
	}{
		{name: "bare FIFO", env: "", content: "SOURCE"},
		{name: "link to a FIFO, env unset", env: "", link: true, content: "SOURCE"},
		{name: "link to a FIFO, env set", env: "1", link: true, content: "SOURCE"},
		// An EMPTY rendered source equals the refused read's "" — the shape
		// check must run before the text compare or this prints "no diff".
		{name: "bare FIFO, empty source", env: "", content: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(iox.AllowSymlinkDestEnv, tc.env)
			if tc.env == "" {
				if err := os.Unsetenv(iox.AllowSymlinkDestEnv); err != nil {
					t.Fatal(err)
				}
			}
			tmp := t.TempDir()
			path := mkfifoDest(t, tmp)
			if tc.link {
				link := filepath.Join(tmp, "link.md")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			hunks, _ := collectDiffHunks(planFor(map[string][]adapter.FileOp{"claude": {fileOp(path, tc.content)}}), []string{"claude"}, "", nil)
			if len(hunks) != 1 || hunks[0].Pointer != "shape" {
				t.Fatalf("want one shape hunk, got %+v", hunks)
			}
			if h := hunks[0]; h.Source != "regular file" || !strings.Contains(h.Dest, "not a regular file") || strings.Contains(h.Dest, "/") || strings.Contains(h.Dest, "SOURCE") {
				t.Errorf("shape hunk = %+v: must name the shape, embed no path, and never render the source", h)
			}
		})
	}
}

// TestDiffPrintsAShapeHunkForAnUnstattableDestination pins the other fact the
// shape token carries: a path under a regular-file parent cannot be stat'd,
// reaches diff as the same hunk, and the hedged Dest says so.
func TestDiffPrintsAShapeHunkForAnUnstattableDestination(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "dest.md")
	hunks, _ := collectDiffHunks(planFor(map[string][]adapter.FileOp{"claude": {fileOp(path, "SOURCE")}}), []string{"claude"}, "", nil)
	if len(hunks) != 1 || hunks[0].Pointer != "shape" {
		t.Fatalf("want one shape hunk for an unstattable destination, got %+v", hunks)
	}
	if d := hunks[0].Dest; !strings.Contains(d, "stat") || strings.Contains(d, "/") {
		t.Errorf("Dest = %q: must say the path cannot be stat'd and embed no path", d)
	}
}

// TestReconcileUsesTheSHADisplayForAShapeRefusedItem pins reconcile's prompt
// half of the same rule diff's shape hunk enforces: a FIFO at a destination
// has no text to show, so the item falls back to the SHA display instead of
// rendering the whole source as an insertion against an "empty" destination.
func TestReconcileUsesTheSHADisplayForAShapeRefusedItem(t *testing.T) {
	tmp := t.TempDir()
	fifo := mkfifoDest(t, tmp)
	items, _ := collectReconcileItems(planFor(map[string][]adapter.FileOp{"claude": {fileOp(fifo, "SOURCE")}}),
		registryFactory(), state.New(), adapter.ScopeUser, "", tmp, nil)
	if len(items) != 1 || items[0].hdest != shapeSentinel {
		t.Fatalf("want one shape-refused item, got %+v", items)
	}
	if items[0].hasText {
		t.Error("hasText = true for a shape-refused item; the prompt would render the source against an empty destination")
	}
}
