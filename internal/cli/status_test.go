package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatus_DriftAfterDirectEdit(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}

	// Write an MCP server so apply produces a merge-json-keys op.
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Modify destination directly to introduce drift.
	dst := filepath.Join(tmp, ".claude.json")
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	modified := strings.ReplaceAll(string(body), `"npx"`, `"npm"`)
	if err := os.WriteFile(dst, []byte(modified), 0o644); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}

	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "drift") {
		t.Fatalf("status didn't report drift: %s", out)
	}
}

// TestStatus_DriftOnReplaceFile guards against the regression where status
// only classified merge-json-keys ops (MCP/hooks/lsp) and silently dropped
// every "replace"-strategy file (skills, subagents, commands, memory) — so a
// hand-edited SKILL.md / CLAUDE.md / subagent showed no drift at all.
func TestStatus_DriftOnReplaceFile(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}
	sk := filepath.Join(tmp, ".agentsync", "skills", "greet", "SKILL.md")
	_ = os.MkdirAll(filepath.Dir(sk), 0o755)
	_ = os.WriteFile(sk, []byte("---\nname: greet\ndescription: d\n---\nhi\n"), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	dst := filepath.Join(tmp, ".claude", "skills", "greet", "SKILL.md")
	if err := os.WriteFile(dst, []byte("---\nname: greet\ndescription: d\n---\nHAND EDITED\n"), 0o644); err != nil {
		t.Fatalf("edit dst: %v", err)
	}
	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "drift") {
		t.Fatalf("status did not report drift on a replace-strategy skill file:\n%s", out)
	}
}

// TestStatus_SecretItemCleanAfterApply is the regression for status reporting
// phantom "pending" drift forever for a secret-bearing MCP server after a clean
// apply. status hashed the TEMPLATED source ("${env:MY_TOKEN}") while state and
// the dest hold the RESOLVED value apply wrote, so hsrc != happlied == hdest →
// Pending. status must render with secrets resolved (matching apply) so a
// synced secret item classifies Clean.
func TestStatus_SecretItemCleanAfterApply(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "MY_TOKEN": "ghp_resolved_secret"}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n[server.env]\nTOKEN=\"${env:MY_TOKEN}\"\n"), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if strings.Contains(out, "pending") {
		t.Fatalf("status reported phantom pending for a synced secret item:\n%s", out)
	}
	if !strings.Contains(out, "clean") {
		t.Fatalf("expected the synced secret item to classify clean:\n%s", out)
	}
}

// TestStatus_ReportsOrphanedFile is the regression for status reporting clean
// while a whole-file dest agentsync owns but no longer renders (its source
// component was removed) lingers on disk — invisible to status, though the next
// apply or a reconcile would act on it.
func TestStatus_ReportsOrphanedFile(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(tmp, ".agentsync", "skills", "demo", "SKILL.md")
	_ = os.MkdirAll(filepath.Dir(skill), 0o755)
	_ = os.WriteFile(skill, []byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	// Remove the source skill; the rendered dest SKILL.md is now owned-but-unrendered.
	_ = os.RemoveAll(filepath.Join(tmp, ".agentsync", "skills", "demo"))

	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "orphan") {
		t.Fatalf("status should report the orphaned dest file; got:\n%s", out)
	}
}

func TestStatus_CleanAfterApply(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatalf("agent add: %v", err)
	}

	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)

	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	// After clean apply, should report clean or new (state recorded).
	if strings.Contains(out, "drift") || strings.Contains(out, "conflict") {
		t.Fatalf("status reported unexpected drift after clean apply: %s", out)
	}
}

// TestStatus_JSONOutput locks in the contract that --json emits the structured
// drift model: a per-agent items list keyed by drift class plus a summary
// tally. Scripts (CI gates, dashboards) consume this — its shape and the
// drift-class vocabulary are public.
func TestStatus_JSONOutput(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	// Drift the dest so the model has a non-zero "drift" tally.
	dst := filepath.Join(tmp, ".claude.json")
	body, _ := os.ReadFile(dst)
	_ = os.WriteFile(dst, []byte(strings.ReplaceAll(string(body), `"npx"`, `"npm"`)), 0o644)

	out, err := runCLI(t, env, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	var got struct {
		Agents []struct {
			Agent string `json:"agent"`
			Items []struct {
				Path    string `json:"path"`
				Pointer string `json:"pointer"`
				Class   string `json:"class"`
			} `json:"items"`
		} `json:"agents"`
		Summary map[string]int `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("status --json: output not valid JSON: %v\noutput:\n%s", err, out)
	}
	if got.Summary["drift"] != 1 {
		t.Errorf("expected summary.drift=1; got %v\noutput:%s", got.Summary, out)
	}
	if len(got.Agents) != 1 || got.Agents[0].Agent != "claude" {
		t.Errorf("expected one agent 'claude'; got %#v", got.Agents)
	}
}

// TestStatus_LegendExplainsDriftClasses locks in the legend contract: status
// emits a brief "What `apply` will do:" glossary for every drift class that
// actually appears in the summary, so a newcomer can scan from a row to its
// meaning without leaving the terminal. The legend is suppressed entirely
// when everything is clean (the word is self-evident) and entirely from
// --json output (the class field is the machine-readable contract).
func TestStatus_LegendExplainsDriftClasses(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	_ = os.MkdirAll(filepath.Dir(mcp), 0o755)
	_ = os.WriteFile(mcp, []byte("[server]\ntype=\"stdio\"\ncommand=\"npx\"\n"), 0o644)
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	// Drift the dest so the summary contains a non-clean class.
	dst := filepath.Join(tmp, ".claude.json")
	body, _ := os.ReadFile(dst)
	_ = os.WriteFile(dst, []byte(strings.ReplaceAll(string(body), `"npx"`, `"npm"`)), 0o644)

	out, err := runCLI(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "What `apply` will do:") {
		t.Errorf("expected the legend header; got:\n%s", out)
	}
	// The drift class is present in the summary, so its explanation must appear.
	if !strings.Contains(out, "will be overwritten") {
		t.Errorf("expected the 'drift' legend line; got:\n%s", out)
	}
	// "clean" is self-evident and the legend must NOT list it (even though
	// the summary footer counts clean items).
	if strings.Contains(out, "clean ") && strings.Count(out, "clean") > 1 &&
		strings.Contains(out, "no action") {
		t.Errorf("legend should not list 'clean'; got:\n%s", out)
	}
	// --json must stay legend-free so the payload is parseable.
	jsonOut, err := runCLI(t, env, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, jsonOut)
	}
	if strings.Contains(jsonOut, "What `apply` will do:") {
		t.Errorf("--json must not include the legend; got:\n%s", jsonOut)
	}
}
