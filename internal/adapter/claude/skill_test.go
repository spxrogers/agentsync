package claude_test

import (
	"path/filepath"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestSkillFileOps_EmitsBundledOps proves the renderer projects a skill as a
// whole directory: one op for SKILL.md plus one verbatim op per bundled file,
// each with the matching dest Path, canonical SourceID, and preserved mode.
func TestSkillFileOps_EmitsBundledOps(t *testing.T) {
	skills := []source.Skill{{
		Name:        "deploy",
		Frontmatter: map[string]any{"name": "deploy", "description": "ship it"},
		Body:        "Body.\n",
		Files: []source.SkillFile{
			{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0o755},
			{Path: "references/REF.md", Content: []byte("# ref\n"), Mode: 0o644},
		},
	}}
	ops, err := claude.SkillFileOps(skills, "/root/skills")
	if err != nil {
		t.Fatalf("SkillFileOps: %v", err)
	}
	byPath := map[string]adapter.FileOp{}
	for _, op := range ops {
		byPath[op.Path] = op
	}
	if len(ops) != 3 {
		t.Fatalf("ops = %d, want 3 (%+v)", len(ops), ops)
	}

	script := byPath[filepath.Join("/root/skills", "deploy", "scripts", "run.sh")]
	if script.SourceID != filepath.Join("skills", "deploy", "scripts", "run.sh") {
		t.Fatalf("script SourceID = %q", script.SourceID)
	}
	if script.Mode != 0o755 {
		t.Fatalf("script mode = %o, want 0755", script.Mode)
	}
	if string(script.Content) != "#!/bin/sh\n" {
		t.Fatalf("script content = %q", script.Content)
	}
	if _, ok := byPath[filepath.Join("/root/skills", "deploy", "SKILL.md")]; !ok {
		t.Fatal("SKILL.md op missing")
	}
}

// TestIngest_RoundTripsSkillBundledFiles renders a skill with bundled files,
// applies it, ingests it back, and asserts the directory survives intact —
// content byte-for-byte and the script's executable bit preserved.
func TestIngest_RoundTripsSkillBundledFiles(t *testing.T) {
	tmp := t.TempDir()
	binary := []byte{0x00, 0x01, 0xff, 0x7f}
	in := source.Canonical{
		Skills: []source.Skill{{
			Name:        "deploy",
			Frontmatter: map[string]any{"name": "deploy", "description": "ship it"},
			Body:        "Body.\n",
			Files: []source.SkillFile{
				{Path: "assets/logo.png", Content: binary, Mode: 0o644},
				{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\necho hi\n"), Mode: 0o755},
			},
		}},
	}
	a := claude.New(claude.Options{TargetRoot: tmp})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.Skills) != 1 {
		t.Fatalf("skills = %d", len(out.Skills))
	}
	files := out.Skills[0].Files
	if len(files) != 2 {
		t.Fatalf("bundled files lost on round-trip: %+v", files)
	}
	for _, f := range files {
		switch f.Path {
		case "scripts/run.sh":
			if f.Mode&0o100 == 0 {
				t.Fatalf("run.sh lost +x: %o", f.Mode)
			}
		case "assets/logo.png":
			if string(f.Content) != string(binary) {
				t.Fatalf("binary asset corrupted")
			}
		default:
			t.Fatalf("unexpected bundled file %s", f.Path)
		}
	}
}
