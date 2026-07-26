package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The acceptance suite for `agentsync explain <path>` (#201) — provenance for a
// destination file or one merged key.

// explainFixture stands up a user-scope home with claude+opencode enabled, one
// MCP server carrying an ${env:…} reference, and one subagent, applied once.
func explainFixture(t *testing.T) (tmp string, env map[string]string) {
	t.Helper()
	tmp = t.TempDir()
	env = map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "EXPLAIN_TEST_TOKEN": "s3cr3t-value-xyz"}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "opencode")
	mustRun(t, env, "mcp", "add", "github",
		"--command", "npx", "--args", "-y,@modelcontextprotocol/server-github",
		"--env", "TOKEN=${env:EXPLAIN_TEST_TOKEN}")

	subDir := filepath.Join(tmp, ".agentsync", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "reviewer.md"),
		[]byte("---\ndescription: reviewer\ntools: [Read]\n---\nReview carefully.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply")
	return tmp, env
}

// TestExplainPath_WholeFileProvenance is the base case: a rendered subagent
// resolves to the canonical file that produced it, with the classifier's own
// drift vocabulary.
func TestExplainPath_WholeFileProvenance(t *testing.T) {
	tmp, env := explainFixture(t)
	dest := filepath.Join(tmp, ".claude", "agents", "reviewer.md")

	out, err := runCLI(t, env, "explain", dest)
	if err != nil {
		t.Fatalf("explain %s: %v\n%s", dest, err, out)
	}
	for _, want := range []string{"claude", "subagent reviewer", "subagents/reviewer.md", "managed", "clean"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q; got:\n%s", want, out)
		}
	}
	// It must NOT print the file's content.
	if strings.Contains(out, "Review carefully") {
		t.Errorf("explain printed destination content; it is metadata-only:\n%s", out)
	}
}

// TestExplainPath_KeyLevelResolvesRealSource is the ratified per-key resolution:
// state's KeyEntry.SourceID carries only the coarse "mcp/* (multiple)" sentinel,
// so explain must derive the actual mcp/<id>.toml from the pointer shape.
func TestExplainPath_KeyLevelResolvesRealSource(t *testing.T) {
	tmp, env := explainFixture(t)
	dest := filepath.Join(tmp, ".claude.json")

	out, err := runCLI(t, env, "explain", dest+"#/mcpServers/github")
	if err != nil {
		t.Fatalf("explain key: %v\n%s", err, out)
	}
	if !strings.Contains(out, "mcp/github.toml") {
		t.Errorf("key-level explain did not resolve the real source file; got:\n%s", out)
	}
	if strings.Contains(out, "(multiple)") {
		t.Errorf("key-level explain fell back to the coarse state sentinel; got:\n%s", out)
	}
	// The REFERENCE is reported; the resolved value never is.
	if !strings.Contains(out, "${env:EXPLAIN_TEST_TOKEN}") {
		t.Errorf("secret reference not reported; got:\n%s", out)
	}
	if strings.Contains(out, "s3cr3t-value-xyz") {
		t.Fatalf("explain leaked a resolved secret value:\n%s", out)
	}
}

// TestExplainPath_PointerFlagWinsOverHash pins the documented precedence.
func TestExplainPath_PointerFlagWinsOverHash(t *testing.T) {
	tmp, env := explainFixture(t)
	dest := filepath.Join(tmp, ".claude.json")

	out, err := runCLI(t, env, "explain", dest+"#/nope/nothing", "--pointer", "/mcpServers/github", "--json")
	if err != nil {
		t.Fatalf("explain --pointer: %v\n%s", err, out)
	}
	var m struct {
		Pointer string `json:"pointer"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if m.Pointer != "/mcpServers/github" {
		t.Errorf("--pointer did not win over the in-argument '#'; got %q", m.Pointer)
	}
}

// TestExplainPath_UnmanagedReportedDistinctly mirrors `diff [<path>]`: a typo'd
// or unmanaged path is its own answer, not a silent empty one — and explain
// suggests the nearest managed path.
func TestExplainPath_UnmanagedReportedDistinctly(t *testing.T) {
	tmp, env := explainFixture(t)

	typo := filepath.Join(tmp, ".claude", "agents", "reviewr.md")
	_, err := runCLI(t, env, "explain", typo)
	if err == nil {
		t.Fatal("an unmanaged path must be reported as an error")
	}
	if !strings.Contains(err.Error(), "not managed") {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "reviewer.md") {
		t.Errorf("no nearest-managed-path suggestion: %v", err)
	}

	// A MANAGED path with an unowned key is a different problem, reported as one.
	dest := filepath.Join(tmp, ".claude.json")
	_, err = runCLI(t, env, "explain", dest+"#/mcpServers/nosuchserver")
	if err == nil {
		t.Fatal("an unowned key must be reported as an error")
	}
	if !strings.Contains(err.Error(), "no enabled agent owns the key") {
		t.Errorf("managed-path/unowned-key must be reported distinctly from an unmanaged path: %v", err)
	}

	// --json still emits the payload for a machine consumer, with unmanaged=true.
	stdout, _, jerr := runCLISplit(t, env, "explain", typo, "--json")
	if jerr == nil {
		t.Fatal("--json on an unmanaged path must still exit non-zero")
	}
	var m struct {
		Unmanaged bool `json:"unmanaged"`
	}
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("--json payload missing/invalid on an unmanaged path: %v\n%s", err, stdout)
	}
	if !m.Unmanaged {
		t.Error(`--json payload should carry "unmanaged": true`)
	}
}

// TestExplainPath_ForeignKeyIsFirstClass verifies the merged-file answer users
// actually need — "agentsync did not put this here" — and that the foreign key's
// VALUE is never emitted.
func TestExplainPath_ForeignKeyIsFirstClass(t *testing.T) {
	tmp, env := explainFixture(t)
	dest := filepath.Join(tmp, ".claude.json")

	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["theme"] = "my-private-theme-value"
	out, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(dest, out, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := runCLI(t, env, "explain", dest)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, got)
	}
	if !strings.Contains(got, "/theme") || !strings.Contains(got, "foreign") {
		t.Errorf("foreign key not reported as first-class; got:\n%s", got)
	}
	if strings.Contains(got, "my-private-theme-value") {
		t.Errorf("explain printed a foreign key's VALUE; it is metadata-only:\n%s", got)
	}
}

// TestExplainPath_WorksWithUnresolvableSecret is the ratified diagnostic
// advantage: `diff` must fail closed when a reference cannot be resolved (it
// would print unmaskable cleartext), while explain — which emits no content —
// still answers.
func TestExplainPath_WorksWithUnresolvableSecret(t *testing.T) {
	tmp, env := explainFixture(t)
	dest := filepath.Join(tmp, ".claude.json")

	// Re-point the reference at a var that is NOT set in this shell — the
	// "env var set at apply time, unset now" case. The destination still holds
	// the cleartext a prior apply substituted, so diff cannot mask it.
	mcpTOML := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	raw, err := os.ReadFile(mcpTOML)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpTOML,
		[]byte(strings.ReplaceAll(string(raw), "EXPLAIN_TEST_TOKEN", "EXPLAIN_TEST_NEVER_SET")), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, derr := runCLI(t, env, "diff", dest); derr == nil {
		t.Fatal("diff must fail closed when a reference cannot be resolved (it prints content)")
	}
	got, err := runCLI(t, env, "explain", dest+"#/mcpServers/github")
	if err != nil {
		t.Fatalf("explain must still answer with an unresolvable reference: %v\n%s", err, got)
	}
	if !strings.Contains(got, "mcp/github.toml") {
		t.Errorf("degraded explain lost its provenance answer; got:\n%s", got)
	}
	if strings.Contains(got, "s3cr3t-value-xyz") {
		t.Fatalf("explain leaked the resolved value:\n%s", got)
	}
}

// TestExplainPath_JSONShape pins the committed machine contract: per-owner
// grouping, the ownership vocabulary, the classifier's own drift names, and
// references-only secrets.
func TestExplainPath_JSONShape(t *testing.T) {
	tmp, env := explainFixture(t)
	dest := filepath.Join(tmp, ".claude.json")

	out, err := runCLI(t, env, "explain", dest, "--json")
	if err != nil {
		t.Fatalf("explain --json: %v\n%s", err, out)
	}
	var m struct {
		Path      string `json:"path"`
		Scope     string `json:"scope"`
		Unmanaged bool   `json:"unmanaged"`
		Owners    []struct {
			Agent string `json:"agent"`
			Items []struct {
				Pointer   string `json:"pointer"`
				Ownership string `json:"ownership"`
				Component string `json:"component"`
				Name      string `json:"name"`
				Source    struct {
					File     string `json:"file"`
					Multiple bool   `json:"multiple"`
				} `json:"source"`
				Drift   string   `json:"drift"`
				Secrets []string `json:"secrets"`
			} `json:"items"`
		} `json:"owners"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if m.Path != dest || m.Scope != "user" || m.Unmanaged {
		t.Fatalf("unexpected envelope: %+v", m)
	}
	if len(m.Owners) != 1 || m.Owners[0].Agent != "claude" {
		t.Fatalf("expected one owner (claude); got %+v", m.Owners)
	}
	item := m.Owners[0].Items[0]
	switch {
	case item.Pointer != "/mcpServers/github":
		t.Errorf("pointer = %q", item.Pointer)
	case item.Ownership != "managed":
		t.Errorf("ownership = %q, want managed", item.Ownership)
	case item.Component != "mcp" || item.Name != "github":
		t.Errorf("component/name = %q/%q", item.Component, item.Name)
	case item.Source.File != "mcp/github.toml" || item.Source.Multiple:
		t.Errorf("source = %+v", item.Source)
	case item.Drift != "clean":
		t.Errorf("drift = %q, want the classifier's own vocabulary", item.Drift)
	case len(item.Secrets) != 1 || item.Secrets[0] != "${env:EXPLAIN_TEST_TOKEN}":
		t.Errorf("secrets = %v, want the reference only", item.Secrets)
	}
}

// TestExplainPath_UntrackedBeforeApply is the honest answer on an unapplied
// source tree: explain re-renders live (like `diff`) rather than narrating the
// last apply, and marks what state has not recorded yet.
func TestExplainPath_UntrackedBeforeApply(t *testing.T) {
	tmp, env := explainFixture(t)

	// Add a NEW subagent and do not apply it.
	if err := os.WriteFile(filepath.Join(tmp, ".agentsync", "subagents", "planner.md"),
		[]byte("---\ndescription: planner\n---\nPlan.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(tmp, ".claude", "agents", "planner.md")
	out, err := runCLI(t, env, "explain", dest)
	if err != nil {
		t.Fatalf("explain of an unapplied component must still answer: %v\n%s", err, out)
	}
	if !strings.Contains(out, "untracked") {
		t.Errorf("an unapplied rendered component should read as untracked; got:\n%s", out)
	}
	if !strings.Contains(out, "subagents/planner.md") {
		t.Errorf("provenance lost for an unapplied component; got:\n%s", out)
	}
}

// TestExplainPath_NormalizesSymlinkedPath is the "point at a file" contract:
// `diff` matches by exact string equality after Abs, so a symlinked directory
// reads as unmanaged. explain resolves symlinks before comparing.
func TestExplainPath_NormalizesSymlinkedPath(t *testing.T) {
	tmp, env := explainFixture(t)

	link := filepath.Join(t.TempDir(), "claude-link")
	if err := os.Symlink(filepath.Join(tmp, ".claude"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	via := filepath.Join(link, "agents", "reviewer.md")
	out, err := runCLI(t, env, "explain", via)
	if err != nil {
		t.Fatalf("explain through a symlinked dir must resolve: %v\n%s", err, out)
	}
	if !strings.Contains(out, "subagents/reviewer.md") {
		t.Errorf("symlinked path lost its provenance; got:\n%s", out)
	}
}

// TestExplainPath_MemoryDecomposesFragments covers the corrected premise:
// memory's SourceID names only AGENTS.md even when the source is
// fragment-composed, so a naive answer would under-report exactly the
// provenance this command promises.
func TestExplainPath_MemoryDecomposesFragments(t *testing.T) {
	tmp, env := explainFixture(t)
	memDir := filepath.Join(tmp, ".agentsync", "memory")
	if err := os.MkdirAll(filepath.Join(memDir, "fragments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "fragments", "style.md"), []byte("Be terse.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "AGENTS.md"), []byte("# Rules\n\n@fragments/style.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply")

	dest := filepath.Join(tmp, ".claude", "CLAUDE.md")
	out, err := runCLI(t, env, "explain", dest)
	if err != nil {
		t.Fatalf("explain memory: %v\n%s", err, out)
	}
	if !strings.Contains(out, "memory/AGENTS.md") {
		t.Fatalf("memory provenance missing; got:\n%s", out)
	}
	if !strings.Contains(out, "memory/fragments/style.md") {
		t.Errorf("fragment-composed memory must decompose to its fragments; got:\n%s", out)
	}
	if strings.Contains(out, "Be terse") {
		t.Errorf("explain printed memory CONTENT; it is metadata-only:\n%s", out)
	}
}

// TestExplainPath_RenamedHookEventResolves is the regression for the latent
// HookEventNamer bug this feature made live: gemini spells PreToolUse
// `BeforeTool` natively, so `/hooks/BeforeTool` must resolve to the canonical
// hooks/PreToolUse.toml, not a nonexistent hooks/BeforeTool.toml.
func TestExplainPath_RenamedHookEventResolves(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "gemini")

	hookDir := filepath.Join(tmp, ".agentsync", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "PreToolUse.toml"),
		[]byte("[[hook]]\nmatcher = 'Bash'\ntype = 'command'\ncommand = 'echo hi'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply")

	dest := filepath.Join(tmp, ".gemini", "settings.json")
	out, err := runCLI(t, env, "explain", dest+"#/hooks/BeforeTool")
	if err != nil {
		t.Fatalf("explain renamed hook: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hooks/PreToolUse.toml") {
		t.Fatalf("renamed hook event did not invert to its canonical source; got:\n%s", out)
	}
	if strings.Contains(out, "hooks/BeforeTool.toml") {
		t.Fatalf("explain claimed a nonexistent native-spelled source file:\n%s", out)
	}
}

// TestExplainPath_LegacySubagentSourceIDDegrades covers the sequencing caveat:
// F1's state rewrite is scoped per tree, so explain must tolerate a stale
// old-spelling SourceID rather than claim an `agents/…` file that is gone.
func TestExplainPath_LegacySubagentSourceIDDegrades(t *testing.T) {
	tmp, env := explainFixture(t)
	dest := filepath.Join(tmp, ".claude", "agents", "reviewer.md")

	// Explain resolves the source from the LIVE render, so the current spelling
	// answers; the guard is that a state file still carrying the old spelling
	// does not break the command or produce a phantom source.
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath,
		[]byte(strings.ReplaceAll(string(raw), "subagents/reviewer.md", "agents/reviewer.md")), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "explain", dest)
	if err != nil {
		t.Fatalf("explain must tolerate a stale old-spelling SourceID: %v\n%s", err, out)
	}
	if !strings.Contains(out, "subagents/reviewer.md") {
		t.Errorf("explain should report the CURRENT source spelling; got:\n%s", out)
	}
}

// TestExplainPath_PluginOriginAttributed covers the derive-first attribution:
// nothing in the canonical model marks a component projected-vs-authored, so
// explain establishes the authored set by loading without the plugin cache and
// re-projects each installed plugin in isolation.
func TestExplainPath_PluginOriginAttributed(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mpDir := makeVersionedMarketplace(t, base, "1.2.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "install", "demo@test-mp-v")
	mustRun(t, env, "apply")

	dest := filepath.Join(tmp, ".claude.json")
	out, err := runCLI(t, env, "explain", dest+"#/mcpServers/demo-mcp", "--json")
	if err != nil {
		t.Fatalf("explain plugin-projected key: %v\n%s", err, out)
	}
	var m struct {
		Owners []struct {
			Items []struct {
				Plugin *struct {
					ID           string `json:"id"`
					Version      string `json:"version"`
					AlsoAuthored bool   `json:"alsoAuthored"`
				} `json:"plugin"`
			} `json:"items"`
		} `json:"owners"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(m.Owners) == 0 || len(m.Owners[0].Items) == 0 {
		t.Fatalf("no items reported:\n%s", out)
	}
	pl := m.Owners[0].Items[0].Plugin
	if pl == nil {
		t.Fatalf("plugin origin not attributed for a plugin-projected component:\n%s", out)
	}
	if !strings.HasPrefix(pl.ID, "demo") {
		t.Errorf("plugin id = %q, want the demo plugin", pl.ID)
	}
	if pl.Version != "1.2.0" {
		t.Errorf("plugin version = %q, want the pinned 1.2.0", pl.Version)
	}
	if pl.AlsoAuthored {
		t.Error("a purely plugin-projected component must not be flagged as also authored")
	}
}

// TestExplainPath_UserAuthoredHasNoPluginOrigin is the other half of the
// set-difference: an authored component must not be attributed to a plugin.
func TestExplainPath_UserAuthoredHasNoPluginOrigin(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	base := t.TempDir()

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mpDir := makeVersionedMarketplace(t, base, "1.0.0")
	mustRun(t, env, "marketplace", "add", mpDir)
	mustRun(t, env, "plugin", "install", "demo@test-mp-v")
	mustRun(t, env, "mcp", "add", "mine", "--command", "echo")
	mustRun(t, env, "apply")

	dest := filepath.Join(tmp, ".claude.json")
	out, err := runCLI(t, env, "explain", dest+"#/mcpServers/mine", "--json")
	if err != nil {
		t.Fatalf("explain authored key: %v\n%s", err, out)
	}
	if strings.Contains(out, `"plugin"`) {
		t.Errorf("a user-authored component was attributed to a plugin:\n%s", out)
	}
}

// TestExplainPath_ProjectScopeInferredFromPath covers the ratified scope
// handling: a destination inside a project tree is explained at PROJECT scope
// without the user having to say so — describing the wrong scope's render is
// worse than no answer.
func TestExplainPath_ProjectScopeInferredFromPath(t *testing.T) {
	tmp := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "init", "--scope", "project", "--project", proj)
	declareProjectAgent(t, env, proj, "claude")

	subDir := filepath.Join(proj, ".agentsync", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "projrev.md"),
		[]byte("---\ndescription: project reviewer\n---\nReview.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply", "--project", proj)

	dest := filepath.Join(proj, ".claude", "agents", "projrev.md")
	out, err := runCLI(t, env, "explain", dest, "--json")
	if err != nil {
		t.Fatalf("explain project dest: %v\n%s", err, out)
	}
	var m struct {
		Scope   string `json:"scope"`
		Project string `json:"project"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if m.Scope != "project" {
		t.Fatalf("scope = %q, want project inferred from the destination path\n%s", m.Scope, out)
	}
	if m.Project == "" {
		t.Error("project root not reported")
	}
}

// TestExplainPath_SharedDestGroupsPerAgent pins the per-owner grouping for a
// destination several agents render — the breadth tier's shared
// `~/.agents/skills/` is the real case.
func TestExplainPath_SharedDestGroupsPerAgent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "codex")
	mustRun(t, env, "agent", "add", "goose")

	skillDir := filepath.Join(tmp, ".agentsync", "skills", "tidy")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: tidy\ndescription: tidy up\n---\nTidy.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, env, "apply")

	dest := filepath.Join(tmp, ".agents", "skills", "tidy", "SKILL.md")
	if _, err := os.Stat(dest); err != nil {
		t.Skipf("shared skills dest not produced by this agent set: %v", err)
	}
	out, err := runCLI(t, env, "explain", dest, "--json")
	if err != nil {
		t.Fatalf("explain shared dest: %v\n%s", err, out)
	}
	var m struct {
		Owners []struct {
			Agent string `json:"agent"`
		} `json:"owners"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(m.Owners) < 2 {
		t.Fatalf("a shared destination must group per owning agent; got %+v\n%s", m.Owners, out)
	}
}
