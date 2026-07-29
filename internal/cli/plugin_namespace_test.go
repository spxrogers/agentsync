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
	//
	// Assert the SKIP is announced, not merely that a file is absent: if ingest
	// simply stopped seeing the namespaced files, the absence checks below would
	// be equally green while the filter did nothing. The warning is the positive
	// control that the filter is what suppressed them.
	for _, component := range []string{"subagent", "skill", "command"} {
		out, err := runCLI(t, env, "import", "claude:"+component)
		if err != nil {
			t.Fatalf("import claude:%s: %v\n%s", component, err, out)
		}
		if !strings.Contains(out, "it is projected from the plugin") {
			t.Errorf("import claude:%s must announce the skip, not silently omit it; got:\n%s",
				component, out)
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
	// Assert the REFUSAL text, not just the plugin name: reconcile echoes the
	// item label (.claude/agents/feature-dev-code-reviewer.md) on every outcome,
	// including a successful write-back, so matching "feature-dev" alone would
	// pass even if the refusal never fired.
	out, _ := runCLI(t, env, "reconcile", "--auto-writeback")
	if !strings.Contains(out, "projected from the plugin") {
		t.Errorf("reconcile must refuse write-back with its own reason; got:\n%s", out)
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

// TestApply_NamespacingMigrationReclaimsPreRenameFiles covers the case every
// existing user actually hits: the ONE-TIME rename on the first apply after
// upgrading.
//
// It differs materially from a plain removal (which apply_orphan_rename_test.go
// covers). Here the plan still renders ops into the same directory, so the
// orphan pass must exclude the freshly-rendered namespaced paths while still
// reclaiming the bare pre-rename one. Get that wrong in either direction and you
// either delete the new file or leave the old one — and Claude Code reads every
// file in its agents directory, so a leftover is a duplicate agent, not a
// harmless artifact.
//
// The pre-rename state is simulated by applying a hand-authored component under
// the bare name, then handing the same component to a plugin.
func TestApply_NamespacingMigrationReclaimsPreRenameFiles(t *testing.T) {
	tmp, env := importTestEnv(t)

	// 1. Pre-upgrade world: the component exists under its BARE name, applied
	//    and recorded in state exactly as a pre-namespacing apply left it.
	bare := filepath.Join(tmp, ".agentsync", "subagents", "code-reviewer.md")
	if err := os.WriteFile(bare, []byte("---\nname: code-reviewer\n---\nReview the feature branch.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("pre-rename apply: %v\n%s", err, out)
	}
	dest := filepath.Join(tmp, ".claude", "agents", "code-reviewer.md")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("pre-rename apply did not write the bare name: %v", err)
	}

	// 2. Upgrade: the component now comes from a plugin, so it renders namespaced.
	if err := os.Remove(bare); err != nil {
		t.Fatal(err)
	}
	mpDir := makeCollidingMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("official-mp", mpDir, "feature-dev"))
	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("post-rename apply: %v\n%s", err, out)
	}

	if _, err := os.Stat(dest); err == nil {
		t.Fatal("the pre-rename destination file was not reclaimed; it would load as a duplicate agent")
	}
	renamed := filepath.Join(tmp, ".claude", "agents", "feature-dev-code-reviewer.md")
	if _, err := os.Stat(renamed); err != nil {
		t.Fatalf("the namespaced destination must exist after the rename: %v", err)
	}
}

// TestReconcile_WriteBackRefusesPluginSkillBundledFile pins the bundled-file
// half of the SourceID mapping. A skill is a DIRECTORY: pluginProvidedSourceIDs
// registers one entry per bundled file (scripts/, references/, assets/), not
// just SKILL.md. Only covering SKILL.md would leave a hand-edited script
// capturable — minting a canonical skill directory that collides with the
// plugin's own.
func TestReconcile_WriteBackRefusesPluginSkillBundledFile(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeBundledSkillMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("bundle-mp", mpDir, "bundler"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	script := filepath.Join(tmp, ".claude", "skills", "bundler-deploy", "scripts", "run.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the plugin's bundled file must render under the namespaced skill: %v", err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho HAND EDITED\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, _ := runCLI(t, env, "reconcile", "--auto-writeback")
	if !strings.Contains(out, "projected from the plugin") {
		t.Errorf("write-back of a plugin skill's bundled file must be refused; got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "skills", "bundler-deploy")); err == nil {
		t.Fatal("write-back minted a canonical copy of a plugin-provided skill directory")
	}
}

// makeBundledSkillMarketplace builds a marketplace with one plugin whose skill
// carries a bundled script, so the skill-is-a-directory paths are exercised.
func makeBundledSkillMarketplace(t *testing.T, dir string) string {
	t.Helper()
	mpDir := filepath.Join(dir, "bundle-marketplace")
	write := func(rel, body string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(mpDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude-plugin/marketplace.json", `{
		"name": "bundle-mp",
		"owner": {"name": "tester"},
		"plugins": [{"name": "bundler", "source": "./plugins/bundler"}]
	}`, 0o644)
	write("plugins/bundler/.claude-plugin/plugin.json", `{"name":"bundler","version":"1.0.0"}`, 0o644)
	write("plugins/bundler/skills/deploy/SKILL.md",
		"---\nname: deploy\ndescription: ship it\n---\nSteps.\n", 0o644)
	write("plugins/bundler/skills/deploy/scripts/run.sh", "#!/bin/sh\necho original\n", 0o755)
	return mpDir
}

// TestProjectScope_PluginNamespacingAndImportFilter covers project scope, which
// the rest of this file's user-scope tests leave untested.
//
// Two distinct properties are at stake, and both come from the same rule: the
// skip set must be exactly the RENDERED set. At project scope every adapter
// renders from the project-only overlay (`renderC = *c.Project`), never the user
// canonical — so a project plugin's components are namespaced and refused, while
// a USER-scope plugin contributes nothing to the project destination and must
// not shadow a project component that merely shares its derived name.
func TestProjectScope_PluginNamespacingAndImportFilter(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "init", "--scope", "project", "--project", proj); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude", "--scope", "project", "--project", proj); err != nil {
		t.Fatal(err)
	}

	// Install the plugin at USER scope — `import claude:plugin` is user-scope
	// only, since a plugin is a user-scope concept across every supported
	// harness — then commit its plugins/<id>.toml into the PROJECT tree. That is
	// how a project tree actually declares a plugin: the pin travels with the
	// repo while the fetched cache stays under the user home.
	mpDir := makeCollidingMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmpHome, directoryMarketplaceSettings("official-mp", mpDir, "feature-dev"))
	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("user plugin import: %v\n%s", err, out)
	}
	pin, err := os.ReadFile(filepath.Join(tmpHome, ".agentsync", "plugins", "feature-dev.toml"))
	if err != nil {
		t.Fatalf("the user-scope plugin pin should exist: %v", err)
	}
	projPlugins := filepath.Join(proj, ".agentsync", "plugins")
	if err := os.MkdirAll(projPlugins, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projPlugins, "feature-dev.toml"), pin, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply", "--scope", "project", "--project", proj); err != nil {
		t.Fatalf("project apply: %v\n%s", err, out)
	}

	// Namespacing applies at project scope too.
	renamed := filepath.Join(proj, ".claude", "agents", "feature-dev-code-reviewer.md")
	if _, err := os.Stat(renamed); err != nil {
		t.Fatalf("a project-scope plugin's subagent must render namespaced: %v", err)
	}

	// A hand-authored PROJECT component is still captured — the filter must not
	// over-refuse just because a plugin is installed at this scope.
	mine := filepath.Join(proj, ".claude", "agents", "project-only.md")
	if err := os.WriteFile(mine, []byte("---\nname: project-only\n---\nMine.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:subagent", "--scope", "project", "--project", proj); err != nil {
		t.Fatalf("project subagent import: %v\n%s", err, out)
	}
	captured := filepath.Join(proj, ".agentsync", "subagents", "project-only.md")
	if _, err := os.Stat(captured); err != nil {
		t.Fatalf("a hand-authored project subagent must still be captured: %v", err)
	}
	// ...and the plugin's own is not.
	if _, err := os.Stat(filepath.Join(proj, ".agentsync", "subagents", "feature-dev-code-reviewer.md")); err == nil {
		t.Fatal("import captured a project-scope plugin's subagent into the project source tree")
	}

	// The project tree must still apply cleanly after the import.
	if out, err := runCLI(t, env, "apply", "--scope", "project", "--project", proj); err != nil {
		t.Fatalf("project apply after import: %v\n%s", err, out)
	}
}

// TestProjectScope_UserPluginDoesNotShadowProjectComponent is the test that can
// actually FAIL on the scope fix, which the sibling above cannot: it copies the
// plugin pin into the project tree, so the old user-union logic and the new
// project-only logic produce an identical skip set.
//
// Here the plugin exists at USER scope ONLY. It therefore contributes nothing to
// the project destination, and a project component that happens to share its
// derived name must still be captured. Under the old union the user plugin's
// entry would appear in the project skip set and silently swallow it.
func TestProjectScope_UserPluginDoesNotShadowProjectComponent(t *testing.T) {
	tmpHome := t.TempDir()
	proj := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmpHome}

	for _, args := range [][]string{
		{"init"},
		{"agent", "add", "claude"},
		{"init", "--scope", "project", "--project", proj},
		{"agent", "add", "claude", "--scope", "project", "--project", proj},
	} {
		if out, err := runCLI(t, env, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	// Plugin installed at USER scope only — the project tree gets no pin.
	mpDir := makeCollidingMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmpHome, directoryMarketplaceSettings("official-mp", mpDir, "feature-dev"))
	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("user plugin import: %v\n%s", err, out)
	}

	// A PROJECT-scope native subagent named exactly what the user-scope plugin
	// derives. It is the user's own file at this scope and must be captured.
	agentsDir := filepath.Join(proj, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "feature-dev-code-reviewer.md"),
		[]byte("---\nname: feature-dev-code-reviewer\n---\nProject's own.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := runCLI(t, env, "import", "claude:subagent", "--scope", "project", "--project", proj); err != nil {
		t.Fatalf("project subagent import: %v\n%s", err, out)
	}
	captured := filepath.Join(proj, ".agentsync", "subagents", "feature-dev-code-reviewer.md")
	data, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("a user-scope plugin must not shadow a project component sharing its derived name: %v", err)
	}
	if !strings.Contains(string(data), "Project's own.") {
		t.Fatalf("captured the wrong content:\n%s", data)
	}
}

// TestImport_DryRunSkipsPluginProvidedComponents pins that the preview tells the
// same story as the real run. A dry-run that listed plugin components as
// "would import" would be worse than useless — it would advertise a capture the
// real import refuses.
func TestImport_DryRunSkipsPluginProvidedComponents(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeCollidingMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("official-mp", mpDir, "feature-dev"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	out, err := runCLI(t, env, "import", "claude:subagent", "--dry-run")
	if err != nil {
		t.Fatalf("import --dry-run: %v\n%s", err, out)
	}
	if strings.Contains(out, "would import subagents/feature-dev-code-reviewer.md") {
		t.Errorf("dry-run must not advertise a capture the real import refuses; got:\n%s", out)
	}
	if !strings.Contains(out, "it is projected from the plugin") {
		t.Errorf("dry-run must report the skip like the real run; got:\n%s", out)
	}
}

// makeServerMarketplace builds a marketplace whose plugin ships an MCP server
// and a hook — the two component kinds that are plugin-owned but NOT namespaced,
// so the capture refusal has to recognise them by id/content rather than by name.
func makeServerMarketplace(t *testing.T, dir string) string {
	t.Helper()
	mpDir := filepath.Join(dir, "server-marketplace")
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
		"name": "srv-mp",
		"owner": {"name": "tester"},
		"plugins": [{"name": "srv", "source": "./plugins/srv"}]
	}`)
	write("plugins/srv/.claude-plugin/plugin.json", `{
		"name": "srv",
		"version": "1.0.0",
		"mcpServers": {"pluginapi": {"command": "plugin-server", "args": ["--serve"]}},
		"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "plugin-guard"}]}]}
	}`)
	return mpDir
}

// TestImport_SkipsPluginProvidedMCPServer covers the component kind that is
// plugin-owned but NOT namespaced. An MCP server keeps its id (a same-id
// divergence across sources is a possible endpoint hijack, refused rather than
// renamed apart), so the refusal must recognise it by id — and capturing one
// still mints a canonical mcp/<id>.toml that renders identically today and
// diverges the moment the plugin updates, at which point every load hard-fails.
func TestImport_SkipsPluginProvidedMCPServer(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeServerMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("srv-mp", mpDir, "srv"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	out, err := runCLI(t, env, "import", "claude:mcp")
	if err != nil {
		t.Fatalf("import claude:mcp: %v\n%s", err, out)
	}
	if !strings.Contains(out, "it is projected from the plugin") {
		t.Errorf("import must announce the skipped plugin MCP server; got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".agentsync", "mcp", "pluginapi.toml")); err == nil {
		t.Fatal("import captured a plugin-provided MCP server into the canonical source")
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply after import must still succeed: %v\n%s", err, out)
	}
}

// TestImport_SkipsPluginHooksButKeepsYourOwn pins that hooks are filtered at
// HANDLER granularity. A canonical hooks/<event>.toml holds handlers from many
// sources, so refusing the whole event because a plugin contributed one would
// silently drop the user's own — an over-refusal that loses real config.
func TestImport_SkipsPluginHooksButKeepsYourOwn(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeServerMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("srv-mp", mpDir, "srv"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	// A hand-written handler on the SAME event the plugin contributes to.
	hooksSrc := filepath.Join(tmp, ".agentsync", "hooks")
	if err := os.MkdirAll(hooksSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksSrc, "PreToolUse.toml"),
		[]byte("[[hook]]\nmatcher = \"Edit\"\ntype = \"command\"\ncommand = \"my-own-guard\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	// Remove the canonical copy so import has to re-capture from native config.
	if err := os.Remove(filepath.Join(hooksSrc, "PreToolUse.toml")); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "import", "claude:hook"); err != nil {
		t.Fatalf("import claude:hook: %v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(hooksSrc, "PreToolUse.toml"))
	if err != nil {
		t.Fatalf("the user's own handler must still be captured: %v", err)
	}
	if !strings.Contains(string(data), "my-own-guard") {
		t.Fatalf("the user's own handler was dropped:\n%s", data)
	}
	if strings.Contains(string(data), "plugin-guard") {
		t.Fatalf("the plugin's handler must not be captured:\n%s", data)
	}
}

// TestImport_FailsClosedWhenPluginProjectionFails pins the fail-CLOSED contract.
//
// The skip filter is what stops import capturing agentsync's own output back
// into the canonical source. It is computed from plugin data, so a plugin the
// user does not control can make that computation fail — an upstream roll
// changes the tree hash and the manifest-SHA pin no longer verifies. If a
// failure left the filter empty, that would be a way to switch the defence off
// on demand, so the import refuses instead.
func TestImport_FailsClosedWhenPluginProjectionFails(t *testing.T) {
	tmp, env := importTestEnv(t)
	mpDir := makeCollidingMarketplace(t, t.TempDir())
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings("official-mp", mpDir, "feature-dev"))

	if out, err := runCLI(t, env, "import", "claude:plugin"); err != nil {
		t.Fatalf("import claude:plugin: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}

	// Tamper the cached plugin tree so the pinned manifest SHA no longer
	// verifies — the projection now fails for a reason outside the user's
	// control, which is exactly the trigger the guard must survive.
	cached := filepath.Join(tmp, ".agentsync", ".state", "cache", "plugins",
		"feature-dev", "agents", "code-reviewer.md")
	if err := os.WriteFile(cached, []byte("---\nname: code-reviewer\n---\nTAMPERED.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "import", "claude:subagent")
	if err == nil {
		t.Fatalf("import must refuse when it cannot determine what the plugins provide; got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "cannot safely import") {
		t.Errorf("the refusal should explain itself; got: %v", err)
	}
	// Assert the WRAPPED cause too, so an incidental failure (a missing file, a
	// parse error elsewhere) cannot satisfy this test in place of the tamper the
	// fixture actually induces.
	if !strings.Contains(err.Error(), "manifest SHA mismatch") {
		t.Errorf("the refusal must be caused by the induced tamper; got: %v", err)
	}
	// Nothing may have been captured.
	if _, serr := os.Stat(filepath.Join(tmp, ".agentsync", "subagents", "feature-dev-code-reviewer.md")); serr == nil {
		t.Fatal("a failed projection must not let a plugin component be captured")
	}
}
