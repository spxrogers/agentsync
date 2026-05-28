package source_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestLoad_SkillBundledFiles proves a skill is loaded as a DIRECTORY: SKILL.md
// becomes frontmatter+body and every other file (scripts/, references/, nested,
// binary assets) is captured verbatim into Skill.Files with a slash-separated
// relative path, sorted deterministically. This is the guard against the
// "only SKILL.md survives" lossiness bug.
func TestLoad_SkillBundledFiles(t *testing.T) {
	fs := afero.NewMemMapFs()
	base := "/home/.agentsync/skills/deploy"
	binary := []byte{0x00, 0x01, 0x02, 0xff, 0xfe}
	_ = afero.WriteFile(fs, filepath.Join(base, "SKILL.md"), []byte("---\nname: deploy\ndescription: ship it\n---\nBody.\n"), 0o644)
	_ = afero.WriteFile(fs, filepath.Join(base, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755)
	_ = afero.WriteFile(fs, filepath.Join(base, "references", "REF.md"), []byte("# Reference\n"), 0o644)
	_ = afero.WriteFile(fs, filepath.Join(base, "assets", "logo.png"), binary, 0o644)

	c, err := source.Load(fs, "/home/.agentsync")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Skills) != 1 {
		t.Fatalf("skills = %d", len(c.Skills))
	}
	sk := c.Skills[0]
	if sk.Name != "deploy" || sk.Body != "Body.\n" {
		t.Fatalf("skill header wrong: %+v", sk)
	}
	want := map[string][]byte{
		"assets/logo.png":   binary,
		"references/REF.md": []byte("# Reference\n"),
		"scripts/run.sh":    []byte("#!/bin/sh\necho hi\n"),
	}
	if len(sk.Files) != len(want) {
		t.Fatalf("Files = %d, want %d (%+v)", len(sk.Files), len(want), sk.Files)
	}
	// Sorted by path.
	gotOrder := []string{sk.Files[0].Path, sk.Files[1].Path, sk.Files[2].Path}
	wantOrder := []string{"assets/logo.png", "references/REF.md", "scripts/run.sh"}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("Files not sorted: got %v want %v", gotOrder, wantOrder)
		}
	}
	for _, f := range sk.Files {
		if string(f.Content) != string(want[f.Path]) {
			t.Fatalf("content for %s = %q", f.Path, f.Content)
		}
	}
	// SKILL.md must never appear in Files.
	for _, f := range sk.Files {
		if f.Path == "SKILL.md" {
			t.Fatal("SKILL.md leaked into Files")
		}
	}
}

// TestWriteSkill_RoundTripsBundledFiles writes a skill (incl. an executable
// script and a binary asset) to a real home, reloads it, and asserts the bundled
// files survive byte-for-byte with their executable bit preserved.
func TestWriteSkill_RoundTripsBundledFiles(t *testing.T) {
	home := t.TempDir()
	binary := []byte{0x00, 0x10, 0x7f, 0x80, 0xff}
	in := source.Skill{
		Name:        "deploy",
		Frontmatter: map[string]any{"name": "deploy", "description": "ship it"},
		Body:        "Body.\n",
		Files: []source.SkillFile{
			{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\necho hi\n"), Mode: 0o755},
			{Path: "assets/logo.png", Content: binary, Mode: 0o644},
		},
	}
	if err := source.WriteSkill(home, in); err != nil {
		t.Fatalf("WriteSkill: %v", err)
	}

	// Executable bit preserved on disk.
	st, err := os.Stat(filepath.Join(home, "skills", "deploy", "scripts", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("scripts/run.sh lost its executable bit: %v", st.Mode())
	}

	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Skills) != 1 || len(c.Skills[0].Files) != 2 {
		t.Fatalf("round-trip lost bundled files: %+v", c.Skills)
	}
	for _, f := range c.Skills[0].Files {
		switch f.Path {
		case "scripts/run.sh":
			if f.Mode&0o100 == 0 {
				t.Fatalf("run.sh mode not preserved on reload: %o", f.Mode)
			}
		case "assets/logo.png":
			if string(f.Content) != string(binary) {
				t.Fatalf("binary asset corrupted: %v", f.Content)
			}
		default:
			t.Fatalf("unexpected bundled file %s", f.Path)
		}
	}
}

// TestWriteSkill_RejectsTraversalBundledPath ensures a malicious bundled path
// (e.g. captured from a foreign native config) cannot escape the skill dir and
// clobber an arbitrary file.
func TestWriteSkill_RejectsTraversalBundledPath(t *testing.T) {
	home := t.TempDir()
	for _, bad := range []string{"../escape.txt", "/etc/passwd", "../../x"} {
		in := source.Skill{
			Name:        "deploy",
			Frontmatter: map[string]any{"name": "deploy", "description": "x"},
			Body:        "b\n",
			Files:       []source.SkillFile{{Path: bad, Content: []byte("x"), Mode: 0o644}},
		}
		if err := source.WriteSkill(home, in); err == nil {
			t.Fatalf("WriteSkill accepted traversal path %q", bad)
		}
	}
}
