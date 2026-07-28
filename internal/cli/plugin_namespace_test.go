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

// TestImport_SkipsPluginProvidedComponents closes the loop between `apply` and
// `import`. An adapter's Ingest reads the agent's native config and cannot tell a
// file agentsync rendered from a plugin apart from one the user hand-wrote — they
// are the same kind of file in the same directory. So `apply` then `import`
// copied every plugin-provided component into ~/.agentsync/ as if the user had
// authored it, and the next load held TWO components of that name (the captured
// copy and the plugin's own projection) rendering to one destination path, which
// apply refuses.
//
// The end-to-end oracle is that apply still works afterwards: capturing a
// plugin's output must not brick the next run.
func TestImport_SkipsPluginProvidedComponents(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeCollidingMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("official-mp", mpDir, "feature-dev"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	// Re-import every text component. The plugin's own output is now sitting in
	// the native config, and must not be captured back.
	for _, component := range []string{"subagent", "skill", "command"} {
		if out, err := runCLI(t, env, "import", "claude:"+component); err != nil {
			t.Fatalf("import claude:%s: %v\n%s", component, err, out)
		}
	}

	for _, rel := range []string{
		filepath.Join("subagents", "feature-dev-code-reviewer.md"),
		filepath.Join("skills", "feature-dev-code-review", "SKILL.md"),
		filepath.Join("commands", "feature-dev-review.md"),
	} {
		if _, err := os.Stat(filepath.Join(tmp, ".agentsync", rel)); err == nil {
			t.Errorf("import captured the plugin-provided component %s into the canonical source; "+
				"it would collide with the plugin's own projection on the next apply", rel)
		}
	}

	// The real proof: the next apply still works. Before the fix it failed with
	// two components resolving to one destination path.
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply after import must still succeed: %v\n%s", err, out)
	}
}

// TestImport_NamedPluginComponentIsAnError pins that a NAMED import of a
// plugin-provided component says why rather than silently importing nothing. The
// user asked for that exact component; "no output" would read as a bug.
func TestImport_NamedPluginComponentIsAnError(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeCollidingMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("official-mp", mpDir, "feature-dev"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	out, err := runCLI(t, env, "import", "claude:subagent:feature-dev-code-reviewer")
	if err == nil {
		t.Fatalf("importing a plugin-provided component by name must error; got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "feature-dev") {
		t.Errorf("the error should name the providing plugin; got: %v", err)
	}
}

// TestImport_StillCapturesHandAuthoredAlongsidePlugins is the other half: the
// filter must be surgical. A component the user hand-wrote into the native
// config is still captured even while a plugin's components are being skipped —
// otherwise the fix would silently break ordinary import for anyone with a
// plugin installed.
func TestImport_StillCapturesHandAuthoredAlongsidePlugins(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeCollidingMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("official-mp", mpDir, "feature-dev"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// A subagent the user wrote straight into the agent's native config.
	mine := filepath.Join(tmp, ".claude", "agents", "my-own.md")
	if err := os.WriteFile(mine, []byte("---\nname: my-own\n---\nMine.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := runCLI(t, env, "import", "claude:subagent"); err != nil {
		t.Fatalf("import claude:subagent: %v\n%s", err, out)
	}
	captured := filepath.Join(tmp, ".agentsync", "subagents", "my-own.md")
	data, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("a hand-authored native subagent must still be captured: %v", err)
	}
	if !strings.Contains(string(data), "Mine.") {
		t.Fatalf("captured the wrong content:\n%s", data)
	}
}

// TestReconcile_WriteBackRefusesPluginProvidedComponent is the sibling of the
// import filter, on the other dest→source path. A plugin-provided component has
// no canonical file of its own — it is projected from the plugin cache on every
// load — so capturing a hand-edit of one would MINT a canonical file with the
// component's namespaced name, and the next load would hold two components of
// that name rendering to one destination path.
//
// The oracle is again the next apply: reconcile must not leave the tree in a
// state that apply refuses.
func TestReconcile_WriteBackRefusesPluginProvidedComponent(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeCollidingMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("official-mp", mpDir, "feature-dev"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	// Hand-edit the plugin's rendered subagent, so reconcile sees drift.
	dest := filepath.Join(tmp, ".claude", "agents", "feature-dev-code-reviewer.md")
	if err := os.WriteFile(dest,
		[]byte("---\nname: feature-dev-code-reviewer\n---\nHAND EDITED\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// --auto-writeback must refuse this item rather than mint a canonical copy.
	out, _ := runCLI(t, env, "reconcile", "--auto-writeback")
	if !strings.Contains(out, "feature-dev") {
		t.Errorf("reconcile should explain that the component comes from a plugin; got:\n%s", out)
	}
	captured := filepath.Join(tmp, ".agentsync", "subagents", "feature-dev-code-reviewer.md")
	if _, err := os.Stat(captured); err == nil {
		t.Fatal("write-back minted a canonical copy of a plugin-provided component; " +
			"it collides with the plugin's own projection on the next apply")
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply after reconcile must still succeed: %v\n%s", err, out)
	}
}
