package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
)

// mcpKeySHA returns the seeded state hash for /mcpServers/<id>, or "".
func mcpKeySHA(t *testing.T, statePath, id string) string {
	t.Helper()
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var st struct {
		Keys map[string]struct {
			SHA256 string `json:"sha256"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	for k, v := range st.Keys {
		if strings.HasSuffix(k, "/mcpServers/"+id) {
			return v.SHA256
		}
	}
	return ""
}

// TestImport_WarningScopedToModeledSections is the regression for the
// post-import warning spamming the user with hundreds of out-of-scope items.
// The previous warning walked every second-level pointer in the dest and
// claimed each would "trigger ForeignCollision on next apply" — but for
// merge-keys ops, the per-pointer OwnedKeys check fires only on keys the op
// claims, so foreign top-level sections (Claude Code's runtime state — e.g.
// `tipsHistory`, `skillUsage`, telemetry caches) are physically incapable of
// colliding. They get preserved untouched. The scoped warning surfaces only
// pointers under sections agentsync's canonical actually renders, and the
// wording no longer makes the false collision claim.
func TestImport_WarningScopedToModeledSections(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	// .claude.json with: one MCP we'll import (foo), a sibling MCP we won't
	// (bar — agentsync MODELS mcpServers so this is in-scope), and a chunk of
	// runtime state under top-level sections agentsync does not model at all
	// (skillUsage, tipsHistory — out of scope, must not appear in the warning).
	claudeJSON := filepath.Join(tmp, ".claude.json")
	body := `{
	  "mcpServers": {
	    "foo": {"type": "stdio", "command": "f"},
	    "bar": {"type": "stdio", "command": "b"}
	  },
	  "skillUsage": {"some-skill": 7},
	  "tipsHistory": {"shift-tab": 12}
	}`
	if err := os.WriteFile(claudeJSON, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:mcp:foo")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}

	// In-scope sibling (mcpServers/bar) is flagged.
	if !strings.Contains(out, "/mcpServers/bar") {
		t.Errorf("in-scope foreign pointer /mcpServers/bar should be flagged; got:\n%s", out)
	}
	// Out-of-scope top-level sections are silent — these were the spam.
	if strings.Contains(out, "skillUsage") {
		t.Errorf("out-of-scope skillUsage must not be flagged; got:\n%s", out)
	}
	if strings.Contains(out, "tipsHistory") {
		t.Errorf("out-of-scope tipsHistory must not be flagged; got:\n%s", out)
	}
	// The factually-wrong collision claim must be gone.
	if strings.Contains(out, "ForeignCollision") {
		t.Errorf("warning must not claim ForeignCollision (merge-keys preserves unowned keys); got:\n%s", out)
	}
}

// TestImport_DoesNotReseedUnimportedSibling is the regression for the
// drift-masking bug: seedStateFromCurrentDest re-rendered the WHOLE canonical
// and re-stamped every pointer, so importing one item silently re-seeded an
// un-imported, drifted sibling at its drifted hash — turning its Drift into
// Pending and causing the next apply to revert the user's edit with no backup.
// Seeding must be scoped to only the items actually imported.
func TestImport_DoesNotReseedUnimportedSibling(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	// Canonical owns foo; apply it so state is seeded for /mcpServers/foo.
	if _, err := runCLI(t, env, "mcp", "add", "foo", "--command", "foo-orig"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "apply"); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	before := mcpKeySHA(t, statePath, "foo")
	if before == "" {
		t.Fatal("setup: foo not seeded after apply")
	}

	// Drift foo in the dest AND add a native bar to import.
	claudeJSON := filepath.Join(tmp, ".claude.json")
	if err := os.WriteFile(claudeJSON,
		[]byte(`{"mcpServers":{"foo":{"type":"stdio","command":"DRIFTED"},"bar":{"type":"stdio","command":"bar-cmd"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Import ONLY bar — foo's drift must be preserved (its state untouched).
	if _, err := runCLI(t, env, "import", "claude:mcp:bar"); err != nil {
		t.Fatalf("import bar: %v", err)
	}
	if after := mcpKeySHA(t, statePath, "foo"); after != before {
		t.Fatalf("importing bar re-seeded the un-imported sibling foo (masking its drift): before=%s after=%s", before, after)
	}
}

// TestImport_InvalidSelector verifies that malformed selectors are rejected.
func TestImport_InvalidSelector(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "import", "badformat")
	if err == nil {
		t.Fatal("expected error for malformed selector; got nil")
	}
}

// TestImport_TrailingColonRejected guards against a footgun: a trailing colon
// (claude:mcp:) parsed to an empty name, which silently meant "bulk import all"
// — surprising for a typo. It must be rejected like an empty component is.
func TestImport_TrailingColonRejected(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "import", "claude:mcp:"); err == nil {
		t.Fatal("expected trailing-colon selector to be rejected, not treated as bulk")
	}
}

// TestImport_DryRunRejectsInvalidID is the regression for a misleading preview:
// --dry-run skipped the writers' validation, so it cheerfully previewed
// "would import mcp/../escape.toml" for a traversal id that a real import
// rejects. The preview must match what a real run accepts.
func TestImport_DryRunRejectsInvalidID(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"),
		[]byte(`{"mcpServers":{"../escape":{"type":"stdio","command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "import", "claude:mcp:../escape", "--dry-run"); err == nil {
		t.Fatal("dry-run should reject an invalid component id, not preview it")
	}
}

// TestImport_RejectsColonInID rejects a native component id containing ':',
// which would write an mcp/ns:gh.toml that is an illegal filename on Windows —
// the canonical source is meant to be portable/committable across machines.
func TestImport_RejectsColonInID(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"),
		[]byte(`{"mcpServers":{"ns:gh":{"type":"stdio","command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "import", "claude:mcp:ns:gh"); err == nil {
		t.Fatal("a colon in a component id is not portable; import must reject it")
	}
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "mcp", "ns:gh.toml")); err == nil {
		t.Fatal("import wrote an unportable mcp/ns:gh.toml")
	}
}

// TestImport_PartialFailureStillSeedsWritten is the regression for a partial
// full-agent import: when a later component fails after an earlier one was
// already written to the canonical source, the written component must still be
// seeded into state — otherwise the next apply treats it as ForeignCollision
// and overwrites the file the user just imported from.
func TestImport_PartialFailureStillSeedsWritten(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	// Native config: an MCP server (imported first, cleanly).
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"),
		[]byte(`{"mcpServers":{"github":{"type":"stdio","command":"gh-mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// ... and a skill (imported after mcp in the full-agent order).
	skillDir := filepath.Join(tmp, ".claude", "skills", "deploy")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: deploy\n---\nDeploy stuff.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force the skill write to fail: pre-create ~/.agentsync/skills/deploy as a
	// FILE so WriteSkill's MkdirAll(skills/deploy) errors mid-import.
	if err := os.MkdirAll(filepath.Join(tmp, ".agentsync", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".agentsync", "skills", "deploy"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Full-agent import: mcp succeeds (written to canonical), skill then fails.
	if _, err := runCLI(t, env, "import", "claude"); err == nil {
		t.Fatal("expected partial import to surface the skill write error")
	}
	// The mcp that WAS written must be seeded, or the next apply foreign-collides
	// and overwrites .claude.json.
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	stData, _ := os.ReadFile(statePath)
	if !strings.Contains(string(stData), "/mcpServers/github") {
		t.Fatalf("partial import left the written mcp unseeded (foreign-collision hazard); state:\n%s", stData)
	}
}

// TestImport_IntraComponentPartialFailureSeedsWritten is the regression for an
// intra-component partial failure: importing a whole component (e.g.
// claude:command) writes item k, then item k+1 fails. The already-written item
// must still be seeded into state, or the next apply treats it as
// ForeignCollision and overwrites the file just imported from. Previously the
// importer returned nil ids on a mid-loop failure, dropping the written item.
func TestImport_IntraComponentPartialFailureSeedsWritten(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	// Two native commands; ReadDir ingests them alphabetically, so a-cmd is
	// written before z-cmd.
	cmdDir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a-cmd", "z-cmd"} {
		if err := os.WriteFile(filepath.Join(cmdDir, n+".md"),
			[]byte("---\nname: "+n+"\n---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Block z-cmd's source write: pre-create commands/z-cmd.md as a non-empty
	// directory so the atomic rename onto it fails mid-import.
	blocked := filepath.Join(tmp, ".agentsync", "commands", "z-cmd.md")
	if err := os.MkdirAll(filepath.Join(blocked, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "import", "claude:command"); err == nil {
		t.Fatal("expected partial import to surface the z-cmd write error")
	}
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	stData, _ := os.ReadFile(statePath)
	if !strings.Contains(string(stData), "a-cmd") {
		t.Fatalf("intra-component partial import left the written a-cmd unseeded (foreign-collision hazard); state:\n%s", stData)
	}
}

// TestImport_UnknownAgent verifies that an unknown agent returns an error.
func TestImport_UnknownAgent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "import", "alienware:mcp:github")
	if err == nil {
		t.Fatal("expected error for unknown agent; got nil")
	}
}

// TestImport_CursorIsImplemented is the regression for cursor graduating from a
// noop placeholder to a real adapter: `import cursor:…` must no longer be
// gated as unimplemented. It proceeds to a real Ingest, which here finds no
// .cursor/mcp.json and reports the component missing — NOT "no implemented
// adapter". (claude, opencode, codex, and cursor are all real adapters now, so
// no valid agent triggers the unimplemented gate.)
func TestImport_CursorIsImplemented(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "import", "cursor:mcp:github")
	if err == nil {
		t.Fatal("expected import of a non-existent server to fail; got nil")
	}
	if strings.Contains(err.Error(), "no implemented adapter") || strings.Contains(err.Error(), "not yet implemented") {
		t.Fatalf("cursor is a real adapter now; import must not be gated as unimplemented, got: %v", err)
	}
}

// TestImport_RejectsTraversalSelector is the end-to-end regression for the
// arbitrary-file-write via import: a foreign native config can carry a
// path-traversal mcpServers key, and `import claude:mcp:<that>` joined it into
// the source path with no validation, writing a .toml outside ~/.agentsync.
func TestImport_RejectsTraversalSelector(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	claudeJSON := filepath.Join(tmp, ".claude.json")
	_ = os.WriteFile(claudeJSON, []byte(`{"mcpServers":{"../../../../escape":{"type":"stdio","command":"x"}}}`), 0o644)

	if _, err := runCLI(t, env, "import", "claude:mcp:../../../../escape"); err == nil {
		t.Fatal("import of a path-traversal mcp id must be rejected")
	}
}

// TestImport_MCPFromClaude verifies that import claude:mcp:<id> reads the MCP
// server from .claude.json and writes it to the canonical source.
// TestImport_MemoryStripsBanner: importing a native memory file that carries the
// agentsync managed-file banner writes the banner-free body to the canonical
// source — the banner must never round-trip back into memory/AGENTS.md.
func TestImport_MemoryStripsBanner(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	body := "# My rules\n\nBe concise.\n"
	native := source.RenderManagedMemory(body, nil, "CLAUDE.md", true) // banner + body, as apply renders it
	if !strings.Contains(native, "agentsync:managed") {
		t.Fatal("test setup: expected a banner in native content")
	}
	memPath := filepath.Join(tmp, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(memPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memPath, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := runCLI(t, env, "import", "claude:memory"); err != nil {
		t.Fatalf("import claude:memory: %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(tmp, ".agentsync", "memory", "AGENTS.md"))
	if err != nil {
		t.Fatalf("canonical memory not written: %v", err)
	}
	if string(got) != body {
		t.Fatalf("canonical memory should be the banner-free body; got:\n%q", got)
	}
	if strings.Contains(string(got), "agentsync:managed") || strings.Contains(string(got), "Managed by [agentsync]") {
		t.Fatalf("banner leaked into canonical memory:\n%s", got)
	}
}

// TestImport_MemoryRejectsReservedMarker: importing a native memory file that
// carries a user-authored block in the reserved `agentsync:managed` namespace is
// rejected at the capture funnel (not silently stripped), so the reserved marker
// never lands in the canonical source.
func TestImport_MemoryRejectsReservedMarker(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// A user-authored block reusing the reserved managed markers — NOT agentsync's
	// banner — so the strip preserves it and the reserved-marker guard must reject.
	native := "# My rules\n\n<!-- agentsync:managed memory-banner -->\nhand-written, not the banner\n<!-- /agentsync:managed memory-banner -->\n"
	memPath := filepath.Join(tmp, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(memPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memPath, []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:memory")
	combined := out
	if err != nil {
		combined += err.Error()
	}
	if !strings.Contains(combined, "reserved marker") {
		t.Fatalf("import should surface the reserved-marker error; err=%v out=%s", err, out)
	}
	// The reserved content must NOT have been persisted to the canonical source.
	if data, rerr := os.ReadFile(filepath.Join(tmp, ".agentsync", "memory", "AGENTS.md")); rerr == nil {
		if strings.Contains(string(data), "agentsync:managed") {
			t.Fatalf("reserved marker leaked into canonical:\n%s", data)
		}
	}
}

func TestImport_MCPFromClaude(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Write a .claude.json with an MCP server entry (simulating native config).
	claudeJSON := filepath.Join(tmp, ".claude.json")
	if err := os.WriteFile(claudeJSON, []byte(`{
		"mcpServers": {
			"github": {
				"type": "stdio",
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-github"]
			}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:mcp:github")
	if err != nil {
		t.Fatalf("import claude:mcp:github: %v\n%s", err, out)
	}
	if !strings.Contains(out, "mcp/github.toml") {
		t.Fatalf("import output missing confirmation; got: %s", out)
	}

	// Verify the canonical source was written.
	tomlPath := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("canonical mcp/github.toml not written: %v", err)
	}
	if !strings.Contains(string(data), "npx") {
		t.Fatalf("canonical mcp/github.toml missing command; got:\n%s", data)
	}
}

// TestImport_RoundTripDoesNotClobberDest is the regression for the
// HIGH-severity finding: previously, \`import claude:mcp:github\` followed
// by \`apply\` saw the existing .claude.json as ForeignCollision and the
// merge silently overwrote any keys the user had hand-edited between the
// import and the apply.
//
// After the fix, import seeds state with the destination's current hash,
// so the next apply classifies the file as Clean (or Pending if the user
// has edited the canonical since) — never ForeignCollision.
func TestImport_RoundTripDoesNotClobberDest(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// User has a hand-managed .claude.json with one MCP server.
	claudeJSON := filepath.Join(tmp, ".claude.json")
	original := `{
  "mcpServers": {
    "github": {
      "type": "stdio",
      "command": "/usr/local/bin/my-github-fork",
      "args": ["--my-flag"]
    }
  },
  "preserveMe": "do not touch"
}`
	if err := os.WriteFile(claudeJSON, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, env, "import", "claude:mcp:github"); err != nil {
		t.Fatalf("import: %v", err)
	}
	// State must now contain a key entry for /mcpServers/github so that
	// the next apply sees Clean, not ForeignCollision.
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	stData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	if !strings.Contains(string(stData), "/mcpServers/github") {
		t.Fatalf("state missing /mcpServers/github after import; have:\n%s", stData)
	}
	// And the user's foreign top-level key must still be there.
	after, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "preserveMe") {
		t.Fatalf("import clobbered foreign key; .claude.json now:\n%s", after)
	}
}

// TestImport_MCPNotFound verifies that importing a non-existent MCP server errors.
func TestImport_MCPNotFound(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// .claude.json exists but has no MCP servers.
	claudeJSON := filepath.Join(tmp, ".claude.json")
	if err := os.WriteFile(claudeJSON, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "import", "claude:mcp:nonexistent")
	if err == nil {
		t.Fatal("expected error for missing MCP server; got nil")
	}
}

// TestImport_SubagentFromClaude verifies that import claude:agent:<name> reads
// a subagent .md file and writes it into the canonical agents/ directory.
func TestImport_SubagentFromClaude(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Write a subagent file in Claude's native location.
	agentsDir := filepath.Join(tmp, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"),
		[]byte("---\ndescription: \"Code reviewer\"\n---\nReview this code.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:agent:reviewer")
	if err != nil {
		t.Fatalf("import claude:agent:reviewer: %v\n%s", err, out)
	}
	if !strings.Contains(out, "agents/reviewer.md") {
		t.Fatalf("import output missing confirmation; got: %s", out)
	}

	// Verify canonical source was written.
	canonicalPath := filepath.Join(tmp, ".agentsync", "agents", "reviewer.md")
	data, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("canonical agents/reviewer.md not written: %v", err)
	}
	if !strings.Contains(string(data), "reviewer") && !strings.Contains(string(data), "Review") {
		t.Fatalf("canonical agents/reviewer.md missing content; got:\n%s", data)
	}
}

// TestImport_CommandFromClaude verifies import claude:command:<name>.
func TestImport_CommandFromClaude(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Write a command file in Claude's native location.
	commandsDir := filepath.Join(tmp, ".claude", "commands")
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "review.md"),
		[]byte("Do a comprehensive code review."), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:command:review")
	if err != nil {
		t.Fatalf("import claude:command:review: %v\n%s", err, out)
	}
	if !strings.Contains(out, "commands/review.md") {
		t.Fatalf("import output missing confirmation; got: %s", out)
	}

	canonicalPath := filepath.Join(tmp, ".agentsync", "commands", "review.md")
	if _, err := os.Stat(canonicalPath); err != nil {
		t.Fatalf("canonical commands/review.md not written: %v", err)
	}
}

// importTestEnv inits + registers claude and returns (tmp, env).
func importTestEnv(t *testing.T) (string, map[string]string) {
	t.Helper()
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	return tmp, env
}

// TestImport_SkillFromClaude round-trips a native SKILL.md into the source.
func TestImport_SkillFromClaude(t *testing.T) {
	tmp, env := importTestEnv(t)
	skillDir := filepath.Join(tmp, ".claude", "skills", "deploy")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: deploy\ndescription: ship it\n---\nSteps to deploy.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:skill:deploy"); err != nil {
		t.Fatalf("import skill: %v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(tmp, ".agentsync", "skills", "deploy", "SKILL.md"))
	if err != nil {
		t.Fatalf("source skill not written: %v", err)
	}
	if !strings.Contains(string(data), "Steps to deploy") {
		t.Fatalf("skill body not captured:\n%s", data)
	}
}

// TestImport_BundledSkillFromClaude is the CLI-level artifact-anchored counterpart
// to the adapter's TestIngest_RoundTripsSkillBundledFiles: it seeds a SPEC-COMPLETE
// on-disk skill directory (SKILL.md + scripts/ + references/ + assets/ + a nested
// references/sub/) in Claude's native location and asserts `import claude:skill:deploy`
// captures the WHOLE directory into the canonical source — every bundled file
// byte-for-byte and the script's 0o755 exec bit — not just SKILL.md. This is the
// end-to-end guard for the "a skill is a directory, not just SKILL.md" fidelity rule
// on the import write-back path (source.WriteSkill), which the older
// TestImport_SkillFromClaude (SKILL.md only) does not exercise.
func TestImport_BundledSkillFromClaude(t *testing.T) {
	tmp, env := importTestEnv(t)
	skillDir := filepath.Join(tmp, ".claude", "skills", "deploy")

	binary := []byte{0x00, 0x01, 0xff, 0x7f}
	type seed struct {
		rel     string
		content []byte
		mode    os.FileMode
	}
	// A spec-complete skill directory: SKILL.md plus bundled scripts/, references/,
	// assets/, and a nested references/sub/ file.
	seeds := []seed{
		{"SKILL.md", []byte("---\nname: deploy\ndescription: ship it\n---\nSteps to deploy.\n"), 0o644},
		{"scripts/deploy.sh", []byte("#!/bin/sh\necho deploying\n"), 0o755},
		{"references/notes.md", []byte("# Notes\nread me\n"), 0o644},
		{"assets/logo.png", binary, 0o644},
		{"references/sub/extra.md", []byte("nested reference\n"), 0o644},
	}
	for _, s := range seeds {
		abs := filepath.Join(skillDir, filepath.FromSlash(s.rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, s.content, s.mode); err != nil {
			t.Fatal(err)
		}
		// os.WriteFile only honors perm on create; pin the exec bit explicitly.
		if err := os.Chmod(abs, s.mode); err != nil {
			t.Fatal(err)
		}
	}

	if out, err := runCLI(t, env, "import", "claude:skill:deploy"); err != nil {
		t.Fatalf("import bundled skill: %v\n%s", err, out)
	}

	canonicalDir := filepath.Join(tmp, ".agentsync", "skills", "deploy")
	for _, s := range seeds {
		abs := filepath.Join(canonicalDir, filepath.FromSlash(s.rel))
		got, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("bundled path %q not written to canonical skill dir: %v", s.rel, err)
		}
		if s.rel == "SKILL.md" {
			// SKILL.md is frontmatter-parsed and re-encoded, so its exact bytes are
			// not guaranteed; assert the body survives (the bundled files below carry
			// the byte-for-byte guarantee).
			if !bytes.Contains(got, []byte("Steps to deploy")) {
				t.Fatalf("SKILL.md body not captured:\n%s", got)
			}
			continue
		}
		// Bundled resources are captured verbatim — byte-for-byte, binary included.
		if !bytes.Equal(got, s.content) {
			t.Fatalf("bundled path %q bytes mismatch:\n got %q\nwant %q", s.rel, got, s.content)
		}
	}
	// The executable bit on scripts/deploy.sh survives the import.
	info, err := os.Stat(filepath.Join(canonicalDir, "scripts", "deploy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("scripts/deploy.sh mode = %o, want 0755 (exec bit lost on import)", info.Mode().Perm())
	}
}

// TestImport_HookFromClaude round-trips a settings.json hook into the source.
func TestImport_HookFromClaude(t *testing.T) {
	tmp, env := importTestEnv(t)
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{
		"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi"}]}]}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:hook:PreToolUse"); err != nil {
		t.Fatalf("import hook: %v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(tmp, ".agentsync", "hooks", "PreToolUse.toml"))
	if err != nil {
		t.Fatalf("source hook not written: %v", err)
	}
	if !strings.Contains(string(data), "echo hi") {
		t.Fatalf("hook command not captured:\n%s", data)
	}
}

// TestImport_LSPFromClaudeSettingsFails verifies that agentsync does not import
// Claude settings.json#/lspServers as native LSP: Claude Code ignores that key
// and only loads LSP servers from plugin manifests.
func TestImport_LSPFromClaudeSettingsFails(t *testing.T) {
	tmp, env := importTestEnv(t)
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"lspServers": {"gopls": {"command": "gopls", "args": ["-rpc"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:lsp:gopls"); err == nil {
		t.Fatalf("import lsp unexpectedly succeeded from ignored settings.json key:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "lsp", "gopls.toml")); !os.IsNotExist(err) {
		t.Fatalf("ignored Claude settings.json lspServers key was written to canonical source: %v", err)
	}
}

// TestImport_MemoryReversesFragmentEdit is the bidirectional payoff: apply
// writes fragment markers into the native memory file; a hand-edit inside a
// fragment block is reversed by `import` back into the fragment FILE (not
// flattened into AGENTS.md), and the @import structure survives.
func TestImport_MemoryReversesFragmentEdit(t *testing.T) {
	tmp, env := importTestEnv(t)

	memDir := filepath.Join(tmp, ".agentsync", "memory")
	fragDir := filepath.Join(memDir, "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcAgents := "# Memory\n@import ./fragments/style.md\n"
	if err := os.WriteFile(filepath.Join(memDir, "AGENTS.md"), []byte(srcAgents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "style.md"), []byte("Be concise.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// apply renders the marked memory to the native file.
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	dest := filepath.Join(tmp, ".claude", "CLAUDE.md")
	rendered, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "<!-- agentsync:fragment style.md -->") {
		t.Fatalf("apply did not emit fragment markers:\n%s", rendered)
	}

	// Hand-edit the content INSIDE the fragment block, then import it back.
	edited := strings.Replace(string(rendered), "Be concise.", "Be very concise.", 1)
	if edited == string(rendered) {
		t.Fatal("test setup: fragment content not found in rendered memory")
	}
	if err := os.WriteFile(dest, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:memory"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}

	// The edit landed in the FRAGMENT file; AGENTS.md still @imports it.
	frag, _ := os.ReadFile(filepath.Join(fragDir, "style.md"))
	if string(frag) != "Be very concise.\n" {
		t.Fatalf("fragment not updated by reverse-collapse: %q", frag)
	}
	agents, _ := os.ReadFile(filepath.Join(memDir, "AGENTS.md"))
	if !strings.Contains(string(agents), "@import ./fragments/style.md") {
		t.Fatalf("AGENTS.md lost its @import (flattened): %q", agents)
	}
	if strings.Contains(string(agents), "Be very concise") {
		t.Fatalf("edit was flattened into AGENTS.md instead of the fragment: %q", agents)
	}
}

// TestImport_MemoryFromClaude round-trips CLAUDE.md into memory/AGENTS.md.
func TestImport_MemoryFromClaude(t *testing.T) {
	tmp, env := importTestEnv(t)
	if err := os.MkdirAll(filepath.Join(tmp, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".claude", "CLAUDE.md"), []byte("# My project memory\nBe concise.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:memory"); err != nil {
		t.Fatalf("import memory: %v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(tmp, ".agentsync", "memory", "AGENTS.md"))
	if err != nil {
		t.Fatalf("source memory not written: %v", err)
	}
	if !strings.Contains(string(data), "Be concise") {
		t.Fatalf("memory body not captured:\n%s", data)
	}
}

// TestImport_MemorySkippedWhenSourceUsesFragments guards the silent flatten-and-
// orphan hazard: if the canonical memory is fragment-composed, importing the
// (fully expanded) destination CLAUDE.md must NOT overwrite memory/AGENTS.md —
// that would inline the @imports and orphan the fragment files. Import skips it
// with a warning and leaves the source untouched.
func TestImport_MemorySkippedWhenSourceUsesFragments(t *testing.T) {
	tmp, env := importTestEnv(t)

	// Canonical memory composed of a fragment.
	memDir := filepath.Join(tmp, ".agentsync", "memory")
	fragDir := filepath.Join(memDir, "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcAgents := "# Memory\n@import ./fragments/style.md\n"
	if err := os.WriteFile(filepath.Join(memDir, "AGENTS.md"), []byte(srcAgents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "style.md"), []byte("Be concise.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A native (expanded) CLAUDE.md to import.
	if err := os.MkdirAll(filepath.Join(tmp, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".claude", "CLAUDE.md"), []byte("# Memory\nBe concise.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:memory")
	if err != nil {
		t.Fatalf("import should not error, just skip memory: %v\n%s", err, out)
	}
	// Source AGENTS.md must be untouched (still references the fragment).
	got, _ := os.ReadFile(filepath.Join(memDir, "AGENTS.md"))
	if string(got) != srcAgents {
		t.Fatalf("fragment-composed memory was flattened by import: %q", got)
	}
	if _, err := os.Stat(filepath.Join(fragDir, "style.md")); err != nil {
		t.Fatalf("fragment file was orphaned/removed: %v", err)
	}
}

// TestImport_AllMCPFromClaude verifies the bulk component form: import
// claude:mcp (no name) captures every native MCP server in one pass.
func TestImport_AllMCPFromClaude(t *testing.T) {
	tmp, env := importTestEnv(t)
	claudeJSON := filepath.Join(tmp, ".claude.json")
	if err := os.WriteFile(claudeJSON, []byte(`{
		"mcpServers": {
			"github": {"type": "stdio", "command": "npx", "args": ["-y", "server-github"]},
			"linear":  {"type": "stdio", "command": "npx", "args": ["-y", "server-linear"]}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:mcp")
	if err != nil {
		t.Fatalf("import claude:mcp: %v\n%s", err, out)
	}
	for _, want := range []string{"mcp/github.toml", "mcp/linear.toml"} {
		if !strings.Contains(out, want) {
			t.Fatalf("bulk import output missing %q; got: %s", want, out)
		}
	}
	for _, id := range []string{"github", "linear"} {
		if _, statErr := os.Stat(filepath.Join(tmp, ".agentsync", "mcp", id+".toml")); statErr != nil {
			t.Fatalf("canonical mcp/%s.toml not written: %v", id, statErr)
		}
	}
}

// TestImport_FullAgentFromClaude verifies the full-agent form: import claude
// (no component) captures every importable component and prints a summary.
func TestImport_FullAgentFromClaude(t *testing.T) {
	tmp, env := importTestEnv(t)

	// Component dirs under .claude/.
	agentsDir := filepath.Join(tmp, ".claude", "agents")
	commandsDir := filepath.Join(tmp, ".claude", "commands")
	skillDir := filepath.Join(tmp, ".claude", "skills", "deploy")
	for _, d := range []string{agentsDir, commandsDir, skillDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// MCP via .claude.json.
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"),
		[]byte(`{"mcpServers": {"github": {"type": "stdio", "command": "npx"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Hooks via settings.json. A stale lspServers key is present too, but Claude
	// Code ignores that key, so full-agent import must not capture it as native
	// LSP.
	if err := os.WriteFile(filepath.Join(tmp, ".claude", "settings.json"), []byte(`{
		"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi"}]}]},
		"lspServers": {"gopls": {"command": "gopls"}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"),
		[]byte("---\ndescription: \"Code reviewer\"\n---\nReview.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "review.md"), []byte("Do a review."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: deploy\ndescription: ship it\n---\nDeploy.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".claude", "CLAUDE.md"), []byte("# Memory\nBe concise.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude")
	if err != nil {
		t.Fatalf("import claude: %v\n%s", err, out)
	}
	if !strings.Contains(out, "imported") || !strings.Contains(out, "from claude") {
		t.Fatalf("full-agent import missing summary line; got: %s", out)
	}

	// Every importable component should have landed in the canonical source.
	wantFiles := []string{
		filepath.Join("mcp", "github.toml"),
		filepath.Join("hooks", "PreToolUse.toml"),
		filepath.Join("agents", "reviewer.md"),
		filepath.Join("commands", "review.md"),
		filepath.Join("skills", "deploy", "SKILL.md"),
		filepath.Join("memory", "AGENTS.md"),
	}
	for _, rel := range wantFiles {
		if _, statErr := os.Stat(filepath.Join(tmp, ".agentsync", rel)); statErr != nil {
			t.Fatalf("full-agent import did not write %s: %v", rel, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(tmp, ".agentsync", "lsp", "gopls.toml")); !os.IsNotExist(statErr) {
		t.Fatalf("full-agent import captured ignored Claude settings.json lspServers key: %v", statErr)
	}
}

// TestImport_FullAgentEmpty reports cleanly when there is nothing to import.
func TestImport_FullAgentEmpty(t *testing.T) {
	_, env := importTestEnv(t)
	out, err := runCLI(t, env, "import", "claude")
	if err != nil {
		t.Fatalf("full-agent import of an empty config should not error; out=%s err=%v", out, err)
	}
	if !strings.Contains(out, "no importable items found") {
		t.Fatalf("expected an empty-config notice; got: %s", out)
	}
}

// TestImport_DryRunWritesNothing verifies --dry-run previews the target
// without writing the source file or seeding state.
func TestImport_DryRunWritesNothing(t *testing.T) {
	tmp, env := importTestEnv(t)
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"),
		[]byte(`{"mcpServers": {"github": {"type": "stdio", "command": "npx"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:mcp:github", "--dry-run")
	if err != nil {
		t.Fatalf("import --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would import mcp/github.toml") {
		t.Fatalf("dry-run should preview the target; got: %s", out)
	}
	if strings.Contains(out, "imported mcp/github.toml") && !strings.Contains(out, "would import") {
		t.Fatalf("dry-run must not claim a real import; got: %s", out)
	}
	if _, statErr := os.Stat(filepath.Join(tmp, ".agentsync", "mcp", "github.toml")); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run must not write the canonical source; stat err=%v", statErr)
	}

	// A real import afterwards still works (dry-run left no partial state).
	if out, err := runCLI(t, env, "import", "claude:mcp:github"); err != nil {
		t.Fatalf("real import after dry-run: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(tmp, ".agentsync", "mcp", "github.toml")); statErr != nil {
		t.Fatalf("real import after dry-run did not write source: %v", statErr)
	}
}

// TestImport_DryRunFullAgent verifies --dry-run on the full-agent form
// previews a summary and writes none of the components.
func TestImport_DryRunFullAgent(t *testing.T) {
	tmp, env := importTestEnv(t)
	if err := os.MkdirAll(filepath.Join(tmp, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"),
		[]byte(`{"mcpServers": {"github": {"type": "stdio", "command": "npx"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".claude", "CLAUDE.md"), []byte("# Memory\nBe concise.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude", "--dry-run")
	if err != nil {
		t.Fatalf("import claude --dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would import") || !strings.Contains(out, "from claude") {
		t.Fatalf("dry-run full-agent summary missing; got: %s", out)
	}
	for _, rel := range []string{
		filepath.Join("mcp", "github.toml"),
		filepath.Join("memory", "AGENTS.md"),
	} {
		if _, statErr := os.Stat(filepath.Join(tmp, ".agentsync", rel)); !os.IsNotExist(statErr) {
			t.Fatalf("dry-run must not write %s; stat err=%v", rel, statErr)
		}
	}
}

// TestImport_UnknownComponent verifies that an unknown component errors.
func TestImport_UnknownComponent(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "import", "claude:widget:foo")
	if err == nil {
		t.Fatal("expected error for unknown component 'widget'; got nil")
	}
}

// TestImport_RetiresStaleHookOnNativeEnrichment closes the second-order
// issue #124 corruption path: a hook event captured while it was a clean
// command hook, then enriched natively (here: a "timeout" field), is refused by
// ingest — but before this fix the stale canonical hooks/<event>.toml kept the
// next apply owning the whole per-event array, rewriting the user's enriched
// native entry without the timeout. import must retire the stale canonical file
// so apply leaves the native entry byte-untouched.
func TestImport_RetiresStaleHookOnNativeEnrichment(t *testing.T) {
	tmp, env := importTestEnv(t)
	mustRun(t, env, "agent", "add", "claude")
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := `{
		"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi"}]}]}
	}`
	if err := os.WriteFile(settings, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	// First import captures the clean command hook into canonical.
	if out, err := runCLI(t, env, "import", "claude:hook:PreToolUse"); err != nil {
		t.Fatalf("import clean hook: %v\n%s", err, out)
	}
	canonical := filepath.Join(tmp, ".agentsync", "hooks", "PreToolUse.toml")
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("precondition: canonical hook not captured: %v", err)
	}

	// The user enriches the native entry with a field agentsync cannot model.
	enriched := `{
		"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi", "timeout": 30}]}]}
	}`
	if err := os.WriteFile(settings, []byte(enriched), 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-import: the event is refused (warning) AND the stale canonical file is
	// retired, with a notice naming it.
	out, err := runCLI(t, env, "import", "claude")
	if err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "retired canonical hooks/PreToolUse.toml") {
		t.Fatalf("import did not announce the stale-hook retirement:\n%s", out)
	}
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Fatalf("stale canonical hooks/PreToolUse.toml should be retired; stat err=%v", err)
	}

	// The apply after the retirement must leave the enriched native entry
	// byte-for-byte untouched — agentsync no longer owns the event.
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	got, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != enriched {
		t.Fatalf("apply rewrote the native hooks entry it no longer owns:\n got %s\nwant %s", got, enriched)
	}
}

// TestImport_RetiresStaleHookOnGeminiEnrichment is the gemini-driven twin of
// TestImport_RetiresStaleHookOnNativeEnrichment, exercising the renaming leg
// end-to-end: the native entry is spelled "BeforeTool" in .gemini/settings.json,
// gemini's HookIngestGuard reports the refusal under the CANONICAL name, import
// retires hooks/PreToolUse.toml, and the next apply leaves the enriched native
// entry byte-untouched.
func TestImport_RetiresStaleHookOnGeminiEnrichment(t *testing.T) {
	tmp, env := importTestEnv(t)
	mustRun(t, env, "agent", "add", "gemini")
	settings := filepath.Join(tmp, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := `{
		"hooks": {"BeforeTool": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi"}]}]}
	}`
	if err := os.WriteFile(settings, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	// First import captures the clean command hook into canonical under its
	// canonical event name.
	if out, err := runCLI(t, env, "import", "gemini:hook:PreToolUse"); err != nil {
		t.Fatalf("import clean hook: %v\n%s", err, out)
	}
	canonical := filepath.Join(tmp, ".agentsync", "hooks", "PreToolUse.toml")
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("precondition: canonical hook not captured: %v", err)
	}

	// The user enriches the native BeforeTool entry with a field agentsync
	// cannot model.
	enriched := `{
		"hooks": {"BeforeTool": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi", "timeout": 30}]}]}
	}`
	if err := os.WriteFile(settings, []byte(enriched), 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-import: the refusal is reported under the canonical name and the stale
	// canonical file is retired.
	out, err := runCLI(t, env, "import", "gemini")
	if err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "retired canonical hooks/PreToolUse.toml") {
		t.Fatalf("import did not announce the stale-hook retirement under the canonical name:\n%s", out)
	}
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Fatalf("stale canonical hooks/PreToolUse.toml should be retired; stat err=%v", err)
	}

	// The apply after the retirement must leave the enriched native entry
	// byte-for-byte untouched — agentsync no longer owns the event.
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	got, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != enriched {
		t.Fatalf("apply rewrote the native hooks entry it no longer owns:\n got %s\nwant %s", got, enriched)
	}
}

// TestImport_RetiresStaleHookOnCursorEnrichment is the cursor-driven twin of
// TestImport_RetiresStaleHookOnGeminiEnrichment, over Cursor's FLAT hooks.json
// shape and camelCase renaming: the native entry is spelled "preToolUse",
// cursor's HookIngestGuard reports the refusal under the CANONICAL name,
// import retires hooks/PreToolUse.toml, and the next apply leaves the
// enriched native file byte-untouched.
func TestImport_RetiresStaleHookOnCursorEnrichment(t *testing.T) {
	tmp, env := importTestEnv(t)
	mustRun(t, env, "agent", "add", "cursor")
	hooksPath := filepath.Join(tmp, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := `{
		"version": 1,
		"hooks": {"preToolUse": [{"command": "echo hi", "matcher": "Bash"}]}
	}`
	if err := os.WriteFile(hooksPath, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	// First import captures the clean command hook into canonical under its
	// canonical event name.
	if out, err := runCLI(t, env, "import", "cursor:hook:PreToolUse"); err != nil {
		t.Fatalf("import clean hook: %v\n%s", err, out)
	}
	canonical := filepath.Join(tmp, ".agentsync", "hooks", "PreToolUse.toml")
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("precondition: canonical hook not captured: %v", err)
	}

	// The user enriches the native preToolUse entry with a field agentsync
	// cannot model.
	enriched := `{
		"version": 1,
		"hooks": {"preToolUse": [{"command": "echo hi", "matcher": "Bash", "timeout": 30}]}
	}`
	if err := os.WriteFile(hooksPath, []byte(enriched), 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-import: the refusal is reported under the canonical name and the stale
	// canonical file is retired.
	out, err := runCLI(t, env, "import", "cursor")
	if err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "retired canonical hooks/PreToolUse.toml") {
		t.Fatalf("import did not announce the stale-hook retirement under the canonical name:\n%s", out)
	}
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Fatalf("stale canonical hooks/PreToolUse.toml should be retired; stat err=%v", err)
	}

	// The apply after the retirement must leave the enriched native file
	// byte-for-byte untouched — agentsync no longer owns the event.
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	got, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != enriched {
		t.Fatalf("apply rewrote the native hooks file it no longer owns:\n got %s\nwant %s", got, enriched)
	}
}

// TestImport_RetiresStaleHookOnCodexEnrichment is the codex-driven twin of the
// claude/gemini/cursor enrichment tests, over Codex's TOML config: codex
// spells events canonically, so no renaming leg — the load-bearing difference
// is the format (config.toml `[[hooks.PreToolUse]]` arrays-of-tables) and
// that codex's guard landed last (round-2 review; it was the one hook ingest
// still capturing a lossy modeled subset).
func TestImport_RetiresStaleHookOnCodexEnrichment(t *testing.T) {
	tmp, env := importTestEnv(t)
	mustRun(t, env, "agent", "add", "codex")
	cfgPath := filepath.Join(tmp, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := `[[hooks.PreToolUse]]
matcher = "Bash"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "echo hi"
`
	if err := os.WriteFile(cfgPath, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	// First import captures the clean command hook into canonical.
	if out, err := runCLI(t, env, "import", "codex:hook:PreToolUse"); err != nil {
		t.Fatalf("import clean hook: %v\n%s", err, out)
	}
	canonical := filepath.Join(tmp, ".agentsync", "hooks", "PreToolUse.toml")
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("precondition: canonical hook not captured: %v", err)
	}

	// The user enriches the native handler with a field agentsync cannot model.
	enriched := `[[hooks.PreToolUse]]
matcher = "Bash"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "echo hi"
timeout = 30
`
	if err := os.WriteFile(cfgPath, []byte(enriched), 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-import: the event is refused and the stale canonical file is retired.
	out, err := runCLI(t, env, "import", "codex")
	if err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "retired canonical hooks/PreToolUse.toml") {
		t.Fatalf("import did not announce the stale-hook retirement:\n%s", out)
	}
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Fatalf("stale canonical hooks/PreToolUse.toml should be retired; stat err=%v", err)
	}

	// The apply after the retirement must leave the enriched native config
	// byte-for-byte untouched — agentsync no longer owns the event.
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != enriched {
		t.Fatalf("apply rewrote the native hooks config it no longer owns:\n got %s\nwant %s", got, enriched)
	}
}

// TestImport_GeminiNamespacedCommandSkippedNotAborted pins the bulk-import
// behavior for a namespaced Gemini command: `commands/git/commit.toml` is
// captured by ingest as Command{Name: "git/commit"}, which the flat canonical
// namespace rejects (ValidateComponentID refuses '/'). That single native file
// used to fail the ENTIRE bulk import — commands, hooks, memory, everything.
// It must instead be skipped with a warning while its flat sibling imports.
func TestImport_GeminiNamespacedCommandSkippedNotAborted(t *testing.T) {
	tmp, env := importTestEnv(t)
	commandsDir := filepath.Join(tmp, ".gemini", "commands")
	if err := os.MkdirAll(filepath.Join(commandsDir, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "git", "commit.toml"),
		[]byte("description = \"commit helper\"\nprompt = \"commit it\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "deploy.toml"),
		[]byte("description = \"deploy helper\"\nprompt = \"ship it\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "gemini")
	if err != nil {
		t.Fatalf("bulk import must not abort on a namespaced command: %v\n%s", err, out)
	}
	if !strings.Contains(out, "git/commit") {
		t.Fatalf("skip warning should name the namespaced command:\n%s", out)
	}
	// The flat sibling imported despite the namespaced skip.
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "commands", "deploy.md")); err != nil {
		t.Fatalf("flat command should have been imported: %v", err)
	}
	// The namespaced command was skipped, not flattened into canonical.
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "commands", "commit.md")); !os.IsNotExist(err) {
		t.Fatalf("namespaced command must not be captured under a truncated name; stat err=%v", err)
	}
	// A named single-item import of the namespaced command still fails loudly.
	if _, err := runCLI(t, env, "import", "gemini:command:git/commit"); err == nil {
		t.Fatal("named import of a namespaced command should fail loudly")
	}
}

// TestImport_RetireFreezesOtherAgentsHooks pins the cross-agent retirement
// semantics: canonical hooks are SHARED (source.Hook has no per-agent
// targeting), so retiring hooks/<event>.toml because CLAUDE's native entry
// became unrepresentable must not let the next apply's orphan-key cleanup
// DELETE the event from codex's native config — every agent's "/hooks/<event>"
// state key is disowned and every native entry is left frozen as-is.
func TestImport_RetireFreezesOtherAgentsHooks(t *testing.T) {
	tmp, env := importTestEnv(t)
	mustRun(t, env, "agent", "add", "claude")
	mustRun(t, env, "agent", "add", "codex")
	// gemini and cursor RENAME hook events natively (PreToolUse -> BeforeTool /
	// preToolUse), so their state keys are only disowned via the
	// adapter.HookEventNamer alias set — the round that matched canonical names
	// only wiped gemini's settings.json hooks on the next apply.
	mustRun(t, env, "agent", "add", "gemini")
	mustRun(t, env, "agent", "add", "cursor")
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi"}]}]}}`
	if err := os.WriteFile(settings, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:hook:PreToolUse"); err != nil {
		t.Fatalf("import clean hook: %v\n%s", err, out)
	}
	// Apply projects the shared canonical hook into BOTH agents' configs.
	if out, err := runCLI(t, env, "apply", "--no-git-backup"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	codexConfig := filepath.Join(tmp, ".codex", "config.toml")
	codexBefore, err := os.ReadFile(codexConfig)
	if err != nil || !strings.Contains(string(codexBefore), "echo hi") {
		t.Fatalf("precondition: codex config should carry the hook (err=%v):\n%s", err, codexBefore)
	}
	geminiConfig := filepath.Join(tmp, ".gemini", "settings.json")
	geminiBefore, err := os.ReadFile(geminiConfig)
	if err != nil || !strings.Contains(string(geminiBefore), "BeforeTool") {
		t.Fatalf("precondition: gemini config should carry the renamed hook (err=%v):\n%s", err, geminiBefore)
	}
	cursorConfig := filepath.Join(tmp, ".cursor", "hooks.json")
	cursorBefore, err := os.ReadFile(cursorConfig)
	if err != nil || !strings.Contains(string(cursorBefore), "preToolUse") {
		t.Fatalf("precondition: cursor config should carry the renamed hook (err=%v):\n%s", err, cursorBefore)
	}

	// Claude's native entry is enriched; re-import retires the SHARED canonical.
	enriched := `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi", "timeout": 30}]}]}}`
	if err := os.WriteFile(settings, []byte(enriched), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude"); err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}

	// The apply after retirement must leave codex's native hook FROZEN — not
	// orphan-deleted (codex's state key was disowned along with claude's).
	if out, err := runCLI(t, env, "apply", "--no-git-backup"); err != nil {
		t.Fatalf("apply after retirement: %v\n%s", err, out)
	}
	codexAfter, err := os.ReadFile(codexConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(codexAfter), "echo hi") {
		t.Fatalf("retirement let orphan cleanup delete codex's native hook:\nbefore:\n%s\nafter:\n%s", codexBefore, codexAfter)
	}
	geminiAfter, err := os.ReadFile(geminiConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(geminiAfter), "BeforeTool") || !strings.Contains(string(geminiAfter), "echo hi") {
		t.Fatalf("retirement let orphan cleanup delete gemini's RENAMED native hook:\nbefore:\n%s\nafter:\n%s", geminiBefore, geminiAfter)
	}
	cursorAfter, err := os.ReadFile(cursorConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cursorAfter), "preToolUse") || !strings.Contains(string(cursorAfter), "echo hi") {
		t.Fatalf("retirement let orphan cleanup delete cursor's RENAMED native hook:\nbefore:\n%s\nafter:\n%s", cursorBefore, cursorAfter)
	}
	if got, _ := os.ReadFile(settings); string(got) != enriched {
		t.Fatalf("claude's enriched native entry should be untouched:\n got %s\nwant %s", got, enriched)
	}
}

// TestImport_NamedHookRetireScopedToName pins that `import claude:hook:<event>`
// retires ONLY the named event — a sibling refused event's canonical file must
// survive a named import the user didn't ask about.
func TestImport_NamedHookRetireScopedToName(t *testing.T) {
	tmp, env := importTestEnv(t)
	mustRun(t, env, "agent", "add", "claude")
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := `{"hooks": {
		"PreToolUse":  [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo pre"}]}],
		"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo post"}]}]
	}}`
	if err := os.WriteFile(settings, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:hook"); err != nil {
		t.Fatalf("import hooks: %v\n%s", err, out)
	}
	// BOTH native events get enriched (both now refused by ingest).
	enriched := `{"hooks": {
		"PreToolUse":  [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo pre", "timeout": 5}]}],
		"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo post", "timeout": 5}]}]
	}}`
	if err := os.WriteFile(settings, []byte(enriched), 0o644); err != nil {
		t.Fatal(err)
	}
	// The named import may fail its own capture ("not found" semantics differ);
	// what matters is the retirement side effect, so ignore the exit error.
	out, _ := runCLI(t, env, "import", "claude:hook:PreToolUse")
	pre := filepath.Join(tmp, ".agentsync", "hooks", "PreToolUse.toml")
	post := filepath.Join(tmp, ".agentsync", "hooks", "PostToolUse.toml")
	if _, err := os.Stat(pre); !os.IsNotExist(err) {
		t.Fatalf("named import should retire the named refused event; stat err=%v\n%s", err, out)
	}
	if _, err := os.Stat(post); err != nil {
		t.Fatalf("named import must NOT retire the sibling event the user didn't name: %v\n%s", err, out)
	}
}

// TestImport_MalformedHookShapeNeverRetires pins the structural/semantic split:
// a settings.json typo (an event value that is not an array) warns and skips
// capture, but must never delete the user's canonical hook file.
func TestImport_MalformedHookShapeNeverRetires(t *testing.T) {
	tmp, env := importTestEnv(t)
	mustRun(t, env, "agent", "add", "claude")
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi"}]}]}}`
	if err := os.WriteFile(settings, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:hook:PreToolUse"); err != nil {
		t.Fatalf("import clean hook: %v\n%s", err, out)
	}
	// A typo makes the event's value a string — structurally malformed.
	if err := os.WriteFile(settings, []byte(`{"hooks": {"PreToolUse": "oops"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude"); err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "hooks", "PreToolUse.toml")); err != nil {
		t.Fatalf("a malformed native shape must never retire canonical config: %v", err)
	}
}

// TestImport_RetireDisownIsScopeExact pins that a user-scope retirement disowns
// only user-scope state keys: a project-scope "/hooks/<event>" key (whose
// canonical overlay file is untouched by a user-scope RemoveHooks) must
// survive, or the next project apply would misclassify its own file as foreign.
func TestImport_RetireDisownIsScopeExact(t *testing.T) {
	tmp, env := importTestEnv(t)
	mustRun(t, env, "agent", "add", "claude")
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi"}]}]}}`
	if err := os.WriteFile(settings, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:hook:PreToolUse"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	// Plant a project-scope key for the same agent+event alongside the real
	// user-scope one written by the import's state seeding. The planted key
	// MUST use the exact spelling render.RecordOpsState emits — the
	// "${HOME}/…" form paths.HomeRelative produces — or the survival
	// assertion is trivially true against a spelling the disown could never
	// match anyway. What this pins is the SCOPE leg (dropping sc.String()
	// from scopeMid disowns this key and fails the test); at user scope the
	// project leg is empty on both sides, so the project-leg distinction
	// between two different projects is exercised only via the scope word —
	// the accepted residual documented on retireRefusedHookEvents.
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	st, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	const projectKey = "claude:project:${HOME}/proj:${HOME}/proj/.claude/settings.json:/hooks/PreToolUse"
	st.Keys[projectKey] = state.KeyEntry{SHA256: "deadbeef", SourceID: "hooks/PreToolUse.toml"}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}

	enriched := `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi", "timeout": 30}]}]}}`
	if err := os.WriteFile(settings, []byte(enriched), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude"); err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	st2, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st2.Keys[projectKey]; !ok {
		t.Fatal("user-scope retirement disowned a PROJECT-scope key whose canonical overlay file survives")
	}
	for key := range st2.Keys {
		if strings.HasPrefix(key, "claude:user:") && strings.HasSuffix(key, ":/hooks/PreToolUse") {
			t.Fatalf("user-scope key should have been disowned by the retirement: %s", key)
		}
	}
}

// TestImport_DryRunPreviewsStaleHookRetirement pins preview honesty for the
// stale-hook retirement: `import --dry-run` used to return before
// retireRefusedHookEvents ran, silently omitting a mutation the real run
// performs. The dry-run must announce the would-be retirement — through a pure
// read (RefusedHookEvents + os.Stat) — while deleting nothing and leaving
// state byte-identical. The named selector previews the same note.
func TestImport_DryRunPreviewsStaleHookRetirement(t *testing.T) {
	tmp, env := importTestEnv(t)
	mustRun(t, env, "agent", "add", "claude")
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi"}]}]}}`
	if err := os.WriteFile(settings, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:hook:PreToolUse"); err != nil {
		t.Fatalf("import clean hook: %v\n%s", err, out)
	}
	canonical := filepath.Join(tmp, ".agentsync", "hooks", "PreToolUse.toml")
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("precondition: canonical hook not captured: %v", err)
	}
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	// The user enriches the native entry with a field agentsync cannot model,
	// so ingest refuses the event and a REAL import would retire the file.
	enriched := `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi", "timeout": 30}]}]}}`
	if err := os.WriteFile(settings, []byte(enriched), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would retire canonical hooks/PreToolUse.toml") {
		t.Fatalf("dry-run must preview the stale-hook retirement:\n%s", out)
	}
	if strings.Contains(out, "retired canonical") {
		t.Fatalf("dry-run must not claim a retirement actually happened:\n%s", out)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("dry-run must not delete the canonical hook file: %v", err)
	}
	stateAfter, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("dry-run must not touch state:\nbefore: %s\nafter:  %s", stateBefore, stateAfter)
	}

	// The named selector previews the retirement too (the capture itself errors
	// "not found" since ingest refused the event — same as the real named run —
	// so only the note is asserted, not the exit code).
	out, _ = runCLI(t, env, "import", "claude:hook:PreToolUse", "--dry-run")
	if !strings.Contains(out, "would retire canonical hooks/PreToolUse.toml") {
		t.Fatalf("named dry-run must preview the stale-hook retirement:\n%s", out)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("named dry-run must not delete the canonical hook file: %v", err)
	}
}

// TestImport_RetireStateFailureLeavesCanonicalIntact pins the disown-BEFORE-
// remove ordering: when the state file cannot be loaded (planted as a
// directory — EISDIR defeats even a privileged container), retirement must
// warn and leave the canonical hook file IN PLACE, so a re-run can retry.
// Under the pre-fix order the file was removed first: a state failure then
// stranded every agent's key owned with no canonical render — the next apply
// clobbered their native configs and a re-run had nothing left to trigger the
// disown.
func TestImport_RetireStateFailureLeavesCanonicalIntact(t *testing.T) {
	tmp, env := importTestEnv(t)
	mustRun(t, env, "agent", "add", "claude")
	settings := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi"}]}]}}`
	if err := os.WriteFile(settings, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:hook:PreToolUse"); err != nil {
		t.Fatalf("import clean hook: %v\n%s", err, out)
	}
	enriched := `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi", "timeout": 30}]}]}}`
	if err := os.WriteFile(settings, []byte(enriched), 0o644); err != nil {
		t.Fatal(err)
	}

	// Plant the state file as a DIRECTORY so state.Load fails deterministically.
	statePath := filepath.Join(tmp, ".agentsync", ".state", "targets.json")
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0o755); err != nil {
		t.Fatal(err)
	}

	out, _ := runCLI(t, env, "import", "claude") // import may error on the planted state; the invariant is below
	if !strings.Contains(out, "retirement skipped this") {
		t.Fatalf("state failure should surface the skip-and-retry warning:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "hooks", "PreToolUse.toml")); err != nil {
		t.Fatalf("state failure must leave the canonical hook file in place for a retry (disown-first ordering): %v\n%s", err, out)
	}
}
