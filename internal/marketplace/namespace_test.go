package marketplace

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// TestNamespaceProjected covers the projection-time rename that resolves
// cross-plugin name collisions (issue #211): a plugin-provided component's Name —
// the thing every adapter derives its destination path and identity from — is
// rewritten to "<plugin>-<name>", with the upstream name kept in BaseName.
func TestNamespaceProjected(t *testing.T) {
	t.Run("renames every name-keyed component and stamps provenance", func(t *testing.T) {
		pr := ProjectionResult{
			Skills:    []source.Skill{{Name: "code-review", Frontmatter: map[string]any{"name": "code-review"}}},
			Subagents: []source.Subagent{{Name: "code-reviewer", Frontmatter: map[string]any{"name": "code-reviewer"}}},
			Commands:  []source.Command{{Name: "review", Frontmatter: map[string]any{"description": "d"}}},
		}
		namespaceProjected(&pr, "feature-dev")
		for _, tc := range []struct{ got, want, field string }{
			{pr.Skills[0].Name, "feature-dev-code-review", "skill Name"},
			{pr.Skills[0].BaseName, "code-review", "skill BaseName"},
			{pr.Skills[0].Plugin, "feature-dev", "skill Plugin"},
			{pr.Subagents[0].Name, "feature-dev-code-reviewer", "subagent Name"},
			{pr.Subagents[0].BaseName, "code-reviewer", "subagent BaseName"},
			{pr.Subagents[0].Plugin, "feature-dev", "subagent Plugin"},
			{pr.Commands[0].Name, "feature-dev-review", "command Name"},
			{pr.Commands[0].BaseName, "review", "command BaseName"},
			{pr.Commands[0].Plugin, "feature-dev", "command Plugin"},
		} {
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
			}
		}
	})

	// Renaming only the struct field would leave the components still colliding:
	// Codex prefers the frontmatter `name` over the file stem when deriving its
	// TOML `name` (which IS the agent's identity), and Claude's Agent Skills
	// require the frontmatter name to match the skill directory.
	t.Run("rewrites a present frontmatter name", func(t *testing.T) {
		pr := ProjectionResult{Subagents: []source.Subagent{
			{Name: "reviewer", Frontmatter: map[string]any{"name": "reviewer", "model": "opus"}},
		}}
		namespaceProjected(&pr, "pkg")
		if got := pr.Subagents[0].Frontmatter["name"]; got != "pkg-reviewer" {
			t.Errorf("frontmatter name = %v, want pkg-reviewer", got)
		}
		if got := pr.Subagents[0].Frontmatter["model"]; got != "opus" {
			t.Errorf("other frontmatter keys must survive; model = %v", got)
		}
	})

	// An absent name stays absent: inventing one would assert an identity the
	// upstream artifact never declared, and for Claude a subagent's name is
	// user-visible invocation surface.
	t.Run("leaves an absent frontmatter name absent", func(t *testing.T) {
		pr := ProjectionResult{Subagents: []source.Subagent{
			{Name: "reviewer", Frontmatter: map[string]any{"description": "d"}},
		}}
		namespaceProjected(&pr, "pkg")
		if _, ok := pr.Subagents[0].Frontmatter["name"]; ok {
			t.Errorf("namespacing must not invent a frontmatter name; got %v", pr.Subagents[0].Frontmatter)
		}
	})

	// The frontmatter map can be shared with the caller's parsed artifact, so the
	// rename copies rather than mutating in place.
	t.Run("does not mutate the caller's frontmatter map", func(t *testing.T) {
		fm := map[string]any{"name": "reviewer"}
		pr := ProjectionResult{Subagents: []source.Subagent{{Name: "reviewer", Frontmatter: fm}}}
		namespaceProjected(&pr, "pkg")
		if fm["name"] != "reviewer" {
			t.Errorf("the caller's map was mutated in place; got %v", fm)
		}
	})

	// Hooks have no name key; MCP/LSP are id-keyed and guarded by
	// checkProjectedConflicts, whose hard failure on a same-id divergence is a
	// deliberate security property (a silent endpoint hijack is a case to refuse,
	// not to rename apart).
	t.Run("leaves mcp, lsp, and hooks alone", func(t *testing.T) {
		pr := ProjectionResult{
			MCPServers: []source.MCPServer{{ID: "srv"}},
			LSPServers: []source.LSPServer{{ID: "gopls"}},
		}
		namespaceProjected(&pr, "pkg")
		if pr.MCPServers[0].ID != "srv" || pr.LSPServers[0].ID != "gopls" {
			t.Errorf("id-keyed components must not be namespaced: %+v %+v", pr.MCPServers, pr.LSPServers)
		}
	})

	// Namespacing itself NEVER fails. An earlier revision validated the derived
	// name here and returned an error, which was a regression: the projection's
	// own validateProjectedName permits ':' and control runes, so a plugin that
	// projected fine before started hard-failing — and loadProjected propagates a
	// projection error regardless of `lenient`, taking down the read-only
	// commands whose whole design is to degrade and show state.
	//
	// The safety property is unchanged because it lives downstream at the single
	// dispatch waist: render.Plan runs source.ValidateComponentID over every
	// component id before any of them is joined into a destination path. This
	// pins BOTH halves — namespacing does not fail, and the derived name is still
	// caught by the validator that matters.
	t.Run("hostile plugin ids namespace without error and are caught downstream", func(t *testing.T) {
		for _, plugin := range []string{"a:b", "esc\x1b[31m"} {
			pr := ProjectionResult{Subagents: []source.Subagent{{Name: "reviewer"}}}
			namespaceProjected(&pr, plugin)
			got := pr.Subagents[0].Name
			if got != plugin+"-reviewer" {
				t.Errorf("plugin %q: derived name = %q", plugin, got)
			}
			if err := source.ValidateComponentID("subagent", got); err == nil {
				t.Errorf("render.Plan's validator must still reject the derived name %q", got)
			}
		}
	})

	// An empty plugin id means "not plugin-provided" — hand-authored components
	// flow through the same code path untouched.
	t.Run("empty plugin is a no-op", func(t *testing.T) {
		pr := ProjectionResult{Subagents: []source.Subagent{{Name: "reviewer"}}}
		namespaceProjected(&pr, "")
		if pr.Subagents[0].Name != "reviewer" || pr.Subagents[0].Plugin != "" {
			t.Errorf("an empty plugin id must change nothing; got %+v", pr.Subagents[0])
		}
	})
}

// TestCheckProjectedConflicts_NameKeyedComponents covers the guard that closes
// the residual namespacing cannot: the derived-name mapping is not injective.
//
// Plugin "a" shipping "b-c" and plugin "a-b" shipping "c" both derive "a-b-c",
// and a user who hand-authors "feature-dev-code-reviewer" collides with what
// plugin "feature-dev" derives. Without this guard those reach the render
// pipeline, where DIVERGENT content aborts with a message that can name neither
// origin, and IDENTICAL content is silently deduped — dropping a component with
// no report at all.
func TestCheckProjectedConflicts_NameKeyedComponents(t *testing.T) {
	sub := func(name, plugin, base, body string) source.Subagent {
		return source.Subagent{Name: name, Plugin: plugin, BaseName: base, Body: body}
	}

	t.Run("divergent cross-plugin derived names are fatal and name both origins", func(t *testing.T) {
		c := source.Canonical{Subagents: []source.Subagent{
			sub("a-b-c", "a", "b-c", "from a"),
			sub("a-b-c", "a-b", "c", "from a-b"),
		}}
		err := checkProjectedConflicts(&c, false)
		if err == nil {
			t.Fatal("two plugins deriving one name with different content must be fatal")
		}
		for _, want := range []string{`plugin "a"`, `plugin "a-b"`, `as "b-c"`, `as "c"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must carry %s so the user can tell the sides apart; got: %v", want, err)
			}
		}
	})

	// This is the case that contradicted provenance.go's claim that "a plugin can
	// never take a name the user chose" — it can, via the derived name.
	t.Run("a plugin colliding with a hand-authored name names the user's file", func(t *testing.T) {
		c := source.Canonical{Subagents: []source.Subagent{
			sub("feature-dev-code-reviewer", "", "", "mine"),
			sub("feature-dev-code-reviewer", "feature-dev", "code-reviewer", "theirs"),
		}}
		err := checkProjectedConflicts(&c, false)
		if err == nil {
			t.Fatal("a plugin's derived name colliding with a hand-authored one must be fatal")
		}
		if !strings.Contains(err.Error(), `your own canonical subagent "feature-dev-code-reviewer"`) {
			t.Errorf("error must point at the user's own file; got: %v", err)
		}
	})

	// Identical content is harmless: render dedups it to one byte-identical
	// write, exactly as for MCP/LSP. Erroring here would fail a no-op.
	t.Run("identical duplicates pass", func(t *testing.T) {
		c := source.Canonical{Subagents: []source.Subagent{
			sub("a-b-c", "a", "b-c", "same"),
			sub("a-b-c", "a-b", "c", "same"),
		}}
		if err := checkProjectedConflicts(&c, false); err != nil {
			t.Fatalf("identical duplicates render identically and must pass; got: %v", err)
		}
	})

	// The read-only commands (status/diff/explain) exist to SHOW a problem; they
	// must not refuse to run on one.
	t.Run("lenient degrades to a warning", func(t *testing.T) {
		// Assert the WARNING, not just the absence of an error: "returned nil"
		// is equally true of a guard that did nothing at all, which is exactly
		// the regression this subtest is meant to catch.
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
		t.Cleanup(func() { slog.SetDefault(prev) })

		c := source.Canonical{Subagents: []source.Subagent{
			sub("a-b-c", "a", "b-c", "from a"),
			sub("a-b-c", "a-b", "c", "from a-b"),
		}}
		if err := checkProjectedConflicts(&c, true); err != nil {
			t.Fatalf("lenient must warn, not fail; got: %v", err)
		}
		got := buf.String()
		// slog's TextHandler escapes the quotes inside the attribute value, so
		// match the escaped form the warning actually carries.
		for _, want := range []string{"a-b-c", `plugin \"a\"`, `plugin \"a-b\"`, `(as \"b-c\")`, `(as \"c\")`} {
			if !strings.Contains(got, want) {
				t.Errorf("the lenient warning must still name %s; got:\n%s", want, got)
			}
		}
	})

	// A skill is a DIRECTORY: an identical SKILL.md with different bundled files
	// still renders a different tree, so it is a divergence.
	t.Run("skills compare bundled files too", func(t *testing.T) {
		mk := func(plugin, base string, files []source.SkillFile) source.Skill {
			return source.Skill{Name: "p-s", Plugin: plugin, BaseName: base, Body: "same", Files: files}
		}
		c := source.Canonical{Skills: []source.Skill{
			mk("p", "s", []source.SkillFile{{Path: "scripts/run.sh", Content: []byte("A"), Mode: 0o755}}),
			mk("p-s", "", []source.SkillFile{{Path: "scripts/run.sh", Content: []byte("B"), Mode: 0o755}}),
		}}
		if err := checkProjectedConflicts(&c, false); err == nil {
			t.Fatal("skills differing only in a bundled file must be a divergence")
		}
	})

	t.Run("commands are covered", func(t *testing.T) {
		c := source.Canonical{Commands: []source.Command{
			{Name: "a-b", Plugin: "a", BaseName: "b", Body: "x"},
			{Name: "a-b", Body: "y"},
		}}
		if err := checkProjectedConflicts(&c, false); err == nil {
			t.Fatal("commands share the failure class and must be guarded")
		}
	})

	// Provenance never reaches a destination file, so two sources differing ONLY
	// on it render identically and are not a collision.
	t.Run("provenance alone is not a divergence", func(t *testing.T) {
		c := source.Canonical{Subagents: []source.Subagent{
			sub("a-b-c", "a", "b-c", "same"),
			sub("a-b-c", "", "", "same"),
		}}
		if err := checkProjectedConflicts(&c, false); err != nil {
			t.Fatalf("differing only on provenance must not be a collision; got: %v", err)
		}
	})
}

// TestProvenanceInvariant pins the relationship the three fields must hold:
// when Plugin is set, Name IS the namespaced form of BaseName. Everything
// downstream reads Name as the effective identity (destination paths, collision
// keys) while diagnostics read Plugin/BaseName, so a component whose fields
// disagree would report an origin that does not match what it rendered.
//
// It walks ProjectionResult by REFLECTION rather than naming each component
// slice, so a future component kind added to the rename loop is covered without
// anyone remembering to extend this test — any struct field named Plugin/
// BaseName/Name is checked wherever it appears.
func TestProvenanceInvariant(t *testing.T) {
	pr := ProjectionResult{
		Skills:    []source.Skill{{Name: "s"}, {Name: "with-hyphen"}},
		Subagents: []source.Subagent{{Name: "a"}, {Name: "b-c"}},
		Commands:  []source.Command{{Name: "cmd"}},
	}
	const plugin = "my-plugin"
	namespaceProjected(&pr, plugin)

	// Reflect over every slice on ProjectionResult, and over every element that
	// carries all three fields. A component kind with no BaseName (MCP/LSP/hooks
	// — stamped but never renamed) is skipped by the field check itself, so the
	// invariant applies exactly where it is meant to.
	v := reflect.ValueOf(pr)
	checked := 0
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if field.Kind() != reflect.Slice {
			continue
		}
		kind := v.Type().Field(i).Name
		for j := 0; j < field.Len(); j++ {
			el := field.Index(j)
			nameF, pluginF, baseF := el.FieldByName("Name"), el.FieldByName("Plugin"), el.FieldByName("BaseName")
			if !nameF.IsValid() || !pluginF.IsValid() || !baseF.IsValid() {
				continue // not a renamed kind
			}
			checked++
			name, gotPlugin, base := nameF.String(), pluginF.String(), baseF.String()
			if gotPlugin != plugin {
				t.Errorf("%s[%d] %q: Plugin = %q, want %q", kind, j, name, gotPlugin, plugin)
				continue
			}
			if want := source.NamespacedComponentName(gotPlugin, base); name != want {
				t.Errorf("%s[%d]: Name = %q but NamespacedComponentName(%q, %q) = %q — "+
					"a component's reported origin must match the name it renders as",
					kind, j, name, gotPlugin, base, want)
			}
		}
	}
	// Guard the guard: a renamed KIND that the fixture leaves empty would
	// contribute zero checks and quietly satisfy a bare count. Assert that every
	// slice whose element type carries BaseName — i.e. every renamed kind, present
	// and future — was actually populated and walked.
	renamedKinds := 0
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if field.Kind() != reflect.Slice {
			continue
		}
		if _, ok := field.Type().Elem().FieldByName("BaseName"); !ok {
			continue
		}
		renamedKinds++
		if field.Len() == 0 {
			t.Errorf("%s is a renamed kind but the fixture leaves it empty — "+
				"this test would pass without ever checking it", v.Type().Field(i).Name)
		}
	}
	if renamedKinds != 3 {
		t.Fatalf("expected 3 renamed kinds (skills, subagents, commands), found %d — "+
			"a kind was added or removed and this test needs its fixture extended", renamedKinds)
	}
	if checked != 5 {
		t.Fatalf("expected to check 5 renamed components, checked %d — "+
			"the reflection walk is not seeing what the fixture provides", checked)
	}

	// MCP/LSP are stamped but deliberately NOT renamed: their ids are the
	// security-relevant key a same-id divergence is refused on, never renamed
	// apart. The invariant above must not be read as applying to them.
	pr2 := ProjectionResult{
		MCPServers: []source.MCPServer{{ID: "srv"}},
		LSPServers: []source.LSPServer{{ID: "gopls"}},
		Hooks:      []source.Hook{{Event: untrusted.Wrap("PreToolUse"), Matcher: "Bash", Type: "command", Command: "guard"}},
	}
	namespaceProjected(&pr2, plugin)
	if pr2.MCPServers[0].ID != "srv" || pr2.MCPServers[0].Plugin != plugin {
		t.Errorf("mcp server must be stamped but not renamed; got %+v", pr2.MCPServers[0])
	}
	if pr2.LSPServers[0].ID != "gopls" || pr2.LSPServers[0].Plugin != plugin {
		t.Errorf("lsp server must be stamped but not renamed; got %+v", pr2.LSPServers[0])
	}
	h := pr2.Hooks[0]
	if h.Event.Unverified() != "PreToolUse" || h.Matcher != "Bash" || h.Command != "guard" {
		t.Errorf("a hook must not be rewritten by namespacing; got %+v", h)
	}
	if h.Plugin != plugin {
		t.Errorf("a hook must still be stamped so import can filter it per handler; got %+v", h)
	}
}

// TestDedupHooks_KeysOnContentNotProvenance pins the rekey. dedupHooks used to
// key on the whole source.Hook struct; adding a Plugin field would then have made
// two byte-identical handlers from different plugins survive as duplicates,
// rendering the same hook twice.
//
// Today namespaceProjected stamps provenance AFTER this runs, so every hook here
// shares one value and the bug could not fire — which is exactly why it is worth
// pinning: the ordering is not something a future change should have to know.
func TestDedupHooks_KeysOnContentNotProvenance(t *testing.T) {
	mk := func(plugin string) source.Hook {
		return source.Hook{
			Event: untrusted.Wrap("PreToolUse"), Matcher: "Bash",
			Type: "command", Command: "guard", Plugin: plugin,
		}
	}
	got := dedupHooks([]source.Hook{mk("a"), mk("b"), mk("a")})
	if len(got) != 1 {
		t.Fatalf("identical handlers must collapse regardless of provenance; got %d: %+v", len(got), got)
	}
	// Genuinely different handlers still survive.
	other := mk("a")
	other.Command = "different"
	if got := dedupHooks([]source.Hook{mk("a"), other}); len(got) != 2 {
		t.Fatalf("distinct handlers must not be collapsed; got %d: %+v", len(got), got)
	}
}
