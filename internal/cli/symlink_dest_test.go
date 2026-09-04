package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/iox"
)

// #229 axis 9: the read side of the symlink policy mirrors the write side.
// AGENTSYNC_ALLOW_SYMLINK_DEST=1 is the one switch under which `apply` writes
// THROUGH a symlinked destination; these two tests pin that the SAME switch
// decides whether `status` and `diff` read through it.
//
// Both run the CLI in-process, so t.Setenv reaches iox.AtomicWrite and the
// walk's destReadPath alike.

// symlinkedMemoryFixture stands up a user-scope home with claude enabled and a
// canonical memory, then pre-creates the chezmoi-style link: the rendered
// CLAUDE.md destination is a symlink into a "dotfiles" directory BEFORE the
// first apply. It returns the link path and its target.
func symlinkedMemoryFixture(t *testing.T, env map[string]string, tmp string) (link, target string) {
	t.Helper()
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	if err := os.WriteFile(filepath.Join(tmp, ".agentsync", "memory", "AGENTS.md"),
		[]byte("# Memory\n\nRendered through a link.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(tmp, "dotfiles", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("stale dotfiles copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(tmp, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return link, target
}

// applyThroughLink applies with the switch set and asserts the chezmoi
// contract held: the link is still a link and the TARGET holds the rendered
// memory. That is what makes the drift assertions below non-vacuous — they
// cannot pass by the link having been replaced with a regular file.
func applyThroughLink(t *testing.T, env map[string]string, link, target string) {
	t.Helper()
	t.Setenv(iox.AllowSymlinkDestEnv, "1")
	mustRun(t, env, "apply")
	lst, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if lst.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("apply replaced the symlink at %s with a regular file (mode=%v)", link, lst.Mode())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Rendered through a link.") {
		t.Fatalf("apply did not write the rendered memory through the link; target holds:\n%s", got)
	}
}

// statusClassOf runs `status --json` and returns the class of the item at path
// (fatal if absent) plus the whole summary tally.
func statusClassOf(t *testing.T, env map[string]string, path string) (class string, summary map[string]int) {
	t.Helper()
	out, err := runCLI(t, env, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	var got struct {
		Agents []struct {
			Agent string `json:"agent"`
			Items []struct {
				Path  string `json:"path"`
				Class string `json:"class"`
			} `json:"items"`
		} `json:"agents"`
		Summary map[string]int `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("status --json: invalid JSON: %v\n%s", err, out)
	}
	for _, ag := range got.Agents {
		for _, it := range ag.Items {
			if it.Path == path {
				return it.Class, got.Summary
			}
		}
	}
	t.Fatalf("status --json lists no item for %s:\n%s", path, out)
	return "", nil
}

// TestSymlinkedDestConvergesWhenAllowed (N-4) is the chezmoi contract end to
// end: with the switch set, `apply` writes through a pre-created link, and then
// `status` says clean and `diff` says no diff — the permanent phantom drift the
// old link-refusing hash produced under this exact, documented configuration is
// gone.
func TestSymlinkedDestConvergesWhenAllowed(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	link, target := symlinkedMemoryFixture(t, env, tmp)
	applyThroughLink(t, env, link, target)

	class, summary := statusClassOf(t, env, link)
	if class != "clean" || summary["drift"] != 0 {
		t.Errorf("status must be clean through an allowed symlink: class=%q summary=%v", class, summary)
	}
	out, err := runCLI(t, env, "diff")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	if out != "no diff" {
		t.Errorf("diff must compare through an allowed symlink and find nothing; got:\n%s", out)
	}
}

// TestSymlinkedDestIsDriftWhenRefused (N-5) is the other half: the same
// converged link, with the switch UNSET, is drift on every surface — and `diff`
// says so with a "symlink" hunk naming the switch, instead of reading through
// the link and printing "no diff" beside a `status --exit-code` that fails.
// The content is genuinely converged (applied through the link first), so the
// assertions cannot be satisfied by ordinary content drift.
func TestSymlinkedDestIsDriftWhenRefused(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	link, target := symlinkedMemoryFixture(t, env, tmp)
	applyThroughLink(t, env, link, target)
	// applyThroughLink's t.Setenv registered the restore; unset for the reads.
	if err := os.Unsetenv(iox.AllowSymlinkDestEnv); err != nil {
		t.Fatal(err)
	}

	if class, summary := statusClassOf(t, env, link); class != "drift" || summary["drift"] != 1 {
		t.Errorf("status must call a refused symlink drift: class=%q summary=%v", class, summary)
	}

	out, err := runCLI(t, env, "diff", "--json")
	if err != nil {
		t.Fatalf("diff --json: %v\n%s", err, out)
	}
	var got struct {
		Hunks []struct {
			Path    string `json:"path"`
			Pointer string `json:"pointer"`
			Source  string `json:"source"`
			Dest    string `json:"dest"`
		} `json:"hunks"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("diff --json: invalid JSON: %v\n%s", err, out)
	}
	if len(got.Hunks) != 1 || got.Hunks[0].Path != link || got.Hunks[0].Pointer != "symlink" {
		t.Fatalf("diff must say the destination is a symlink it is not comparing through — one "+
			"symlink hunk for %s; got %+v", link, got.Hunks)
	}
	h := got.Hunks[0]
	if h.Source != "regular file" || !strings.Contains(h.Dest, iox.AllowSymlinkDestEnv+"=1") {
		t.Errorf("symlink hunk must name the switch that reads through the link: %+v", h)
	}
	// Terminal safety: the link TARGET is attacker-choosable and the hunk's
	// Dest reaches the terminal unsanitized, so it must never embed it.
	if strings.Contains(h.Dest, target) || strings.Contains(h.Dest, "dotfiles") {
		t.Errorf("symlink hunk embeds the link target; it must be a constant: %+v", h)
	}
	if strings.Contains(h.Dest, "/") {
		t.Errorf("symlink hunk Dest contains a path separator; it must embed no path: %q", h.Dest)
	}
}
