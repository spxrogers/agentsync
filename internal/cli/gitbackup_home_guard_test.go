package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/spxrogers/agentsync/internal/adapter"
	agit "github.com/spxrogers/agentsync/internal/git"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
	"github.com/spxrogers/agentsync/internal/ui"
)

// homeSwallowingAdapter is a stand-in registered adapter whose declared version
// roots are caller-supplied, so the central never-at-or-above-$HOME guard can be
// driven with roots no real adapter would declare ($HOME itself, an ancestor).
type homeSwallowingAdapter struct {
	adapter.Adapter
	name  string
	roots []string
}

func (a homeSwallowingAdapter) Name() string                                { return a.name }
func (a homeSwallowingAdapter) VersionRoots(adapter.Scope, string) []string { return a.roots }

// TestEnabledVersionRoots_NeverAtOrAboveHome pins the central guard (issue
// #270): a declared root that IS the user's home, or an ancestor of it, is
// dropped from the version-root union before de-nesting — otherwise de-nesting
// would fold every other agent's root into it and the apply tail would `git
// init` $HOME. Ordinary roots under $HOME and outside it survive. The guard
// follows the DIRECTORY: a symlinked spelling of $HOME is dropped too.
func TestEnabledVersionRoots_NeverAtOrAboveHome(t *testing.T) {
	testenv.RequireContainer(t)
	base := t.TempDir()
	home := filepath.Join(base, "home", "alice")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	homeLink := filepath.Join(base, "home-link")
	mustSymlink(t, home, homeLink)
	outside := filepath.Join(base, "opt", "grok")
	under := filepath.Join(home, ".grok")

	reg := adapter.NewRegistry()
	if err := reg.Register(homeSwallowingAdapter{name: "swallower", roots: []string{
		home, filepath.Dir(home), string(filepath.Separator), homeLink, outside, under,
	}}); err != nil {
		t.Fatal(err)
	}

	kept, swallowing := partitionVersionRoots(reg, []string{"swallower"}, adapter.ScopeUser, "", home)
	wantKept := []string{outside, under}
	sort.Strings(wantKept) // denestRoots sorts
	if !reflect.DeepEqual(kept, wantKept) {
		t.Fatalf("kept = %v, want %v", kept, wantKept)
	}
	wantDropped := []string{string(filepath.Separator), filepath.Dir(home), homeLink, home}
	if !reflect.DeepEqual(swallowing, wantDropped) {
		t.Fatalf("swallowing = %v, want %v (sorted)", swallowing, wantDropped)
	}
	if got := enabledVersionRoots(reg, []string{"swallower"}, adapter.ScopeUser, "", home); !reflect.DeepEqual(got, kept) {
		t.Fatalf("enabledVersionRoots = %v, want the kept set %v", got, kept)
	}
	if got := dropHomeSwallowing([]string{home, outside}, home); !reflect.DeepEqual(got, []string{outside}) {
		t.Fatalf("dropHomeSwallowing = %v, want [%s]", got, outside)
	}
	// An empty userHome (HOME unset, no redirect) disables the guard deliberately:
	// there is no home to protect. Pinned so the documented behaviour cannot drift.
	if kept, swallowing := partitionVersionRoots(reg, []string{"swallower"}, adapter.ScopeUser, "", ""); len(swallowing) != 0 || len(kept) == 0 {
		t.Fatalf("with userHome=\"\": kept=%v swallowing=%v; want nothing dropped", kept, swallowing)
	}

	// The drop is never silent: the apply-tail session warns once per dropped root.
	var errBuf bytes.Buffer
	p := ui.New(&bytes.Buffer{}, &errBuf, ui.ColorNever)
	t.Setenv("AGENTSYNC_TARGET_ROOT", home) // the session resolves $HOME through paths.HomeDir
	s := newGitBackupSession(&cobra.Command{}, p, reg, []string{"swallower"}, adapter.ScopeUser, "", t.TempDir(),
		source.DestinationGitBackupConfig{Mode: source.GitBackupModeOn}, false)
	if s == nil {
		t.Fatal("session must be created for user scope with mode on")
	}
	if !reflect.DeepEqual(s.roots, kept) {
		t.Fatalf("session roots = %v, want %v", s.roots, kept)
	}
	out := errBuf.String()
	for _, dropped := range wantDropped {
		if !strings.Contains(out, dropped) {
			t.Errorf("warning does not name dropped root %q:\n%s", dropped, out)
		}
	}
	if n := strings.Count(out, "never inits a repo at or above $HOME"); n != len(wantDropped) {
		t.Errorf("want %d warnings, got %d:\n%s", len(wantDropped), n, out)
	}
}

// TestDoctorReportsHomeSwallowingRoot backs the docs/grok.md claim that `doctor`
// reports a version root the central guard refuses: with GROK_HOME pointing at
// an ANCESTOR of $HOME (accepted by the adapter, dropped by the guard), the
// destination-git-backup section names the root and the reason instead of
// silently omitting grok from its table. Runs against the real registry with a
// tmp HOME and no redirect, since the redirect would blank GROK_HOME.
func TestDoctorReportsHomeSwallowingRoot(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Dir(home)) // ancestor of $HOME: not refused, but never versioned
	var out bytes.Buffer
	p := ui.New(&out, &out, ui.ColorNever)
	checkDestinationGitBackup(p, source.DestinationGitBackupConfig{Mode: source.GitBackupModeOn})
	// The root and the reason must be on the SAME line: the root string alone is
	// a prefix of $HOME and would match any line naming a path under it.
	found := false
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "never inits a repo at or above $HOME") && strings.Contains(line, filepath.Dir(home)+" — ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor output has no line reporting %s as a home-swallowing root:\n%s", filepath.Dir(home), out.String())
	}
}

// TestRevertAgent_SkipsHomeSwallowingRoot pins dropHomeSwallowing on the path
// that reaches it: `revert grok` with a GROK_HOME above $HOME has no revertable
// root, and says so, rather than operating on the ancestor directory.
func TestRevertAgent_SkipsHomeSwallowingRoot(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Dir(home))
	reg := registryFactory()
	// Precondition: grok really DECLARED the ancestor root (validateHome accepts
	// it), so the error below proves the drop, not an upstream nil.
	if _, swallowing := partitionVersionRoots(reg, []string{"grok"}, adapter.ScopeUser, "", home); !reflect.DeepEqual(swallowing, []string{filepath.Dir(home)}) {
		t.Fatalf("precondition: grok must declare %s and the guard must classify it as swallowing; got %v", filepath.Dir(home), swallowing)
	}
	var out bytes.Buffer
	p := ui.New(&out, &out, ui.ColorNever)
	err := revertAgent(p, reg, "grok", "", true, agit.Identity{}, true)
	if err == nil || !strings.Contains(err.Error(), "no user-scope destination dir") {
		t.Fatalf("revertAgent(grok) = %v; want the no-revertable-root error once the home-swallowing root is dropped", err)
	}
}

// TestPartitionVersionRoots_LexicalAncestorOfSymlinkedHome pins the guard's second
// leg (#271 review round 4): when the home directory is ITSELF a symlink
// elsewhere (`$HOME=/home/alice → /data/alice`), a declared root that is an
// ancestor of the home's spelling (`/home`) does not contain the resolved
// directory — identity alone would keep it and agentsync would offer to
// `git init /home`. The lexical leg drops it. A root that is an ancestor of
// neither spelling stays.
func TestPartitionVersionRoots_LexicalAncestorOfSymlinkedHome(t *testing.T) {
	testenv.RequireContainer(t)
	base := t.TempDir()
	real := filepath.Join(base, "data", "alice")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(base, "home")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userHome := filepath.Join(linkDir, "alice") // the $HOME spelling
	mustSymlink(t, real, userHome)

	unrelated := filepath.Join(base, "opt", "grok")
	reg := adapter.NewRegistry()
	if err := reg.Register(homeSwallowingAdapter{name: "swallower", roots: []string{linkDir, filepath.Join(base, "data"), unrelated}}); err != nil {
		t.Fatal(err)
	}
	kept, swallowing := partitionVersionRoots(reg, []string{"swallower"}, adapter.ScopeUser, "", userHome)
	if !reflect.DeepEqual(kept, []string{unrelated}) {
		t.Fatalf("kept = %v, want only %s", kept, unrelated)
	}
	// Both ancestors are dropped: /home by spelling, /data by identity.
	if want := []string{filepath.Join(base, "data"), linkDir}; !reflect.DeepEqual(swallowing, want) {
		t.Fatalf("swallowing = %v, want %v", swallowing, want)
	}
	if !swallowsHome(linkDir, userHome) || !swallowsHome(filepath.Join(base, "data"), userHome) || swallowsHome(unrelated, userHome) {
		t.Fatal("swallowsHome must be true for the spelling ancestor and the identity ancestor, false for an unrelated dir")
	}
}

// mustSymlink creates link → target, skipping the test where symlinks are
// unavailable (never the case in the Linux container, but the skip keeps the
// intent honest on any other host).
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
}
