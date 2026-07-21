package release

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"sigs.k8s.io/yaml"
)

// wantArchiveMtimePin is the template every in-archive entry's mtime must be
// pinned to for the archives to be byte-reproducible run-to-run. Pinning to the
// commit date (rather than wall-clock build time) is exactly what PR #115 removed
// and issue #138 restored; issue #173 then empirically confirmed the restored pin
// makes all six tar.gz/zip archives byte-identical across two snapshot releases.
const wantArchiveMtimePin = "{{ .CommitDate }}"

// goreleaserConfig models only the archive fields this guard inspects.
// sigs.k8s.io/yaml round-trips YAML through JSON, so the struct tags are `json`.
// Unmodeled blocks (builds, nfpms, publishers, …) are ignored on unmarshal.
type goreleaserConfig struct {
	Archives []struct {
		ID         string `json:"id"`
		BuildsInfo struct {
			Mtime string `json:"mtime"`
		} `json:"builds_info"`
		Files []struct {
			Src  string `json:"src"`
			Info struct {
				Mtime string `json:"mtime"`
			} `json:"info"`
		} `json:"files"`
	} `json:"archives"`
}

// TestGoreleaserReproducibilityClaimMatchesConfig anchors the archive-
// reproducibility claim in .goreleaser.yaml to the config that actually backs it.
// The `archives` block comment claims re-cutting a release produces byte-identical
// archives; that holds only because every in-archive entry's mtime is pinned to
// the commit date — builds_info.mtime for the binary, files[].info.mtime for the
// bundled LICENSE/README/CHANGELOG. This reads the real on-disk .goreleaser.yaml
// and fails if any of those pins is missing or points somewhere other than the
// commit date, catching the PR #115 regression class (silently stripping the pin)
// before it ships a non-reproducible archive.
//
// Scope: archives only. checksums.txt and the nfpms deb/rpm are NOT reproducible —
// they embed the wall-clock build time (see issue #173 and the `checksum` block in
// .goreleaser.yaml) — so this guard deliberately asserts nothing about them; a
// check over checksums.txt would be permanently red.
func TestGoreleaserReproducibilityClaimMatchesConfig(t *testing.T) {
	root := findRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatalf("reading .goreleaser.yaml: %v", err)
	}

	var cfg goreleaserConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parsing .goreleaser.yaml: %v", err)
	}
	if len(cfg.Archives) == 0 {
		t.Fatal("no `archives` block found in .goreleaser.yaml")
	}

	// Collect every mtime pin the config must carry: one builds_info pin per
	// archive (the binary), plus one info pin per bundled file.
	type pin struct {
		name  string
		mtime string
	}
	var pins []pin
	for i, a := range cfg.Archives {
		id := a.ID
		if id == "" {
			id = "archive[" + strconv.Itoa(i) + "]"
		}
		pins = append(pins, pin{name: id + ".builds_info.mtime", mtime: a.BuildsInfo.Mtime})
		if len(a.Files) == 0 {
			t.Errorf("archive %q bundles no `files:` entries with pinned mtimes; "+
				"the default LICENSE/README/CHANGELOG set must be pinned for reproducibility", id)
		}
		for _, f := range a.Files {
			pins = append(pins, pin{name: id + ".files[" + f.Src + "].info.mtime", mtime: f.Info.Mtime})
		}
	}

	for _, p := range pins {
		t.Run(p.name, func(t *testing.T) {
			if p.mtime != wantArchiveMtimePin {
				t.Errorf("mtime pin = %q, want %q — archive reproducibility (and the "+
					".goreleaser.yaml comment claiming byte-identical archives) is broken; "+
					"see issues #173/#138", p.mtime, wantArchiveMtimePin)
			}
		})
	}
}

// findRepoRoot walks up from the test's working directory until it finds go.mod.
// The test only reads the committed .goreleaser.yaml (no home dir, no writes), so
// it is host-safe and needs no container guard.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod walking up from test cwd")
		}
		dir = parent
	}
}
