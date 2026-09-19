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
	if err := os.Symlink(home, homeLink); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
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
