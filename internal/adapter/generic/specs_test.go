package generic_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter/generic"
)

// deepAdapterNames are the hand-written adapters. A generic Spec must never
// collide with one (that would double-register / shadow the richer adapter).
var deepAdapterNames = map[string]bool{
	"claude": true, "opencode": true, "codex": true, "cursor": true,
	"gemini": true, "continue": true, "windsurf": true, "roo": true, "cline": true,
}

// TestSpecsTable is the reflective guard over the breadth-tier table: every entry
// must be well-formed, so a typo in a data row fails the build rather than
// silently producing a broken adapter.
func TestSpecsTable(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range generic.Specs() {
		if s.Name == "" {
			t.Fatalf("spec with empty name: %+v", s)
		}
		if seen[s.Name] {
			t.Fatalf("duplicate generic spec name %q", s.Name)
		}
		seen[s.Name] = true
		if deepAdapterNames[s.Name] {
			t.Fatalf("generic spec %q collides with a deep adapter", s.Name)
		}

		mem := s.Memory.User != "" || s.Memory.Project != ""
		mcp := s.MCP.User != "" || s.MCP.Project != ""
		if !mem && !mcp {
			t.Fatalf("spec %q supports neither memory nor MCP", s.Name)
		}

		// Paths must be relative (joined under a scope root) and not escape it.
		for _, p := range []string{s.Memory.User, s.Memory.Project, s.MCP.User, s.MCP.Project, s.DetectDir} {
			if p == "" {
				continue
			}
			if filepath.IsAbs(p) {
				t.Errorf("spec %q has absolute path %q", s.Name, p)
			}
			if strings.Contains(p, "..") {
				t.Errorf("spec %q path %q contains ..", s.Name, p)
			}
		}

		// Dialect knobs only make sense when MCP is supported.
		if !mcp {
			if s.MCP.RootKey != "" || s.MCP.TransportKey != "" || s.MCP.StdioValue != "" || s.MCP.RemoteURLKey != "" {
				t.Errorf("spec %q sets MCP dialect knobs but no MCP path", s.Name)
			}
		}
		// StdioValue/RemoteValue only meaningful with a TransportKey.
		if s.MCP.TransportKey == "" && (s.MCP.StdioValue != "" || s.MCP.RemoteValue != "") {
			t.Errorf("spec %q sets transport values without a TransportKey", s.Name)
		}
	}
	if len(generic.Specs()) < 15 {
		t.Fatalf("breadth tier unexpectedly small (%d) — did the table get truncated?", len(generic.Specs()))
	}
}
