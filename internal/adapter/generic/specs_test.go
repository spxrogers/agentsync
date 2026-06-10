package generic_test

import (
	"path/filepath"
	"reflect"
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

		// Dialect knobs only make sense when MCP is supported. Enumerate via
		// reflection so a NEW MCPTarget knob is covered automatically — the
		// whole point of this guard (cf. TestNewSecretFieldGuard).
		if !mcp {
			v := reflect.ValueOf(s.MCP)
			tt := v.Type()
			for i := 0; i < v.NumField(); i++ {
				if tt.Field(i).Name == "User" || tt.Field(i).Name == "Project" {
					continue
				}
				if v.Field(i).Kind() == reflect.String && v.Field(i).String() != "" {
					t.Errorf("spec %q sets MCP dialect knob %s but no MCP path", s.Name, tt.Field(i).Name)
				}
			}
		}
		// StdioValue/RemoteValue only meaningful with a TransportKey.
		if s.MCP.TransportKey == "" && (s.MCP.StdioValue != "" || s.MCP.RemoteValue != "") {
			t.Errorf("spec %q sets transport values without a TransportKey", s.Name)
		}
		// SSEURLKey only meaningful alongside an explicit RemoteURLKey (the
		// dual-URL dialect), and they must differ.
		if s.MCP.SSEURLKey != "" && (s.MCP.RemoteURLKey == "" || s.MCP.SSEURLKey == s.MCP.RemoteURLKey) {
			t.Errorf("spec %q SSEURLKey requires a distinct RemoteURLKey", s.Name)
		}
	}
	// The breadth tier is exactly the documented 22 agents; the count is
	// asserted across README/docs/comparison, so a row added or dropped here
	// must update those in the same commit (this failure is the reminder).
	if got := len(generic.Specs()); got != 22 {
		t.Fatalf("breadth tier has %d specs, docs say 22 — update the docs and this pin together", got)
	}
}
