package marketplace

import (
	"os"
	"testing"
)

// TestProjectWithFuncs_LenientConflict verifies the read-only/diagnostic
// projection mode: a strict same-name conflict that the fatal path errors on is
// instead resolved entry-wins with no error, so status/diff/explain can still
// show state. The fatal path (lenient=false) must still error on the same input.
func TestProjectWithFuncs_LenientConflict(t *testing.T) {
	cacheDir := "/cache"
	files := map[string][]byte{
		"/cache/.claude-plugin/plugin.json": []byte(`{"name":"p","mcpServers":{"srv":{"command":"base"}}}`),
	}
	readFile := func(p string) ([]byte, error) {
		if d, ok := files[p]; ok {
			return d, nil
		}
		return nil, os.ErrNotExist
	}
	// Strict (default) + a differing same-id entry override = a conflict.
	entry := PluginEntry{Name: "p", MCPServers: map[string]any{"srv": map[string]any{"command": "entry"}}}

	// Fatal mode errors.
	if _, err := projectWithFuncs(entry, cacheDir, readFile, nil, false); err == nil {
		t.Fatal("fatal mode must error on a strict conflict")
	}

	// Lenient mode resolves entry-wins, no error.
	pr, err := projectWithFuncs(entry, cacheDir, readFile, nil, true)
	if err != nil {
		t.Fatalf("lenient mode must not error on a strict conflict: %v", err)
	}
	if len(pr.MCPServers) != 1 {
		t.Fatalf("lenient should dedup to 1 mcp server, got %d", len(pr.MCPServers))
	}
	if got := pr.MCPServers[0].Server.Command; got != "entry" {
		t.Errorf("lenient should resolve entry-wins; command = %q, want entry", got)
	}
}

// TestProjectWithFuncs_LenientNoFalseWarn confirms lenient mode is a no-op when
// there is no conflict (identical content dedups silently, distinct ids coexist).
func TestProjectWithFuncs_LenientNoFalseWarn(t *testing.T) {
	files := map[string][]byte{
		"/cache/.claude-plugin/plugin.json": []byte(`{"name":"p","mcpServers":{"a":{"command":"x"}}}`),
	}
	readFile := func(p string) ([]byte, error) {
		if d, ok := files[p]; ok {
			return d, nil
		}
		return nil, os.ErrNotExist
	}
	entry := PluginEntry{Name: "p", MCPServers: map[string]any{"b": map[string]any{"command": "y"}}}
	pr, err := projectWithFuncs(entry, "/cache", readFile, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.MCPServers) != 2 {
		t.Fatalf("distinct ids must both survive; got %d", len(pr.MCPServers))
	}
}

// TestLoadComponentEntry_RejectsTraversalName closes the divergence between
// the marketplace projection and the source loader twin: the loader validates
// component names (a name becomes a path segment at render time, so a hostile
// plugin frontmatter "name: ../../evil" is zip-slip-by-name), but the
// marketplace projection's loaders did not. marketplace.Project has no
// production caller today (the runtime path projects via the validating source
// loader), but it is exported API and the two paths must not diverge.
func TestLoadComponentEntry_RejectsTraversalName(t *testing.T) {
	readFile := func(string) ([]byte, error) {
		return []byte("---\nname: ../../evil\n---\nbody\n"), nil
	}
	if _, err := loadMarkdownEntry("cmd.md", readFile); err == nil {
		t.Fatal("expected loadMarkdownEntry to reject a traversal name")
	}
	if _, err := loadSkillEntry("skills/x", readFile); err == nil {
		t.Fatal("expected loadSkillEntry to reject a traversal name")
	}
}

// TestLoadComponentEntry_AllowsNormalName confirms the guard does not reject a
// legitimate component name.
func TestLoadComponentEntry_AllowsNormalName(t *testing.T) {
	readFile := func(string) ([]byte, error) {
		return []byte("---\nname: code-review\n---\nbody\n"), nil
	}
	if _, err := loadMarkdownEntry("cmd.md", readFile); err != nil {
		t.Fatalf("legitimate name rejected: %v", err)
	}
}
