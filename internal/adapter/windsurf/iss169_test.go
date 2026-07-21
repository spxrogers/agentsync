package windsurf_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/windsurf"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// TestApply_GlobalRules_WholeFileOverwrite_BacksUpForeign is the artifact-anchored
// P2 test. It writes SPEC-COMPLETE hand-authored files to disk BEFORE apply — a
// user-scope global_rules.md and a global workflow .md — then applies user-scope
// memory + command through the REAL render.Writer (not PassThroughWriter), so the
// foreign-collision backup path is exercised. Every assertion anchors to the
// on-disk file: (a) each destination now holds the agentsync-rendered body, and
// (b) the pre-existing foreign copy was backed up verbatim.
func TestApply_GlobalRules_WholeFileOverwrite_BacksUpForeign(t *testing.T) {
	targetRoot := t.TempDir()
	home := t.TempDir() // agentsync home = the backup-root anchor

	// Hand-authored global rules file, placed on disk before apply.
	globalRules := filepath.Join(targetRoot, ".codeium", "windsurf", "memories", "global_rules.md")
	if err := os.MkdirAll(filepath.Dir(globalRules), 0o755); err != nil {
		t.Fatal(err)
	}
	foreignRules := "# Hand-authored global rules\n\nDo not lose me.\n"
	if err := os.WriteFile(globalRules, []byte(foreignRules), 0o644); err != nil {
		t.Fatal(err)
	}
	// Hand-authored global workflow, placed on disk before apply.
	workflow := filepath.Join(targetRoot, ".codeium", "windsurf", "global_workflows", "release.md")
	if err := os.MkdirAll(filepath.Dir(workflow), 0o755); err != nil {
		t.Fatal(err)
	}
	foreignWorkflow := "1. hand-authored\n2. steps\n"
	if err := os.WriteFile(workflow, []byte(foreignWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}

	a := windsurf.New(windsurf.Options{TargetRoot: targetRoot})
	in := source.Canonical{
		Memory:   source.Memory{Body: "# agentsync memory\n\nManaged body.\n"},
		Commands: []source.Command{{Name: "release", Body: "1. tag\n2. push\n"}},
	}
	ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	// Empty state => the destinations are UNOWNED => the writer treats the
	// pre-existing files as foreign and backs them up before overwriting.
	st := state.New()
	w := render.NewWriter(st, home, targetRoot, adapter.ScopeUser, "", "windsurf")
	if err := a.Apply(ops, w); err != nil {
		t.Fatal(err)
	}

	// (a) On-disk destinations now hold the agentsync-rendered content.
	gotRules, err := os.ReadFile(globalRules)
	if err != nil {
		t.Fatal(err)
	}
	if source.StripManagedBanner(string(gotRules)) != in.Memory.Body {
		t.Fatalf("global_rules.md not overwritten with the agentsync body: %q", gotRules)
	}
	if strings.Contains(string(gotRules), "Do not lose me") {
		t.Fatalf("foreign global-rules content still on disk after overwrite: %q", gotRules)
	}
	gotWf, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotWf) != "1. tag\n2. push\n" {
		t.Fatalf("workflow not overwritten with the agentsync body: %q", gotWf)
	}

	// (b) Each pre-existing foreign copy was backed up VERBATIM before overwrite.
	assertBackedUp := func(destSuffix, wantContent string) {
		t.Helper()
		for _, r := range w.Reports() {
			if !strings.HasSuffix(filepath.ToSlash(r.Path), destSuffix) {
				continue
			}
			data, err := os.ReadFile(r.BackupTo)
			if err != nil {
				t.Fatalf("backup for %s unreadable at %s: %v", destSuffix, r.BackupTo, err)
			}
			if string(data) != wantContent {
				t.Fatalf("backup for %s = %q, want verbatim foreign %q", destSuffix, data, wantContent)
			}
			return
		}
		t.Fatalf("no foreign-collision backup reported for %s; reports=%+v", destSuffix, w.Reports())
	}
	assertBackedUp("memories/global_rules.md", foreignRules)
	assertBackedUp("global_workflows/release.md", foreignWorkflow)
}

// TestRoundTrip_Memory_FrontmatterCarryingBody is the P3 fidelity test: a canonical
// memory body that ITSELF begins with a user-authored `---`…`---` YAML block (not
// the agentsync activation frontmatter) must survive Render→Apply→Ingest
// byte-for-byte at both scopes. This proves the project-scope activation-frontmatter
// strip and the user-scope verbatim write never eat the user's own leading fence.
func TestRoundTrip_Memory_FrontmatterCarryingBody(t *testing.T) {
	body := "---\ntitle: My Rules\n---\n\n# Conventions\n\nUse tabs.\n"

	t.Run("project scope", func(t *testing.T) {
		proj := t.TempDir()
		a := windsurf.New(windsurf.Options{TargetRoot: t.TempDir()})
		projC := source.Canonical{Memory: source.Memory{Body: body}}
		c := projC
		c.Project = &projC
		ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, proj)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
			t.Fatal(err)
		}
		// Sanity: on disk the agentsync activation fence is at byte 0 and the user's
		// own frontmatter sits AFTER the managed banner, not at byte 0.
		onDisk, err := os.ReadFile(filepath.Join(proj, ".windsurf", "rules", "agentsync.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(onDisk), "---\ntrigger: always_on\n---\n") {
			t.Fatalf("activation fence must be at byte 0: %q", onDisk)
		}
		got, err := a.Ingest(adapter.ScopeProject, proj)
		if err != nil {
			t.Fatal(err)
		}
		if got.Memory.Body != body {
			t.Fatalf("user frontmatter not preserved byte-for-byte:\n got  %q\n want %q", got.Memory.Body, body)
		}
	})

	t.Run("user scope", func(t *testing.T) {
		tmp := t.TempDir()
		a := windsurf.New(windsurf.Options{TargetRoot: tmp})
		in := source.Canonical{Memory: source.Memory{Body: body}}
		ops, _, err := a.Render(secrets.ForRender(in), adapter.ScopeUser, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Apply(ops, adapter.PassThroughWriter{}); err != nil {
			t.Fatal(err)
		}
		got, err := a.Ingest(adapter.ScopeUser, "")
		if err != nil {
			t.Fatal(err)
		}
		if got.Memory.Body != body {
			t.Fatalf("user frontmatter not preserved byte-for-byte:\n got  %q\n want %q", got.Memory.Body, body)
		}
	})
}

// TestIngestMCPSpec_UrlCanonicalizedToServerUrl pins the P4 documented
// canonicalization: a hand-authored native server using the alternate `url` key
// ingests to the http transport with URL set, and re-renders as `serverUrl` (never
// `url`). This locks the benign dest→source→dest key rename so it can't drift.
func TestIngestMCPSpec_UrlCanonicalizedToServerUrl(t *testing.T) {
	spec := windsurf.IngestMCPSpec(map[string]any{
		"url":     "https://x/mcp",
		"headers": map[string]any{"K": "v"},
	})
	if spec.Type != "http" {
		t.Fatalf("native url key must canonicalize to the http transport, got %q", spec.Type)
	}
	if spec.URL != "https://x/mcp" {
		t.Fatalf("url not captured into canonical URL: %q", spec.URL)
	}

	c := source.Canonical{MCPServers: []source.MCPServer{{ID: "srv", Server: spec}}}
	ops, _, err := windsurf.New(windsurf.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	op := findOp(ops, "mcp_config.json")
	if op == nil {
		t.Fatal("mcp_config.json op missing")
	}
	var ours map[string]any
	if err := json.Unmarshal(op.Content, &ours); err != nil {
		t.Fatal(err)
	}
	srv := ours["mcpServers"].(map[string]any)["srv"].(map[string]any)
	if srv["serverUrl"] != "https://x/mcp" {
		t.Fatalf("re-render must emit serverUrl: %v", srv)
	}
	if _, hasURL := srv["url"]; hasURL {
		t.Fatalf("re-render must NOT emit the native url key (canonicalizes to serverUrl): %v", srv)
	}
}
