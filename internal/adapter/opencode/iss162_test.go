package opencode_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/opencode"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestIngest_ReadsAgentArtifact is artifact-anchored (issue #162 item K): it
// hand-writes a real on-disk OpenCode agent .md (frontmatter + body) into the
// native agents dir, marks it agentsync-owned, and asserts Ingest reads the
// on-disk artifact — the frontmatter keys and body — NOT a model that was just
// rendered.
func TestIngest_ReadsAgentArtifact(t *testing.T) {
	targetRoot := t.TempDir()
	agentsDir := filepath.Join(targetRoot, ".config", "opencode", "agents")
	p := filepath.Join(agentsDir, "reviewer.md")
	writeFixtureFile(t, p,
		"---\ndescription: reviews code\nmode: subagent\nmodel: anthropic/claude-sonnet-4\n---\nReviewer body here.\n")
	ownFiles(t, targetRoot, adapter.ScopeUser, "", p)

	a := opencode.New(opencode.Options{TargetRoot: targetRoot})
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.Subagents) != 1 || out.Subagents[0].Name != "reviewer" {
		t.Fatalf("want 1 subagent 'reviewer', got %v", subagentNames(out.Subagents))
	}
	sa := out.Subagents[0]
	if sa.Frontmatter["description"] != "reviews code" {
		t.Errorf("description = %v, want %q", sa.Frontmatter["description"], "reviews code")
	}
	if sa.Frontmatter["mode"] != "subagent" {
		t.Errorf("mode = %v, want %q", sa.Frontmatter["mode"], "subagent")
	}
	if sa.Frontmatter["model"] != "anthropic/claude-sonnet-4" {
		t.Errorf("model = %v, want %q", sa.Frontmatter["model"], "anthropic/claude-sonnet-4")
	}
	if !strings.Contains(sa.Body, "Reviewer body here.") {
		t.Errorf("body = %q, want it to contain the on-disk body", sa.Body)
	}
}

// TestIngest_ReadsCommandArtifact is the command-side twin of the above (item K):
// a hand-written on-disk command .md is read back from the artifact.
func TestIngest_ReadsCommandArtifact(t *testing.T) {
	targetRoot := t.TempDir()
	commandsDir := filepath.Join(targetRoot, ".config", "opencode", "commands")
	p := filepath.Join(commandsDir, "deploy.md")
	writeFixtureFile(t, p,
		"---\ndescription: deploy the app\n---\nRun the deploy script.\n")
	ownFiles(t, targetRoot, adapter.ScopeUser, "", p)

	a := opencode.New(opencode.Options{TargetRoot: targetRoot})
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(out.Commands) != 1 || out.Commands[0].Name != "deploy" {
		t.Fatalf("want 1 command 'deploy', got %v", commandNames(out.Commands))
	}
	cm := out.Commands[0]
	if cm.Frontmatter["description"] != "deploy the app" {
		t.Errorf("description = %v, want %q", cm.Frontmatter["description"], "deploy the app")
	}
	if !strings.Contains(cm.Body, "Run the deploy script.") {
		t.Errorf("body = %q, want it to contain the on-disk body", cm.Body)
	}
}

// TestIngest_ReadsSkillArtifact is artifact-anchored (item K): a hand-written
// skill DIRECTORY (SKILL.md + a bundled executable script) in the shared Claude
// skills dir is read back with its frontmatter, body, and bundled file content +
// executable mode intact — proving Ingest captures the whole skill directory
// (source.Skill), not just SKILL.md.
func TestIngest_ReadsSkillArtifact(t *testing.T) {
	targetRoot := t.TempDir()
	skillDir := filepath.Join(targetRoot, ".claude", "skills", "greet")
	writeFixtureFile(t, filepath.Join(skillDir, "SKILL.md"),
		"---\nname: greet\ndescription: greet the user\n---\nGreet skill body.\n")
	script := filepath.Join(skillDir, "scripts", "run.sh")
	writeFixtureFile(t, script, "#!/bin/sh\necho hi\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatalf("chmod script: %v", err)
	}

	a := opencode.New(opencode.Options{TargetRoot: targetRoot})
	out, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	var sk *source.Skill
	for i := range out.Skills {
		if out.Skills[i].Name == "greet" {
			sk = &out.Skills[i]
		}
	}
	if sk == nil {
		t.Fatalf("skill 'greet' not ingested; got %d skills", len(out.Skills))
	}
	if sk.Frontmatter["description"] != "greet the user" {
		t.Errorf("description = %v, want %q", sk.Frontmatter["description"], "greet the user")
	}
	if !strings.Contains(sk.Body, "Greet skill body.") {
		t.Errorf("body = %q, want the on-disk body", sk.Body)
	}
	var bundled *source.SkillFile
	for i := range sk.Files {
		if sk.Files[i].Path == "scripts/run.sh" {
			bundled = &sk.Files[i]
		}
	}
	if bundled == nil {
		t.Fatalf("bundled scripts/run.sh not captured; files=%+v", sk.Files)
	}
	if string(bundled.Content) != "#!/bin/sh\necho hi\n" {
		t.Errorf("bundled content = %q, want the on-disk bytes", bundled.Content)
	}
	if os.FileMode(bundled.Mode).Perm() != 0o755 {
		t.Errorf("bundled mode = %o, want 0755 (+x preserved)", bundled.Mode)
	}
}

// TestIngest_RoundTripsMemoryFragments proves OpenCode memory FRAGMENTS survive
// the round-trip (issue #162 item I). OpenCode has no native @import concept, so
// agentsync expands fragments inline into the rendered AGENTS.md wrapped in
// reversible boundary markers. This renders a fragment-composed memory to disk,
// reads the ON-DISK AGENTS.md via Ingest, and confirms (a) the markers survive
// StripManagedBanner into Memory.Body, and (b) the write-back layer's
// source.CollapseMemoryMarkers reconstructs the @import directive + the fragment
// file from them — i.e. fragments are round-tripped, not silently flattened.
func TestIngest_RoundTripsMemoryFragments(t *testing.T) {
	targetRoot := t.TempDir()
	in := source.Canonical{
		Memory: source.Memory{
			Body:      "# Root memory\n\n@import ./fragments/team.md\n\nTail line.\n",
			Fragments: map[string]string{"team.md": "Team-specific rules.\n"},
		},
	}
	a := opencode.New(opencode.Options{TargetRoot: targetRoot})
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Artifact anchor: the on-disk AGENTS.md must carry the fragment markers.
	memPath := opencode.ResolvePaths(targetRoot, "", false).Memory
	onDisk, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatalf("read rendered AGENTS.md: %v", err)
	}
	if !strings.Contains(string(onDisk), "<!-- agentsync:fragment team.md -->") {
		t.Fatalf("rendered AGENTS.md lost the fragment boundary marker:\n%s", onDisk)
	}

	ingested, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	// The banner is stripped, but the fragment markers must remain in Body so the
	// capture layer can reverse them.
	if !strings.Contains(ingested.Memory.Body, "<!-- agentsync:fragment team.md -->") {
		t.Fatalf("Ingest dropped the fragment markers from Memory.Body:\n%s", ingested.Memory.Body)
	}
	mem, hadMarkers, err := source.CollapseMemoryMarkers(ingested.Memory.Body)
	if err != nil {
		t.Fatalf("CollapseMemoryMarkers: %v", err)
	}
	if !hadMarkers {
		t.Fatal("no markers found — fragments would collapse into Body (silent flatten)")
	}
	if got := mem.Fragments["team.md"]; got != "Team-specific rules.\n" {
		t.Fatalf("fragment not recovered: got %q, want %q", got, "Team-specific rules.\n")
	}
	if !strings.Contains(mem.Body, "@import ./fragments/team.md") {
		t.Fatalf("@import directive not restored in reconstructed AGENTS.md:\n%s", mem.Body)
	}
}
