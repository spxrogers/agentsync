package adapter_test

import (
	"errors"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// TestRequireProjectRoot pins the adapter-boundary guard: only the
// (ScopeProject, "") combination is rejected — every other pairing passes. This
// is the single source of truth each adapter's Render/Ingest delegates to, so a
// project-scope call with no root can never silently fall through to user paths.
func TestRequireProjectRoot(t *testing.T) {
	tests := []struct {
		name    string
		scope   adapter.Scope
		project string
		wantErr bool
	}{
		{"user scope, empty project — ok", adapter.ScopeUser, "", false},
		{"user scope, with project — ok", adapter.ScopeUser, "/repo", false},
		{"project scope, with root — ok", adapter.ScopeProject, "/repo", false},
		{"project scope, empty root — error", adapter.ScopeProject, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := adapter.RequireProjectRoot(tt.scope, tt.project)
			if tt.wantErr {
				if !errors.Is(err, adapter.ErrProjectRootRequired) {
					t.Fatalf("want ErrProjectRootRequired, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}
}
