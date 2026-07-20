package windsurf

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestIngest_GlobalRulesDoesNotClobberProjectRule pins the P1 guard: the memory
// read must project EXACTLY one source per scope, and a populated project-rule
// body can never be overwritten by the global rules read. ResolvePaths keeps
// RulesDir/GlobalRules mutually exclusive per scope, so this exercises the guard
// directly on readMemoryBody with a hand-constructed Paths in which BOTH point at
// real on-disk files — the only way to prove the else-if guard holds regardless
// of resolution order. Under the old two-independent-if structure the global read
// would win; here the project rule must.
func TestIngest_GlobalRulesDoesNotClobberProjectRule(t *testing.T) {
	root := t.TempDir()
	rulesDir := filepath.Join(root, ".windsurf", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Project workspace rule with the agentsync activation fence + a distinctive body.
	projectBody := "# Project rule\n\nProject wins.\n"
	if err := os.WriteFile(filepath.Join(rulesDir, memoryRuleFile), []byte(memoryRuleFrontmatter+projectBody), 0o644); err != nil {
		t.Fatal(err)
	}
	// A global rules file planted on disk with DIFFERENT content.
	globalPath := filepath.Join(root, ".codeium", "windsurf", "memories", "global_rules.md")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	globalBody := "# Global rule\n\nGlobal must NOT clobber.\n"
	if err := os.WriteFile(globalPath, []byte(globalBody), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		paths    Paths
		wantBody string
		wantOK   bool
	}{
		{
			// Both sources present on disk: the project rule must win and the global
			// read must not overwrite it (the guard).
			name:     "both present: project rule wins",
			paths:    Paths{RulesDir: rulesDir, GlobalRules: globalPath},
			wantBody: projectBody,
			wantOK:   true,
		},
		{
			// User scope: only the global file is set; it is the single source.
			name:     "global only: global body returned",
			paths:    Paths{GlobalRules: globalPath},
			wantBody: globalBody,
			wantOK:   true,
		},
		{
			// Project scope: only the project rule is set.
			name:     "project only: project body returned",
			paths:    Paths{RulesDir: rulesDir},
			wantBody: projectBody,
			wantOK:   true,
		},
		{
			// Neither source resolvable: no body found.
			name:   "neither: no source",
			paths:  Paths{},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var warn bytes.Buffer
			a := New(Options{Stderr: &warn})
			got, ok, err := a.readMemoryBody(tt.paths)
			if err != nil {
				t.Fatalf("readMemoryBody: unexpected error: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (body %q)", ok, tt.wantOK, got)
			}
			if got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}
