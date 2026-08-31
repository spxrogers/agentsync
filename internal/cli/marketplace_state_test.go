package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/state"
)

// writeMarketplaceFixture lays down a minimal local marketplace directory that
// `marketplace add <path>` can fetch without touching the network.
func writeMarketplaceFixture(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name": "` + name + `", "owner": {"name": "x"}, "plugins": []}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestMarketplaceAdd_DoesNotClobberUnreadableState pins that a targets.json
// agentsync CANNOT read is left alone rather than replaced with an empty state.
//
// state.Load returns a fresh empty state with NO error for an ABSENT file, so a
// non-nil error means the file exists and is unreadable — which the typed-key
// migration made materially more reachable (an ambiguous v1 key, a v2 role
// violation, a non-UTF-8 key), and whose remedy text invites the user to keep
// running commands meanwhile. Falling back to state.New() and saving would
// discard every Files/Keys/Plugins entry and every other marketplace; the next
// apply would then own nothing and back up every managed destination as a
// foreign collision — the exact mass-backup failure the typed key exists to
// prevent. The record must be skipped instead.
//
// Skipping SILENTLY would be its own defect, so the test also pins the warning:
// without it the user reads a success line while status/apply/diff/doctor are
// all already failing on the same file, with nothing connecting the two.
func TestMarketplaceAdd_DoesNotClobberUnreadableState(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	fixture := writeMarketplaceFixture(t, filepath.Join(tmp, "fixture-mp"), "test-mp")
	mustRun(t, env, "init")

	// A v1 key with more than one possible reading — issue #227's colon-bearing
	// project root is exactly the user who hits this — alongside REAL ownership
	// and marketplace records that must survive.
	const refused = `{"schema_version":1,` +
		`"files":{"claude:project:${HOME}/a:${HOME}/b:${HOME}/c":{"sha256":"deadbeef"}},` +
		`"marketplaces":{"other-mp":{"url":"https://example.invalid/mp","head_sha":"cafe"}}}`
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(refused), 0o644); err != nil {
		t.Fatal(err)
	}
	// Guard the premise: if Load ever stops refusing this document the test is
	// no longer exercising the unreadable-state branch at all.
	if _, err := state.Load(statePath); err == nil {
		t.Fatal("premise broken: state.Load must refuse this targets.json")
	}

	out, err := runCLI(t, env, "marketplace", "add", fixture)
	if err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}
	// The add itself succeeded, so this is a warning beside the success line —
	// not a failure. It must name the file, or the user has nothing to go on.
	for _, want := range []string{"WARN", "not recorded", "targets.json", "agentsync doctor"} {
		if !strings.Contains(out, want) {
			t.Errorf("skipping the state record must warn and mention %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "added marketplace") {
		t.Errorf("the add still succeeded and must still say so; got:\n%s", out)
	}

	got, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != refused {
		t.Fatalf("marketplace add rewrote an unreadable targets.json, destroying its records:\n got: %s\nwant: %s", got, refused)
	}
}
