package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeCollidingMarketplace builds a local marketplace with TWO plugins that each
// ship a same-named subagent, skill, and command, mirroring the real reproduction
// in issue #211: feature-dev@claude-plugins-official and
// pr-review-toolkit@claude-plugins-official both ship agents/code-reviewer.md.
//
// The bodies differ per plugin deliberately. Identical content would be deduped
// by the apply pipeline's shared-path dedup and prove nothing — the failure this
// guards against is precisely two DIVERGENT components claiming one destination.
func makeCollidingMarketplace(t *testing.T, dir string) string {
	t.Helper()
	mpDir := filepath.Join(dir, "colliding-marketplace")

	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(mpDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(".claude-plugin/marketplace.json", `{
		"name": "official-mp",
		"owner": {"name": "tester"},
		"plugins": [
			{"name": "feature-dev", "source": "./plugins/feature-dev"},
			{"name": "pr-review-toolkit", "source": "./plugins/pr-review-toolkit"}
		]
	}`)

	for _, p := range []struct{ id, model, body string }{
		{"feature-dev", "sonnet", "Review the feature branch."},
		{"pr-review-toolkit", "opus", "Review the pull request."},
	} {
		write("plugins/"+p.id+"/.claude-plugin/plugin.json", `{"name":"`+p.id+`","version":"1.0.0"}`)
		write("plugins/"+p.id+"/agents/code-reviewer.md",
			"---\nname: code-reviewer\ndescription: review code\nmodel: "+p.model+"\n---\n"+p.body+"\n")
		write("plugins/"+p.id+"/skills/code-review/SKILL.md",
			"---\nname: code-review\ndescription: review code\n---\n"+p.body+"\n")
		write("plugins/"+p.id+"/commands/review.md",
			"---\ndescription: review code\n---\n"+p.body+"\n")
	}
	return mpDir
}

// TestApply_CrossPluginCollisionsNamespaceApart is the end-to-end regression for
// issue #211: two installed plugins each shipping a same-named component made
// `status` and `apply` exit 1, and every remedy the error named was one the user
// structurally could not perform — both files live under the marketplace-managed
// plugin cache, which is overwritten on the next update.
//
// The oracle is the ON-DISK RESULT, not the parsed model: a round-trip over a
// model that cannot represent two same-named components would be trivially
// "lossless" while dropping one of them. So this asserts the rendered file tree
// holds both components, at distinct paths, with distinct bodies.
//
// Codex is enabled alongside Claude because the two adapters failed differently
// and BOTH must be fixed — codex refused during Render (its `name` is the agent's
// identity, so two TOMLs cannot claim one), while Claude emitted two write ops at
// one destination path and tripped the apply pipeline's divergence guard.
func TestApply_CrossPluginCollisionsNamespaceApart(t *testing.T) {
	tmp, env := importTestEnv(t) // inits + enables claude
	if _, err := runCLI(t, env, "agent", "add", "codex"); err != nil {
		t.Fatalf("agent add codex: %v", err)
	}
	mpDir := makeCollidingMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings(
		"official-mp", mpDir, "feature-dev", "pr-review-toolkit",
	))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	// The pre-fix failure was here: `status` exited 1 with "codex subagents
	// \"code-reviewer\" and \"code-reviewer\" resolve to the same agent name".
	if out, err := runCLI(t, env, "status"); err != nil {
		t.Fatalf("status must not fail on a cross-plugin name collision: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply must not fail on a cross-plugin name collision: %v\n%s", err, out)
	}

	mustContain := func(path, want string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("expected a namespaced component at %s: %v", path, err)
		}
		if !strings.Contains(string(data), want) {
			t.Fatalf("%s should carry %q; got:\n%s", path, want, data)
		}
	}

	// Both plugins' subagents survive, at distinct destination paths, each
	// carrying its OWN body — neither silently overwrote the other.
	agents := filepath.Join(tmp, ".claude", "agents")
	mustContain(filepath.Join(agents, "feature-dev-code-reviewer.md"), "Review the feature branch.")
	mustContain(filepath.Join(agents, "pr-review-toolkit-code-reviewer.md"), "Review the pull request.")

	// The frontmatter `name` follows the rename. Renaming only the file would
	// leave both still colliding: Claude treats `name` as the agent's identity
	// (and documents it must be unique across the tree), and Codex prefers the
	// frontmatter name over the file stem when deriving its TOML `name`.
	mustContain(filepath.Join(agents, "feature-dev-code-reviewer.md"), "name: feature-dev-code-reviewer")
	mustContain(filepath.Join(agents, "pr-review-toolkit-code-reviewer.md"), "name: pr-review-toolkit-code-reviewer")

	// The bare pre-namespace name must not exist at all: leaving it would be the
	// third, ambiguous agent Claude Code would still load.
	if _, err := os.Stat(filepath.Join(agents, "code-reviewer.md")); err == nil {
		t.Fatal("the bare colliding name must not be rendered; found .claude/agents/code-reviewer.md")
	}

	// Codex renders subagents as TOML keyed by `name` — the case that failed
	// loudest — so assert its on-disk identity too.
	codexAgents := filepath.Join(tmp, ".codex", "agents")
	mustContain(filepath.Join(codexAgents, "feature-dev-code-reviewer.toml"), `name = 'feature-dev-code-reviewer'`)
	mustContain(filepath.Join(codexAgents, "pr-review-toolkit-code-reviewer.toml"), `name = 'pr-review-toolkit-code-reviewer'`)

	// Skills and commands share the failure class — both are name-keyed and
	// render to a name-derived destination path — so both are namespaced too.
	skills := filepath.Join(tmp, ".claude", "skills")
	mustContain(filepath.Join(skills, "feature-dev-code-review", "SKILL.md"), "Review the feature branch.")
	mustContain(filepath.Join(skills, "pr-review-toolkit-code-review", "SKILL.md"), "Review the pull request.")
	mustContain(filepath.Join(skills, "feature-dev-code-review", "SKILL.md"), "name: feature-dev-code-review")

	commands := filepath.Join(tmp, ".claude", "commands")
	mustContain(filepath.Join(commands, "feature-dev-review.md"), "Review the feature branch.")
	mustContain(filepath.Join(commands, "pr-review-toolkit-review.md"), "Review the pull request.")
}

// TestApply_HandAuthoredComponentKeepsBareName pins the other half of the
// contract: namespacing applies to PLUGIN-provided components only. A component
// the user wrote in ~/.agentsync/ keeps the bare name they chose, even when an
// installed plugin ships one by the same name — a plugin can never take a name
// out from under the user, and an upgrade never renames their own files.
func TestApply_HandAuthoredComponentKeepsBareName(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeCollidingMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("official-mp", mpDir, "feature-dev"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	// A hand-authored subagent whose name matches what the plugin ships.
	srcDir := filepath.Join(tmp, ".agentsync", "subagents")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "code-reviewer.md"),
		[]byte("---\nname: code-reviewer\n---\nMy own reviewer.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	agents := filepath.Join(tmp, ".claude", "agents")
	mine, err := os.ReadFile(filepath.Join(agents, "code-reviewer.md"))
	if err != nil {
		t.Fatalf("a hand-authored subagent must keep its bare name: %v", err)
	}
	if !strings.Contains(string(mine), "My own reviewer.") {
		t.Fatalf("the bare name must belong to the USER's subagent; got:\n%s", mine)
	}
	if _, err := os.Stat(filepath.Join(agents, "feature-dev-code-reviewer.md")); err != nil {
		t.Fatalf("the plugin's subagent should still render, namespaced: %v", err)
	}
}
